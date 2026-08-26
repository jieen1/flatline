package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/canonical"
	"flatline/internal/eventstore"
	"flatline/internal/runtime"
	"flatline/internal/storage"
)

const aggregateProject = "/synthetic/project-agg"

var aggregateStart = time.Date(2026, 7, 2, 9, 15, 0, 0, time.UTC)

// aggregateFixtureDB builds one synthetic project: a main session, a subagent
// thread under it, an empty session, the command and file projections, and the
// same friction in two sessions so a recurring signature exists. Every row is
// fixture data and is never read from a real local history.
func aggregateFixtureDB(t *testing.T) *storage.DB {
	t.Helper()
	db := testAPIDB(t)
	ctx := context.Background()
	store := eventstore.New(db)

	mainID := aggregateSession(t, store, "agg-main", aggregateStart, "分析会话")
	secondID := aggregateSession(t, store, "agg-second", aggregateStart.Add(24*time.Hour), "第二次会话")
	subID := aggregateSession(t, store, "agg-sub", aggregateStart.Add(time.Hour), "子代理线程")
	emptyID := aggregateSession(t, store, "agg-empty", aggregateStart.Add(2*time.Hour), "空会话")

	exec(t, db, `UPDATE sessions SET thread_kind = 'main', originator = 'codex-tui' WHERE id IN (?, ?, ?)`, mainID, secondID, emptyID)
	exec(t, db, `UPDATE sessions SET thread_kind = 'subagent', parent_session_id = ?, agent_role = 'explore', agent_nickname = '侦察', originator = 'codex-tui' WHERE id = ?`, mainID, subID)
	exec(t, db, `UPDATE session_stats SET is_empty = 1 WHERE session_id = ?`, emptyID)

	// The same missing file, hit in two different sessions: one signature,
	// two sessions, which is what "recurring" means.
	for _, sessionID := range []string{mainID, secondID} {
		events := []canonical.Event{
			frictionTestCallEvent(sessionID, sessionID+"-call", aggregateStart.Add(time.Minute), map[string]any{
				"tool_name": "Bash", "tool_use_id": sessionID + "-call-1", "tool_input": `{"command":"ls /synthetic/project-agg/notes.md"}`,
			}),
			frictionTestEvent(sessionID, sessionID+"-result", aggregateStart.Add(2*time.Minute), map[string]any{
				"tool_use_id": sessionID + "-call-1",
				"tool_output": "ls: cannot access '/synthetic/project-agg/notes.md': No such file or directory",
				"exit_code":   2,
			}),
		}
		if _, err := store.IngestEvents(ctx, sessionID, events); err != nil {
			t.Fatalf("ingest events: %v", err)
		}
		if _, err := store.IngestFriction(ctx, sessionID, events); err != nil {
			t.Fatalf("ingest friction: %v", err)
		}
	}

	// The tool usage projection is what /tools reads, so the fixture has to
	// project the sessions it just ingested. The command and file projections
	// below are hand-written instead, so the rows the projection derived from
	// the same events are cleared first.
	for _, sessionID := range []string{mainID, secondID, subID, emptyID} {
		if err := store.RecomputeSessionProjections(ctx, sessionID); err != nil {
			t.Fatalf("project session %s: %v", sessionID, err)
		}
	}
	exec(t, db, `DELETE FROM session_commands`)
	exec(t, db, `DELETE FROM session_files`)
	// The projection recomputes is_empty from the stats; this fixture states
	// which session is the empty one itself.
	exec(t, db, `UPDATE session_stats SET is_empty = 0`)
	exec(t, db, `UPDATE session_stats SET is_empty = 1 WHERE session_id = ?`, emptyID)

	exec(t, db, `INSERT INTO session_commands (session_id, event_id, tool_name, program, command, exit_code, is_error, occurred_at)
		VALUES (?, 9001, 'Bash', 'go', 'go test ./...', 0, 0, ?), (?, 9002, 'Bash', 'go', 'go build ./...', 1, 0, ?), (?, 9003, 'Bash', 'rg', 'rg TODO', 0, 0, ?)`,
		mainID, aggregateStart.Format(time.RFC3339), mainID, aggregateStart.Format(time.RFC3339), subID, aggregateStart.Format(time.RFC3339))
	exec(t, db, `INSERT INTO session_files (session_id, event_id, path, action, tool_name, occurred_at)
		VALUES (?, 9101, ?, 'read', 'Read', ?), (?, 9102, ?, 'edit', 'Edit', ?), (?, 9103, '/tmp/scratch/draft.md', 'write', 'Write', ?)`,
		mainID, aggregateProject+"/main.go", aggregateStart.Format(time.RFC3339),
		mainID, aggregateProject+"/main.go", aggregateStart.Format(time.RFC3339),
		mainID, aggregateStart.Format(time.RFC3339))
	return db
}

func aggregateSession(t *testing.T, store *eventstore.Store, sourceID string, at time.Time, title string) string {
	t.Helper()
	id, err := store.IngestSession(context.Background(), adapters.SourceCodex, adapters.SessionMeta{
		SourceSessionID: sourceID, StartedAt: &at, CWD: aggregateProject, Model: "synthetic-model", Title: title,
	})
	if err != nil {
		t.Fatalf("ingest session %s: %v", sourceID, err)
	}
	if err := store.RecomputeSessionStats(context.Background(), id); err != nil {
		t.Fatalf("recompute stats %s: %v", sourceID, err)
	}
	return id
}

func TestProjectPageAggregatesOneWorkingDirectory(t *testing.T) {
	handler := NewServerWithDB(aggregateFixtureDB(t)).Handler()
	var page struct {
		Project struct {
			Key         string         `json:"key"`
			Label       string         `json:"label"`
			CWD         *string        `json:"cwd"`
			Sessions    int            `json:"sessions"`
			Harnesses   map[string]int `json:"harnesses"`
			Originators map[string]int `json:"originators"`
		} `json:"project"`
		Sessions map[string]int `json:"sessions"`
		ByWeek   []struct {
			Week     string `json:"week"`
			Sessions int    `json:"sessions"`
		} `json:"by_week"`
		Roles []struct {
			Key   string `json:"key"`
			Count int    `json:"count"`
		} `json:"roles"`
		Friction struct {
			Total     int `json:"total"`
			Recurring []struct {
				Signature    string `json:"signature"`
				SampleLine   string `json:"sample_line"`
				Category     string `json:"category"`
				Count        int    `json:"count"`
				SessionCount int    `json:"session_count"`
			} `json:"recurring"`
			RecurringSignatures int `json:"recurring_signatures"`
		} `json:"friction"`
		HotFiles            []hotFile      `json:"hot_files"`
		OutsideProjectFiles int            `json:"outside_project_files"`
		TopPrograms         []programUsage `json:"top_programs"`
		RecentSessions      []struct {
			ID string `json:"id"`
		} `json:"recent_sessions"`
	}
	getJSON(t, handler, "/api/v1/projects/"+urlEscape(aggregateProject)+"?from=all", &page)

	if page.Project.Key != aggregateProject || page.Project.Label != "project-agg" || page.Project.Sessions != 4 {
		t.Fatalf("project header = %+v", page.Project)
	}
	if page.Project.Harnesses["codex"] != 4 || page.Project.Originators["codex-tui"] != 4 {
		t.Fatalf("project harnesses=%v originators=%v", page.Project.Harnesses, page.Project.Originators)
	}
	if page.Sessions["main"] != 3 || page.Sessions["subagent"] != 1 || page.Sessions["empty"] != 1 || page.Sessions["in_range"] != 4 {
		t.Fatalf("session counts = %+v", page.Sessions)
	}
	if len(page.ByWeek) == 0 {
		t.Fatalf("by_week is empty")
	}
	if len(page.Roles) == 0 || page.Roles[0].Key == "" {
		t.Fatalf("roles = %+v", page.Roles)
	}
	if page.Friction.Total != 2 || page.Friction.RecurringSignatures != 1 {
		t.Fatalf("friction = %+v", page.Friction)
	}
	if len(page.Friction.Recurring) != 1 || page.Friction.Recurring[0].SessionCount != 2 || page.Friction.Recurring[0].Category != "file_not_found" {
		t.Fatalf("recurring = %+v", page.Friction.Recurring)
	}
	if !strings.Contains(page.Friction.Recurring[0].SampleLine, "notes.md") {
		t.Fatalf("sample line = %q, want the normalized evidence line", page.Friction.Recurring[0].SampleLine)
	}
	// The scratch file is outside the project and is counted, not listed.
	if len(page.HotFiles) != 1 || page.HotFiles[0].Path != aggregateProject+"/main.go" || page.HotFiles[0].Edits != 1 || page.HotFiles[0].Reads != 1 {
		t.Fatalf("hot files = %+v", page.HotFiles)
	}
	if page.OutsideProjectFiles != 1 {
		t.Fatalf("outside_project_files = %d, want 1", page.OutsideProjectFiles)
	}
	if len(page.TopPrograms) != 2 || page.TopPrograms[0].Program != "go" || page.TopPrograms[0].Calls != 2 ||
		page.TopPrograms[0].KnownOutcomes != 2 || page.TopPrograms[0].Failures != 1 {
		t.Fatalf("top programs = %+v", page.TopPrograms)
	}
	if len(page.RecentSessions) == 0 {
		t.Fatalf("recent sessions is empty")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+urlEscape("/synthetic/does-not-exist"), nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown project status = %d, want 404", rec.Code)
	}
}

func TestTimeStatsPlacesSessionsInTheViewersHourAndWeekday(t *testing.T) {
	handler := NewServerWithDB(aggregateFixtureDB(t)).Handler()
	var stats struct {
		HourWeekday     [][]int `json:"hour_weekday"`
		ByDayOfWeek     []int   `json:"by_day_of_week"`
		TZOffsetMinutes int     `json:"tz_offset_minutes"`
		ByWeek          []struct {
			Week     string `json:"week"`
			Sessions int    `json:"sessions"`
		} `json:"by_week"`
	}
	getJSON(t, handler, "/api/v1/stats/time?from=all&project="+urlEscape(aggregateProject)+"&tz_offset_minutes=480", &stats)
	if len(stats.HourWeekday) != 7 || len(stats.HourWeekday[0]) != 24 {
		t.Fatalf("hour_weekday shape = %d x %d", len(stats.HourWeekday), len(stats.HourWeekday[0]))
	}
	local := aggregateStart.Add(480 * time.Minute)
	weekday := (int(local.Weekday()) + 6) % 7
	if stats.HourWeekday[weekday][local.Hour()] != 1 {
		t.Fatalf("cell [%d][%d] = %d, want the main session", weekday, local.Hour(), stats.HourWeekday[weekday][local.Hour()])
	}
	if stats.ByDayOfWeek[weekday] < 1 || stats.TZOffsetMinutes != 480 {
		t.Fatalf("by_day_of_week=%v tz=%d", stats.ByDayOfWeek, stats.TZOffsetMinutes)
	}
	// The subagent thread and the empty session are not counted.
	total := 0
	for _, day := range stats.HourWeekday {
		for _, count := range day {
			total += count
		}
	}
	if total != 2 {
		t.Fatalf("counted %d sessions, want 2 main non-empty sessions", total)
	}
	if len(stats.ByWeek) == 0 {
		t.Fatalf("by_week is empty")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stats/time?tz_offset_minutes=abc", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad tz offset status = %d, want 400", rec.Code)
	}
}

func TestToolsEndpointCountsCallsAndRecordedFailures(t *testing.T) {
	handler := NewServerWithDB(aggregateFixtureDB(t)).Handler()
	var response struct {
		Tools    []toolUsage    `json:"tools"`
		Programs []programUsage `json:"programs"`
	}
	getJSON(t, handler, "/api/v1/tools?from=all", &response)
	var bash *toolUsage
	for index := range response.Tools {
		if response.Tools[index].ToolName == "Bash" {
			bash = &response.Tools[index]
		}
	}
	if bash == nil || bash.Calls != 2 || bash.Sessions != 2 || bash.Failures != 2 {
		t.Fatalf("Bash usage = %+v (tools %+v)", bash, response.Tools)
	}
	if len(response.Programs) != 2 || response.Programs[0].Program != "go" || response.Programs[0].Failures != 1 {
		t.Fatalf("programs = %+v", response.Programs)
	}
}

func TestSearchReturnsEachKindOfMatch(t *testing.T) {
	handler := NewServerWithDB(aggregateFixtureDB(t)).Handler()
	var response struct {
		Sessions []struct {
			ID string `json:"id"`
		} `json:"sessions"`
		Projects []struct {
			Key string `json:"key"`
		} `json:"projects"`
		Assets   []searchAsset `json:"assets"`
		Programs []struct {
			Program string `json:"program"`
		} `json:"programs"`
		FrictionCategories []frictionCategoryCount `json:"friction_categories"`
	}
	getJSON(t, handler, "/api/v1/search?q=project-agg", &response)
	if len(response.Projects) != 1 || response.Projects[0].Key != aggregateProject {
		t.Fatalf("search projects = %+v", response.Projects)
	}
	if len(response.Sessions) == 0 {
		t.Fatalf("search sessions is empty")
	}

	getJSON(t, handler, "/api/v1/search?q=fixture", &response)
	if len(response.Assets) != 1 || response.Assets[0].ID != "skill:project:fixture" {
		t.Fatalf("search assets = %+v", response.Assets)
	}

	getJSON(t, handler, "/api/v1/search?q=go", &response)
	if len(response.Programs) != 1 || response.Programs[0].Program != "go" {
		t.Fatalf("search programs = %+v", response.Programs)
	}

	getJSON(t, handler, "/api/v1/search?q=file_not_found", &response)
	if len(response.FrictionCategories) != 1 || response.FrictionCategories[0].Count != 2 {
		t.Fatalf("search friction categories = %+v", response.FrictionCategories)
	}

	getJSON(t, handler, "/api/v1/search?q=", &response)
	if len(response.Sessions) != 0 || len(response.Projects) != 0 {
		t.Fatalf("empty query returned %d sessions and %d projects", len(response.Sessions), len(response.Projects))
	}
}

func TestSessionExportDownloadsTheCurrentFilter(t *testing.T) {
	handler := NewServerWithDB(aggregateFixtureDB(t)).Handler()
	var export struct {
		Sessions []struct {
			ID         string `json:"id"`
			ThreadKind string `json:"thread_kind"`
		} `json:"sessions"`
		Exported  int  `json:"exported"`
		Truncated bool `json:"truncated"`
	}
	rec := getJSON(t, handler, "/api/v1/sessions/export?project="+urlEscape(aggregateProject)+"&from=all", &export)
	if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, `attachment; filename="flatline-sessions-`) || !strings.HasSuffix(got, `.json"`) {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if export.Exported != 2 || export.Truncated {
		t.Fatalf("export = %+v (default filter keeps main, non-empty sessions)", export)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/export?project="+urlEscape(aggregateProject)+"&from=all&thread=all&empty=all&format=csv", nil)
	csvRec := httptest.NewRecorder()
	handler.ServeHTTP(csvRec, req)
	if csvRec.Code != http.StatusOK {
		t.Fatalf("csv export status = %d, body=%s", csvRec.Code, csvRec.Body.String())
	}
	if got := csvRec.Header().Get("Content-Disposition"); !strings.HasSuffix(got, `.csv"`) {
		t.Fatalf("csv Content-Disposition = %q", got)
	}
	records, err := csv.NewReader(strings.NewReader(csvRec.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if len(records) != 5 || records[0][0] != "id" || records[0][len(records[0])-1] != "tags" {
		t.Fatalf("csv has %d rows, header %v", len(records), records[0])
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/export?format=xml", nil)
	badRec := httptest.NewRecorder()
	handler.ServeHTTP(badRec, req)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("unsupported format status = %d, want 400", badRec.Code)
	}
}

func TestIngestHealthReportsCountsAndUnrecordedFields(t *testing.T) {
	handler := NewServerWithDB(aggregateFixtureDB(t)).Handler()
	var health struct {
		SchemaVersion int            `json:"schema_version"`
		DBBytes       *int64         `json:"db_bytes"`
		Counts        map[string]int `json:"counts"`
		Unrecorded    map[string]int `json:"unrecorded"`
		Warnings      []string       `json:"warnings"`
		LastImport    map[string]any `json:"last_import"`
	}
	rec := getJSON(t, handler, "/api/v1/ingest/health", &health)
	if rec.Header().Get("ETag") != "" {
		t.Fatalf("health carries an ETag; it must never be cached")
	}
	if health.SchemaVersion < 9 {
		t.Fatalf("schema_version = %d", health.SchemaVersion)
	}
	if health.DBBytes == nil || *health.DBBytes <= 0 {
		t.Fatalf("db_bytes = %v", health.DBBytes)
	}
	// main_sessions counts the three fixture main threads plus the API fixture
	// session, whose thread kind was never recorded and so is not known to be a
	// subagent; that one is also reported on its own.
	if health.Counts["sessions"] != 5 || health.Counts["main_sessions"] != 4 ||
		health.Counts["subagent_sessions"] != 1 || health.Counts["unrecorded_thread_sessions"] != 1 {
		t.Fatalf("counts = %+v", health.Counts)
	}
	if health.Counts["commands"] != 3 || health.Counts["files"] != 3 {
		t.Fatalf("projection counts = %+v", health.Counts)
	}
	if health.Counts["empty_sessions"] < 1 {
		t.Fatalf("empty_sessions = %d", health.Counts["empty_sessions"])
	}
	// The API fixture session records no working directory; the health report
	// is where that shows up as a number rather than as a blank cell.
	if health.Unrecorded["sessions_without_cwd"] != 1 {
		t.Fatalf("unrecorded = %+v", health.Unrecorded)
	}
	if health.Warnings == nil {
		t.Fatalf("warnings must be an empty list, not null")
	}
	if health.LastImport["phase"] != runtime.PhaseIdle {
		t.Fatalf("last_import = %+v", health.LastImport)
	}
}

// fakeDaemon stands in for the running daemon: it answers the two questions
// the API asks of it and records whether a refresh was requested.
type fakeDaemon struct {
	phase     string
	requested int
}

func (f *fakeDaemon) DataVersion() int64 { return 1 }

func (f *fakeDaemon) Progress() runtime.ImportProgress {
	return runtime.ImportProgress{Phase: f.phase}
}

func (f *fakeDaemon) RequestRefresh() bool {
	if f.phase != runtime.PhaseIdle {
		return false
	}
	f.requested++
	return true
}

func TestIngestRefreshQueuesOnePassAndRefusesWhileImporting(t *testing.T) {
	server := NewServerWithDB(aggregateFixtureDB(t))
	daemon := &fakeDaemon{phase: runtime.PhaseIdle}
	server.SetStatusSource(daemon)
	handler := server.Handler()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/ingest/refresh", nil))
	if rec.Code != http.StatusAccepted || daemon.requested != 1 {
		t.Fatalf("refresh status = %d requested = %d, want 202 and one request", rec.Code, daemon.requested)
	}
	var accepted struct {
		Started bool `json:"started"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil || !accepted.Started {
		t.Fatalf("accepted body = %s", rec.Body.String())
	}

	daemon.phase = runtime.PhaseHistory
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/ingest/refresh", nil))
	if rec.Code != http.StatusConflict || daemon.requested != 1 {
		t.Fatalf("refresh during import status = %d requested = %d, want 409 and no new request", rec.Code, daemon.requested)
	}
	var conflict struct {
		Running bool `json:"running"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &conflict); err != nil || !conflict.Running {
		t.Fatalf("conflict body = %s", rec.Body.String())
	}

	// With no daemon attached the API says so instead of claiming a pass ran.
	rec = httptest.NewRecorder()
	NewServerWithDB(testAPIDB(t)).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/ingest/refresh", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("refresh with no daemon status = %d, want 503", rec.Code)
	}
}

// TestAggregatesDegradeWhenTheProjectionTablesAreMissing drops the command and
// file projections that migration 008 creates. Every endpoint must still
// answer 200 with an empty projection rather than failing, because the two
// migrations of this stage land independently.
func TestAggregatesDegradeWhenTheProjectionTablesAreMissing(t *testing.T) {
	db := aggregateFixtureDB(t)
	exec(t, db, `DROP TABLE session_commands`)
	exec(t, db, `DROP TABLE session_files`)
	handler := NewServerWithDB(db).Handler()

	var page struct {
		Sessions            map[string]int `json:"sessions"`
		HotFiles            []hotFile      `json:"hot_files"`
		OutsideProjectFiles int            `json:"outside_project_files"`
		TopPrograms         []programUsage `json:"top_programs"`
	}
	getJSON(t, handler, "/api/v1/projects/"+urlEscape(aggregateProject)+"?from=all", &page)
	if len(page.HotFiles) != 0 || len(page.TopPrograms) != 0 || page.OutsideProjectFiles != 0 {
		t.Fatalf("degraded project page = %+v", page)
	}
	if page.Sessions["in_range"] != 4 {
		t.Fatalf("degraded project page in_range = %d, want 4", page.Sessions["in_range"])
	}

	var tools struct {
		Tools    []toolUsage    `json:"tools"`
		Programs []programUsage `json:"programs"`
	}
	getJSON(t, handler, "/api/v1/tools?from=all", &tools)
	if len(tools.Programs) != 0 || len(tools.Tools) == 0 {
		t.Fatalf("degraded tools = %+v", tools)
	}

	var health struct {
		Counts map[string]int `json:"counts"`
	}
	getJSON(t, handler, "/api/v1/ingest/health", &health)
	for _, absent := range []string{"commands", "files"} {
		if _, ok := health.Counts[absent]; ok {
			t.Fatalf("degraded health reports %q it cannot know: %+v", absent, health.Counts)
		}
	}

	var search struct {
		Programs []searchProgram `json:"programs"`
	}
	getJSON(t, handler, "/api/v1/search?q=go", &search)
	if len(search.Programs) != 0 {
		t.Fatalf("degraded search programs = %+v", search.Programs)
	}
	getJSON(t, handler, "/api/v1/overview?from=all", nil)
	getJSON(t, handler, "/api/v1/sessions/export?from=all", nil)
}

// TestAggregatesDegradeWhenTheHierarchyColumnsAreMissing drops the session
// hierarchy columns as well. The aggregate endpoints must fall back to counting
// every session rather than failing on a column that is not there yet.
func TestAggregatesDegradeWhenTheHierarchyColumnsAreMissing(t *testing.T) {
	db := aggregateFixtureDB(t)
	exec(t, db, `DROP TABLE session_commands`)
	exec(t, db, `DROP TABLE session_files`)
	exec(t, db, `DROP INDEX idx_sessions_thread`)
	exec(t, db, `DROP INDEX idx_sessions_parent`)
	exec(t, db, `DROP INDEX idx_session_stats_empty`)
	for _, column := range []string{"thread_kind", "parent_session_id", "agent_role", "agent_nickname", "originator"} {
		exec(t, db, `ALTER TABLE sessions DROP COLUMN `+column)
	}
	exec(t, db, `ALTER TABLE session_stats DROP COLUMN is_empty`)
	handler := NewServerWithDB(db).Handler()

	var stats struct {
		HourWeekday [][]int `json:"hour_weekday"`
	}
	getJSON(t, handler, "/api/v1/stats/time?from=all", &stats)
	total := 0
	for _, day := range stats.HourWeekday {
		for _, count := range day {
			total += count
		}
	}
	if total != 5 {
		t.Fatalf("counted %d sessions, want every session when the scope rule cannot be applied", total)
	}

	var tools struct {
		Tools []toolUsage `json:"tools"`
	}
	getJSON(t, handler, "/api/v1/tools?from=all", &tools)
	if len(tools.Tools) == 0 {
		t.Fatalf("degraded tools list is empty")
	}

	var health struct {
		Counts map[string]int `json:"counts"`
	}
	getJSON(t, handler, "/api/v1/ingest/health", &health)
	for _, absent := range []string{"main_sessions", "subagent_sessions", "empty_sessions"} {
		if _, ok := health.Counts[absent]; ok {
			t.Fatalf("degraded health reports %q it cannot know: %+v", absent, health.Counts)
		}
	}
	if health.Counts["sessions"] != 5 {
		t.Fatalf("degraded health sessions = %d", health.Counts["sessions"])
	}
}

// TestDerivedFrictionReachesEventsThatWereAlreadyStored covers the back-fill
// path: the transcript file will never be re-read, so the outcome the harness
// printed into the output text has to be picked up from the stored event.
func TestDerivedFrictionReachesEventsThatWereAlreadyStored(t *testing.T) {
	db := testAPIDB(t)
	ctx := context.Background()
	store := eventstore.New(db)
	at := time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)
	sessionID, err := store.IngestSession(ctx, adapters.SourceCodex, adapters.SessionMeta{
		SourceSessionID: "derive-fixture", StartedAt: &at, CWD: aggregateProject, Title: "回填会话",
	})
	if err != nil {
		t.Fatal(err)
	}
	// A Codex exec result as the older parser stored it: the exit code is in
	// the text, and the call id is on turn_id.
	events := []canonical.Event{
		frictionTestCallEvent(sessionID, "derive-call", at.Add(time.Second), map[string]any{
			"tool_name": "exec_command", "turn_id": "call_derive_1", "tool_input": `{"cmd":"ruff check ."}`,
		}),
		frictionTestEvent(sessionID, "derive-result", at.Add(2*time.Second), map[string]any{
			"turn_id":     "call_derive_1",
			"tool_output": "Chunk ID: 9f1a\nWall time: 0.2 seconds\nProcess exited with code 127\nOutput:\nbash: line 1: ruff: command not found",
		}),
		frictionTestEvent(sessionID, "derive-ok", at.Add(3*time.Second), map[string]any{
			"turn_id":     "call_derive_2",
			"tool_output": "Chunk ID: 9f1b\nProcess exited with code 0\nOutput:\nfine",
		}),
	}
	if _, err := store.IngestEvents(ctx, sessionID, events); err != nil {
		t.Fatal(err)
	}
	if err := store.RecomputeSessionStats(ctx, sessionID); err != nil {
		t.Fatal(err)
	}

	derived, err := store.DeriveMissingFriction(ctx)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if derived != 1 {
		t.Fatalf("derived %d records, want 1 (the zero exit code is not friction)", derived)
	}
	var toolName, category, signature string
	var exitCode int
	if err := db.QueryRowContext(ctx, `
		SELECT tool_name, category, signature, exit_code FROM friction_records WHERE session_id = ?`, sessionID).
		Scan(&toolName, &category, &signature, &exitCode); err != nil {
		t.Fatalf("read derived row: %v", err)
	}
	if toolName != "exec_command" || category != "command_not_found" || exitCode != 127 {
		t.Fatalf("derived row: tool=%q category=%q exit=%d", toolName, category, exitCode)
	}
	if signature != "command_not_found|exec_command|bash: line #: ruff: command not found" {
		t.Fatalf("derived signature = %q", signature)
	}
	var frictionCount int
	if err := db.QueryRowContext(ctx, `SELECT friction_count FROM session_stats WHERE session_id = ?`, sessionID).Scan(&frictionCount); err != nil {
		t.Fatalf("read session stats: %v", err)
	}
	if frictionCount != 1 {
		t.Fatalf("session_stats.friction_count = %d, want the derived record to be counted", frictionCount)
	}

	again, err := store.DeriveMissingFriction(ctx)
	if err != nil {
		t.Fatalf("second derive: %v", err)
	}
	if again != 0 {
		t.Fatalf("second derive inserted %d records; the pass must be idempotent", again)
	}
}
