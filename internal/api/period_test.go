package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/canonical"
	"flatline/internal/eventstore"
	"flatline/internal/storage"
)

// periodStart is the middle of the window the fixture below is built around;
// periodPrevious is the same distance before it, so the previous window is not
// empty and the two can actually be compared.
var (
	periodStart    = time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	periodPrevious = periodStart.AddDate(0, 0, -20)
)

const periodProject = "/synthetic/project-period"

// periodFixtureDB builds two windows of the same synthetic project: three
// overlapping main sessions and one subagent in the current window, one main
// session in the previous one. Every row is fixture data.
func periodFixtureDB(t *testing.T) *storage.DB {
	t.Helper()
	db := testAPIDB(t)
	ctx := context.Background()
	store := eventstore.New(db)

	session := func(id string, start time.Time, minutes int) string {
		end := start.Add(time.Duration(minutes) * time.Minute)
		stored, err := store.IngestSession(ctx, adapters.SourceCodex, adapters.SessionMeta{
			SourceSessionID: id, StartedAt: &start, EndedAt: &end, CWD: periodProject,
			Model: "synthetic-model", Title: id,
		})
		if err != nil {
			t.Fatalf("ingest session %s: %v", id, err)
		}
		if err := store.RecomputeSessionStats(ctx, stored); err != nil {
			t.Fatalf("stats %s: %v", id, err)
		}
		return stored
	}

	// Three sessions open at once at 09:20 — the first two started before it
	// and the third starts on it; the peak is therefore 3.
	first := session("period-a", periodStart, 60)
	second := session("period-b", periodStart.Add(10*time.Minute), 60)
	third := session("period-c", periodStart.Add(20*time.Minute), 5)
	child := session("period-sub", periodStart.Add(25*time.Minute), 5)
	old := session("period-old", periodPrevious, 30)

	exec(t, db, `UPDATE sessions SET thread_kind = 'main' WHERE id IN (?, ?, ?, ?)`, first, second, third, old)
	exec(t, db, `UPDATE sessions SET thread_kind = 'subagent', parent_session_id = ?, agent_role = 'explore' WHERE id = ?`, first, child)

	// A missing command in two sessions of the current window, so the
	// environment block has something to dedupe.
	for _, sessionID := range []string{first, second} {
		events := []canonical.Event{
			frictionTestCallEvent(sessionID, sessionID+"-rf-call", periodStart.Add(time.Minute), map[string]any{
				"tool_name": "Bash", "tool_use_id": sessionID + "-rf", "tool_input": `{"command":"ruff check ."}`,
			}),
			frictionTestEvent(sessionID, sessionID+"-rf-result", periodStart.Add(2*time.Minute), map[string]any{
				"tool_use_id": sessionID + "-rf", "tool_output": "bash: line 1: ruff: command not found", "exit_code": 127,
			}),
		}
		if _, err := store.IngestEvents(ctx, sessionID, events); err != nil {
			t.Fatalf("ingest events: %v", err)
		}
		if _, err := store.IngestFriction(ctx, sessionID, events); err != nil {
			t.Fatalf("ingest friction: %v", err)
		}
		if err := store.RecomputeSessionProjections(ctx, sessionID); err != nil {
			t.Fatalf("project: %v", err)
		}
	}

	// The same file read three times in one session is one reread session.
	for index := 0; index < 3; index++ {
		exec(t, db, `INSERT INTO session_files (session_id, event_id, path, action, tool_name, occurred_at)
			VALUES (?, ?, ?, 'read', 'Read', ?)`,
			first, 8000+index, periodProject+"/loop.go", periodStart.Format(time.RFC3339))
	}
	exec(t, db, `INSERT INTO session_files (session_id, event_id, path, action, tool_name, occurred_at)
		VALUES (?, 8100, ?, 'read', 'Read', ?)`, second, periodProject+"/once.go", periodStart.Format(time.RFC3339))
	return db
}

// periodResponse is the part of the overview this file checks.
type periodResponse struct {
	Current  periodSummary          `json:"current"`
	Previous *periodSummary         `json:"previous"`
	Delta    map[string]*deltaValue `json:"delta"`
	Sessions struct {
		InRange int `json:"in_range"`
	} `json:"sessions"`
	Parallelism parallelismSummary `json:"parallelism"`
	Environment environmentSummary `json:"environment"`
	Subagents   subagentSummary    `json:"subagents"`
	Reread      rereadSummary      `json:"reread"`
}

func periodOverview(t *testing.T, handler http.Handler, query string) periodResponse {
	t.Helper()
	var out periodResponse
	getJSON(t, handler, "/api/v1/overview?"+query, &out)
	return out
}

func TestOverviewPeriodAnswersTheSameWindowAsTheTopLevelCounts(t *testing.T) {
	handler := NewServerWithDB(periodFixtureDB(t)).Handler()
	from := periodStart.AddDate(0, 0, -1).Format("2006-01-02")
	to := periodStart.AddDate(0, 0, 1).Format("2006-01-02")
	page := periodOverview(t, handler, "from="+from+"&to="+to)

	// The period block and the top-level counts are the same window under the
	// same rule, so they cannot disagree about how many sessions it holds.
	if page.Current.Sessions != page.Sessions.InRange {
		t.Fatalf("current.sessions = %d, sessions.in_range = %d", page.Current.Sessions, page.Sessions.InRange)
	}
	if page.Current.Sessions != 3 {
		t.Fatalf("current.sessions = %d, want the 3 main sessions of the window", page.Current.Sessions)
	}
	if page.Previous != nil || len(page.Delta) != 0 {
		t.Fatalf("previous/delta present without compare=1: %+v %+v", page.Previous, page.Delta)
	}
}

func TestOverviewParallelismIsThePeakOfOverlappingSessions(t *testing.T) {
	handler := NewServerWithDB(periodFixtureDB(t)).Handler()
	page := periodOverview(t, handler, "from="+periodStart.AddDate(0, 0, -1).Format("2006-01-02"))
	if page.Parallelism.Peak != 3 {
		t.Fatalf("parallelism.peak = %d, want 3", page.Parallelism.Peak)
	}
	if page.Parallelism.PeakAt == nil {
		t.Fatal("parallelism.peak_at is null but a peak was reported")
	}
	if page.Parallelism.SessionsConsidered != 3 {
		t.Fatalf("sessions_considered = %d, want 3", page.Parallelism.SessionsConsidered)
	}
	if page.Parallelism.Peak > page.Parallelism.SessionsConsidered {
		t.Fatalf("peak %d exceeds its own denominator %d", page.Parallelism.Peak, page.Parallelism.SessionsConsidered)
	}
}

func TestOverviewEnvironmentNamesTheMissingCommandAndItsSessions(t *testing.T) {
	handler := NewServerWithDB(periodFixtureDB(t)).Handler()
	page := periodOverview(t, handler, "from="+periodStart.AddDate(0, 0, -1).Format("2006-01-02"))
	if len(page.Environment.MissingCommands) != 1 {
		t.Fatalf("missing_commands = %+v", page.Environment.MissingCommands)
	}
	item := page.Environment.MissingCommands[0]
	if item.Command != "ruff" {
		t.Fatalf("missing command = %q, want ruff", item.Command)
	}
	if item.Sessions != 2 {
		t.Fatalf("missing command sessions = %d, want 2", item.Sessions)
	}
	if item.LastAt == nil {
		t.Fatal("missing command last_at is null but the record has a timestamp")
	}
	// Two runs is not a failure rate, so nothing is listed under the floor.
	if len(page.Environment.FailingPrograms) != 0 {
		t.Fatalf("failing_programs = %+v, want none below %d recorded outcomes",
			page.Environment.FailingPrograms, page.Environment.MinKnownOutcomes)
	}
}

func TestOverviewSubagentAndRereadBlocks(t *testing.T) {
	handler := NewServerWithDB(periodFixtureDB(t)).Handler()
	page := periodOverview(t, handler, "from="+periodStart.AddDate(0, 0, -1).Format("2006-01-02"))
	if page.Subagents.SubagentSessions != 1 || page.Subagents.SessionsWithSubagents != 1 {
		t.Fatalf("subagents = %+v", page.Subagents)
	}
	if page.Subagents.AvgPerSession == nil || *page.Subagents.AvgPerSession != 1 {
		t.Fatalf("avg_per_session = %v", page.Subagents.AvgPerSession)
	}
	if len(page.Subagents.ByRole) != 1 || page.Subagents.ByRole[0].Key != "explore" {
		t.Fatalf("by_role = %+v", page.Subagents.ByRole)
	}
	// One session read one file three times; the file read once is not a
	// reread, and neither is the session that read it.
	if page.Reread.Sessions != 1 || page.Reread.Reads != 3 {
		t.Fatalf("reread = %+v", page.Reread)
	}
}

func TestOverviewCompareReportsThePreviousWindowAndItsDelta(t *testing.T) {
	handler := NewServerWithDB(periodFixtureDB(t)).Handler()
	from := periodStart.AddDate(0, 0, -1).Format("2006-01-02")
	to := periodStart.AddDate(0, 0, 1).Format("2006-01-02")
	page := periodOverview(t, handler, "from="+from+"&to="+to+"&compare=1")
	if page.Previous == nil {
		t.Fatal("compare=1 returned no previous window")
	}

	// §16 accuracy gate: the previous window's own total has to equal what the
	// same window returns when it is asked for directly.
	if page.Previous.Range.From == nil || page.Previous.Range.To == nil {
		t.Fatalf("previous.range = %+v", page.Previous.Range)
	}
	direct := periodOverview(t, handler,
		"from="+*page.Previous.Range.From+"&to="+*page.Previous.Range.To)
	if direct.Current.Sessions != page.Previous.Sessions {
		t.Fatalf("previous.sessions = %d, direct query of the same window = %d",
			page.Previous.Sessions, direct.Current.Sessions)
	}
	if direct.Current.ToolCalls != page.Previous.ToolCalls || direct.Current.Friction != page.Previous.Friction {
		t.Fatalf("previous block disagrees with a direct query: %+v vs %+v", *page.Previous, direct.Current)
	}

	sessions := page.Delta["sessions"]
	if sessions == nil {
		t.Fatal("delta.sessions is null but both windows counted sessions")
	}
	if want := int64(page.Current.Sessions - page.Previous.Sessions); sessions.Value != want {
		t.Fatalf("delta.sessions = %d, want %d", sessions.Value, want)
	}
	if sessions.Direction != "up" {
		t.Fatalf("delta.sessions direction = %q, want up", sessions.Direction)
	}
	// Nothing measured tokens in this fixture, so the movement is unknown
	// rather than zero.
	if page.Delta["total_tokens"] != nil {
		t.Fatalf("delta.total_tokens = %+v, want null when neither window measured tokens", page.Delta["total_tokens"])
	}
}

func TestProjectPageCarriesTheSamePeriodBlocks(t *testing.T) {
	handler := NewServerWithDB(periodFixtureDB(t)).Handler()
	var page struct {
		Sessions struct {
			MainNonEmpty int `json:"main_non_empty"`
		} `json:"sessions"`
		Current     periodSummary          `json:"current"`
		Previous    *periodSummary         `json:"previous"`
		Delta       map[string]*deltaValue `json:"delta"`
		Parallelism parallelismSummary     `json:"parallelism"`
		Environment environmentSummary     `json:"environment"`
		Reread      rereadSummary          `json:"reread"`
	}
	from := periodStart.AddDate(0, 0, -1).Format("2006-01-02")
	getJSON(t, handler, "/api/v1/projects/"+urlEscape(periodProject)+"?from="+from+"&compare=1", &page)
	if page.Current.Sessions != page.Sessions.MainNonEmpty {
		t.Fatalf("current.sessions = %d, sessions.main_non_empty = %d", page.Current.Sessions, page.Sessions.MainNonEmpty)
	}
	if page.Parallelism.Peak != 3 {
		t.Fatalf("project parallelism.peak = %d, want 3", page.Parallelism.Peak)
	}
	if page.Reread.Sessions != 1 {
		t.Fatalf("project reread = %+v", page.Reread)
	}
	if page.Previous == nil || page.Delta["sessions"] == nil {
		t.Fatalf("project compare=1 returned previous=%+v delta=%+v", page.Previous, page.Delta)
	}
}

func TestMissingCommandNameReadsTheRecordedLine(t *testing.T) {
	for _, item := range []struct{ line, want string }{
		{"bash: line #: ruff: command not found", "ruff"},
		{"bash: line #: sqlite#: command not found", "sqlite#"},
		{`echo "all checks passed"/bin/bash: line #: go: command not found`, "go"},
		{"zsh: command not found: pnpm", "pnpm"},
		{"ls exit 127", "ls"},
		{`t.fatalf("first import report = %+v", first)`, unparsedCommandKey},
		{"", unparsedCommandKey},
	} {
		if got := missingCommandName(item.line); got != item.want {
			t.Errorf("missingCommandName(%q) = %q, want %q", item.line, got, item.want)
		}
	}
}

func TestTimelinePagesInAStableOrder(t *testing.T) {
	handler := NewServerWithDB(testAPIDB(t)).Handler()
	var first struct {
		Timeline   []map[string]any `json:"timeline"`
		Pagination struct {
			Offset  int  `json:"offset"`
			Limit   int  `json:"limit"`
			Total   int  `json:"total"`
			HasMore bool `json:"has_more"`
		} `json:"pagination"`
	}
	getJSON(t, handler, "/api/v1/timeline?limit=1", &first)
	if first.Pagination.Limit != 1 || first.Pagination.Offset != 0 {
		t.Fatalf("pagination = %+v", first.Pagination)
	}
	if first.Pagination.Total < len(first.Timeline) {
		t.Fatalf("total %d is smaller than the page it returned (%d rows)", first.Pagination.Total, len(first.Timeline))
	}
	if first.Pagination.Total > 1 && !first.Pagination.HasMore {
		t.Fatalf("has_more = false with total %d and limit 1", first.Pagination.Total)
	}

	// Walking the pages one at a time visits every row exactly once, which is
	// what a stable order is for.
	seen := make(map[string]int)
	for offset := 0; offset < first.Pagination.Total; offset++ {
		var page struct {
			Timeline []map[string]any `json:"timeline"`
		}
		getJSON(t, handler, "/api/v1/timeline?limit=1&offset="+strconv.Itoa(offset), &page)
		if len(page.Timeline) != 1 {
			t.Fatalf("offset %d returned %d rows", offset, len(page.Timeline))
		}
		encoded, _ := json.Marshal(page.Timeline[0])
		seen[string(encoded)]++
	}
	for row, count := range seen {
		if count != 1 {
			t.Fatalf("row visited %d times while paging: %s", count, row)
		}
	}
	if len(seen) != first.Pagination.Total {
		t.Fatalf("paging visited %d distinct rows, total says %d", len(seen), first.Pagination.Total)
	}
}

func TestETagCannotRepeatAcrossProcesses(t *testing.T) {
	db := periodFixtureDB(t)
	first := httptest.NewRecorder()
	NewServerWithDB(db).Handler().ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/api/v1/overview?from=all", nil))
	// A second Server is what a restarted daemon is. Its responses must not be
	// interchangeable with the previous process's, even at the same data
	// version, or a browser is told a stale copy is current.
	second := httptest.NewRecorder()
	NewServerWithDB(db).Handler().ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/api/v1/overview?from=all", nil))
	firstTag, secondTag := first.Header().Get("ETag"), second.Header().Get("ETag")
	if firstTag == "" || secondTag == "" {
		t.Fatalf("missing ETag: %q %q", firstTag, secondTag)
	}
	if firstTag == secondTag {
		t.Fatalf("two processes minted the same ETag %q", firstTag)
	}

	// The same process still answers 304 for its own tag; the boot stamp
	// separates processes, it does not defeat revalidation.
	revalidate := httptest.NewRecorder()
	server := NewServerWithDB(db)
	fresh := httptest.NewRecorder()
	server.Handler().ServeHTTP(fresh, httptest.NewRequest(http.MethodGet, "/api/v1/overview?from=all", nil))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/overview?from=all", nil)
	request.Header.Set("If-None-Match", fresh.Header().Get("ETag"))
	server.Handler().ServeHTTP(revalidate, request)
	if revalidate.Code != http.StatusNotModified {
		t.Fatalf("revalidation returned %d, want 304", revalidate.Code)
	}
}

func TestRereadListsTheFilesBehindItsOwnTotals(t *testing.T) {
	handler := NewServerWithDB(periodFixtureDB(t)).Handler()
	page := periodOverview(t, handler, "from="+periodStart.AddDate(0, 0, -1).Format("2006-01-02"))
	if len(page.Reread.TopFiles) != 1 {
		t.Fatalf("top_files = %+v", page.Reread.TopFiles)
	}
	item := page.Reread.TopFiles[0]
	if item.Path != periodProject+"/loop.go" || item.Reads != 3 || item.Sessions != 1 {
		t.Fatalf("top file = %+v", item)
	}
	// The list and the totals are the same set counted two ways.
	reads, sessions := 0, map[string]struct{}{}
	for _, file := range page.Reread.TopFiles {
		reads += file.Reads
		sessions[file.Path] = struct{}{}
	}
	if reads != page.Reread.Reads {
		t.Fatalf("top_files add to %d reads, reread.reads = %d", reads, page.Reread.Reads)
	}
}

func TestPeriodCountsProjectsAndReportsTheirDelta(t *testing.T) {
	handler := NewServerWithDB(periodFixtureDB(t)).Handler()
	from := periodStart.AddDate(0, 0, -1).Format("2006-01-02")
	to := periodStart.AddDate(0, 0, 1).Format("2006-01-02")
	page := periodOverview(t, handler, "from="+from+"&to="+to+"&compare=1")
	if page.Current.Projects != 1 {
		t.Fatalf("current.projects = %d, want the one project of the fixture", page.Current.Projects)
	}
	if page.Delta["projects"] == nil {
		t.Fatal("delta.projects is null but both windows counted projects")
	}
}

// A session whose transcript is still being written is reporting a reading of
// a file that will be longer next time. The row says so, so a page can label
// it instead of presenting a moving number as settled.
func TestSessionSaysWhenItsTranscriptIsStillBeingWritten(t *testing.T) {
	db := periodFixtureDB(t)
	handler := NewServerWithDB(db).Handler()
	live := "codex:period-a"
	stale := "codex:period-b"
	exec(t, db, `INSERT INTO native_files (path, size, mtime_ns, session_id, last_read_at)
		VALUES ('/synthetic/live.jsonl', 1, ?, ?, '2026-08-15T09:00:00Z')`,
		time.Now().Add(-time.Minute).UnixNano(), live)
	exec(t, db, `INSERT INTO native_files (path, size, mtime_ns, session_id, last_read_at)
		VALUES ('/synthetic/stale.jsonl', 1, ?, ?, '2026-08-15T09:00:00Z')`,
		time.Now().Add(-2*time.Hour).UnixNano(), stale)

	for id, want := range map[string]bool{live: true, stale: false} {
		var page struct {
			Session struct {
				InProgress bool `json:"in_progress"`
			} `json:"session"`
		}
		getJSON(t, handler, "/api/v1/sessions/"+id, &page)
		if page.Session.InProgress != want {
			t.Errorf("%s in_progress = %v, want %v", id, page.Session.InProgress, want)
		}
	}
}

// environmentFixtureDB adds to the period fixture the two records the
// environment block reads besides the command_not_found one: a probe that came
// back empty-handed, and a "not found" line no rule can pull a name out of.
func environmentFixtureDB(t *testing.T) *storage.DB {
	t.Helper()
	db := periodFixtureDB(t)
	ctx := context.Background()
	store := eventstore.New(db)
	sessionID, err := store.IngestSession(ctx, adapters.SourceCodex, adapters.SessionMeta{
		SourceSessionID: "period-probe", StartedAt: &periodStart, CWD: periodProject, Title: "period-probe",
	})
	if err != nil {
		t.Fatalf("ingest probe session: %v", err)
	}
	exec(t, db, `UPDATE sessions SET thread_kind = 'main' WHERE id = ?`, sessionID)
	if err := store.RecomputeSessionStats(ctx, sessionID); err != nil {
		t.Fatalf("stats: %v", err)
	}
	// One statement, one name, nonzero exit: the probe says psql is not there.
	exec(t, db, `INSERT INTO session_commands (session_id, event_id, ordinal, tool_name, program, command, exit_code, is_error, expected_exit, occurred_at)
		VALUES (?, 9100, 0, 'Bash', NULL, 'which psql', 1, NULL, 0, ?)`,
		sessionID, periodStart.Format(time.RFC3339))
	// Two names in one probe: a nonzero exit does not say which was missing.
	exec(t, db, `INSERT INTO session_commands (session_id, event_id, ordinal, tool_name, program, command, exit_code, is_error, expected_exit, occurred_at)
		VALUES (?, 9101, 0, 'Bash', NULL, 'which nsys ncu', 1, NULL, 0, ?)`,
		sessionID, periodStart.Format(time.RFC3339))
	// A "not found" line with no command name in it at all.
	events := []canonical.Event{
		frictionTestCallEvent(sessionID, sessionID+"-np-call", periodStart.Add(time.Minute), map[string]any{
			"tool_name": "Bash", "tool_use_id": sessionID + "-np", "tool_input": `{"command":"make all"}`,
		}),
		frictionTestEvent(sessionID, sessionID+"-np-result", periodStart.Add(2*time.Minute), map[string]any{
			"tool_use_id": sessionID + "-np", "tool_output": "command not found", "exit_code": 127,
		}),
	}
	if _, err := store.IngestEvents(ctx, sessionID, events); err != nil {
		t.Fatalf("ingest events: %v", err)
	}
	if _, err := store.IngestFriction(ctx, sessionID, events); err != nil {
		t.Fatalf("ingest friction: %v", err)
	}
	return db
}

func TestEnvironmentReadsWhichProbesAndSinksTheUnparsedBucket(t *testing.T) {
	handler := NewServerWithDB(environmentFixtureDB(t)).Handler()
	page := periodOverview(t, handler, "from="+periodStart.AddDate(0, 0, -1).Format("2006-01-02"))
	commands := page.Environment.MissingCommands
	if len(commands) < 3 {
		t.Fatalf("missing_commands = %+v, want the two named commands and the unparsed bucket", commands)
	}
	byName := make(map[string]missingCommand, len(commands))
	for _, item := range commands {
		byName[item.Command] = item
	}
	if _, ok := byName["psql"]; !ok {
		t.Fatalf("missing_commands has no psql: a single-statement `which psql` that exited nonzero is the evidence, got %+v", commands)
	}
	if _, ok := byName["nsys"]; ok {
		t.Fatalf("missing_commands names nsys: `which nsys ncu` does not say which of the two was missing")
	}
	if _, ok := byName[unparsedCommandKey]; !ok {
		t.Fatalf("missing_commands has no %s bucket: a line with no name must be reported, not dropped", unparsedCommandKey)
	}
	// psql and the unparsed bucket have the same session count, so without the
	// sink rule the bucket would sort ahead of it on the name alone.
	if last := commands[len(commands)-1].Command; last != unparsedCommandKey {
		t.Fatalf("last missing command = %q, want the unparsed bucket to sink below every named command", last)
	}
}

func TestFailingProgramsLeavesOutCommandLinesThatNameNoProgram(t *testing.T) {
	db := periodFixtureDB(t)
	ctx := context.Background()
	var sessionID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM sessions WHERE source_session_id = 'period-a'`).Scan(&sessionID); err != nil {
		t.Fatalf("session id: %v", err)
	}
	// Enough recorded outcomes to clear the floor, all of them failures, and
	// none of them naming a program: a rate over "no program" ranks nothing.
	for index := 0; index < minKnownOutcomes+5; index++ {
		exec(t, db, `INSERT INTO session_commands (session_id, event_id, ordinal, tool_name, program, command, exit_code, is_error, expected_exit, occurred_at)
			VALUES (?, ?, 0, 'Bash', NULL, 'set -euo pipefail', 1, NULL, 0, ?)`,
			sessionID, 9200+index, periodStart.Format(time.RFC3339))
	}
	handler := NewServerWithDB(db).Handler()
	page := periodOverview(t, handler, "from="+periodStart.AddDate(0, 0, -1).Format("2006-01-02"))
	for _, item := range page.Environment.FailingPrograms {
		if item.Program == "" || item.Program == unrecordedKey || item.Program == unparsedCommandKey {
			t.Fatalf("failing_programs contains %q: %+v", item.Program, page.Environment.FailingPrograms)
		}
	}
}

func TestOverviewScopeCarriesItsOwnKey(t *testing.T) {
	handler := NewServerWithDB(periodFixtureDB(t)).Handler()
	read := func(query string) map[string]any {
		var page struct {
			Scope map[string]any `json:"scope"`
		}
		getJSON(t, handler, "/api/v1/overview?"+query, &page)
		return page.Scope
	}
	if key := read("from=all")["key"]; key != scopeMainNonEmpty {
		t.Fatalf("default scope.key = %v, want %q", key, scopeMainNonEmpty)
	}
	if key := read("from=all&include=all")["key"]; key != scopeAll {
		t.Fatalf("include=all scope.key = %v, want %q", key, scopeAll)
	}
}
