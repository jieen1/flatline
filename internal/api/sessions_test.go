package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/canonical"
	"flatline/internal/eventstore"
	"flatline/internal/storage"
)

type fixtureSession struct {
	id       string
	source   adapters.Source
	title    string
	taskText string
	cwd      string
	model    string
	started  time.Time
	ended    time.Time
	messages []string
	tags     []string
	failures int
}

// sessionFixtureDB builds a small synthetic corpus. None of it is copied from
// a real local transcript.
func sessionFixtureDB(t *testing.T) *storage.DB {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := eventstore.New(db)
	base := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	fixtures := []fixtureSession{
		{id: "alpha", source: adapters.SourceClaudeCode, title: "重构登录页", taskText: "重构登录页并验证",
			cwd: "/home/fixture/proj-a", model: "model-one", started: base, ended: base.Add(30 * time.Minute),
			messages: []string{"重构登录页并验证", "已经完成重构"}, tags: []string{"implementation", "workspace-proj-a"}, failures: 2},
		{id: "beta", source: adapters.SourceCodex, title: "梳理迁移脚本", taskText: "梳理数据库迁移脚本",
			cwd: "/home/fixture/proj-b", model: "model-two", started: base.AddDate(0, 0, 1), ended: base.AddDate(0, 0, 1).Add(time.Hour),
			messages: []string{"梳理数据库迁移脚本"}, tags: []string{"analysis", "workspace-proj-b"}},
		{id: "gamma", source: adapters.SourceCodex, title: "", taskText: "",
			cwd: "", model: "model-two", started: base.AddDate(0, 0, 2),
			messages: []string{"没有记录工作目录的会话"}, tags: nil},
	}
	for _, fixture := range fixtures {
		started, ended := fixture.started, fixture.ended
		meta := adapters.SessionMeta{SourceSessionID: fixture.id, StartedAt: &started, Model: fixture.model,
			Title: fixture.title, TaskText: fixture.taskText, CWD: fixture.cwd, HarnessVersion: "fixture"}
		if !fixture.ended.IsZero() {
			meta.EndedAt = &ended
		}
		sessionID, err := store.IngestSession(ctx, fixture.source, meta)
		if err != nil {
			t.Fatalf("ingest session %s: %v", fixture.id, err)
		}
		events := make([]canonical.Event, 0, len(fixture.messages)+fixture.failures)
		at := fixture.started
		for i, text := range fixture.messages {
			occurred := at.Add(time.Duration(i) * time.Minute)
			role := "user"
			if i%2 == 1 {
				role = "assistant"
			}
			events = append(events, canonical.Event{
				SourceEventID: fixture.id + "-m" + string(rune('a'+i)), SessionID: sessionID,
				EventType: canonical.EventTypeTranscriptMessage, ObservationLevel: canonical.LevelUnknown,
				Payload:    map[string]any{"role": role, "text": text},
				Locator:    canonical.Locator{Source: string(fixture.source), SessionID: sessionID, RawRef: "fixture:message"},
				OccurredAt: &occurred,
			})
		}
		for i := 0; i < fixture.failures; i++ {
			occurred := at.Add(time.Duration(10+i) * time.Minute)
			events = append(events, canonical.Event{
				SourceEventID: fixture.id + "-f" + string(rune('a'+i)), SessionID: sessionID,
				EventType: canonical.EventTypeTranscriptResult, ObservationLevel: canonical.LevelInvoked,
				Payload:    map[string]any{"tool_name": "Bash", "is_error": true, "exit_code": 1, "tool_output": "command not found"},
				Locator:    canonical.Locator{Source: string(fixture.source), SessionID: sessionID, RawRef: "fixture:result"},
				OccurredAt: &occurred,
			})
		}
		if _, err := store.IngestEvents(ctx, sessionID, events); err != nil {
			t.Fatalf("ingest events %s: %v", fixture.id, err)
		}
		if _, err := store.IngestFriction(ctx, sessionID, events); err != nil {
			t.Fatalf("ingest friction %s: %v", fixture.id, err)
		}
		if err := store.ReplaceRuleTags(ctx, sessionID, fixture.tags); err != nil {
			t.Fatalf("rule tags %s: %v", fixture.id, err)
		}
	}
	if written, err := store.RecomputeAllSessionStats(ctx); err != nil || written != len(fixtures) {
		t.Fatalf("RecomputeAllSessionStats = %d, %v", written, err)
	}
	return db
}

type sessionListPayload struct {
	Sessions []struct {
		ID            string `json:"id"`
		ProjectKey    string `json:"project_key"`
		ProjectLabel  string `json:"project_label"`
		FrictionCount int    `json:"friction_count"`
		MessageCount  int    `json:"message_count"`
		ToolCallCount int    `json:"tool_call_count"`
		DurationMS    *int64 `json:"duration_ms"`
		Pinned        bool   `json:"pinned"`
		MatchCount    *int   `json:"match_count"`
		MatchSnippet  *string
		Tags          []struct {
			Tag  string `json:"tag"`
			Kind string `json:"kind"`
		} `json:"tags"`
	} `json:"sessions"`
	Pagination struct {
		Offset  int  `json:"offset"`
		Limit   int  `json:"limit"`
		Total   int  `json:"total"`
		HasMore bool `json:"has_more"`
	} `json:"pagination"`
}

func TestSessionListFiltersSortsAndPages(t *testing.T) {
	handler := NewServerWithDB(sessionFixtureDB(t)).Handler()
	cases := []struct {
		name  string
		path  string
		want  []string
		total int
	}{
		{"default recent order", "/api/v1/sessions", []string{"codex:gamma", "codex:beta", "claude_code:alpha"}, 3},
		{"oldest first", "/api/v1/sessions?sort=oldest", []string{"claude_code:alpha", "codex:beta", "codex:gamma"}, 3},
		{"by project", "/api/v1/sessions?project=/home/fixture/proj-a", []string{"claude_code:alpha"}, 1},
		{"unrecorded project", "/api/v1/sessions?project=__unrecorded__", []string{"codex:gamma"}, 1},
		{"by harness", "/api/v1/sessions?harness=codex", []string{"codex:gamma", "codex:beta"}, 2},
		{"by tag", "/api/v1/sessions?tag=analysis", []string{"codex:beta"}, 1},
		{"only with friction", "/api/v1/sessions?has_friction=1", []string{"claude_code:alpha"}, 1},
		{"by model", "/api/v1/sessions?model=model-one", []string{"claude_code:alpha"}, 1},
		{"date window", "/api/v1/sessions?from=2026-08-11&to=2026-08-11", []string{"codex:beta"}, 1},
		{"title search", "/api/v1/sessions?q=登录页", []string{"claude_code:alpha"}, 1},
		{"body only search stays empty without deep", "/api/v1/sessions?q=已经完成", nil, 0},
		{"deep body search", "/api/v1/sessions?q=已经完成&deep=1", []string{"claude_code:alpha"}, 1},
		{"sort by friction", "/api/v1/sessions?sort=friction&limit=1", []string{"claude_code:alpha"}, 3},
		{"second page", "/api/v1/sessions?limit=1&offset=1", []string{"codex:beta"}, 3},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var payload sessionListPayload
			getJSON(t, handler, testCase.path, &payload)
			ids := make([]string, 0, len(payload.Sessions))
			for _, session := range payload.Sessions {
				ids = append(ids, session.ID)
			}
			if strings.Join(ids, ",") != strings.Join(testCase.want, ",") {
				t.Fatalf("ids = %v, want %v", ids, testCase.want)
			}
			if payload.Pagination.Total != testCase.total {
				t.Fatalf("total = %d, want %d", payload.Pagination.Total, testCase.total)
			}
		})
	}
}

func TestSessionListCarriesProjectStatsAndTags(t *testing.T) {
	handler := NewServerWithDB(sessionFixtureDB(t)).Handler()
	var payload sessionListPayload
	getJSON(t, handler, "/api/v1/sessions?sort=oldest", &payload)
	alpha := payload.Sessions[0]
	if alpha.ProjectKey != "/home/fixture/proj-a" || alpha.ProjectLabel != "proj-a" {
		t.Fatalf("project = %q / %q", alpha.ProjectKey, alpha.ProjectLabel)
	}
	if alpha.FrictionCount != 2 || alpha.MessageCount != 2 {
		t.Fatalf("stats = friction %d, messages %d", alpha.FrictionCount, alpha.MessageCount)
	}
	if alpha.DurationMS == nil || *alpha.DurationMS != 30*60*1000 {
		t.Fatalf("duration = %v, want 1800000", alpha.DurationMS)
	}
	kinds := map[string]string{}
	for _, tag := range alpha.Tags {
		kinds[tag.Tag] = tag.Kind
	}
	if kinds["implementation"] != "task" || kinds["workspace-proj-a"] != "workspace" {
		t.Fatalf("tags = %+v", alpha.Tags)
	}
	unrecorded := payload.Sessions[2]
	if unrecorded.ProjectKey != unrecordedKey || unrecorded.ProjectLabel != "项目未记录" {
		t.Fatalf("unrecorded project = %q / %q", unrecorded.ProjectKey, unrecorded.ProjectLabel)
	}
	if unrecorded.DurationMS != nil {
		t.Fatalf("duration for a session with no end time = %v, want null", *unrecorded.DurationMS)
	}
}

func TestSessionFacetsCountUnderTheOtherFilters(t *testing.T) {
	handler := NewServerWithDB(sessionFixtureDB(t)).Handler()
	var payload struct {
		Total    int `json:"total"`
		Projects []struct {
			Key   string `json:"key"`
			Label string `json:"label"`
			Count int    `json:"count"`
		} `json:"projects"`
		Harnesses []struct {
			Key   string `json:"key"`
			Count int    `json:"count"`
		} `json:"harnesses"`
		Tags []struct {
			Tag   string `json:"tag"`
			Kind  string `json:"kind"`
			Count int    `json:"count"`
		} `json:"tags"`
		Friction struct {
			With    int `json:"with"`
			Without int `json:"without"`
		} `json:"friction"`
		Pinned        int `json:"pinned"`
		DateHistogram []struct {
			Day   string `json:"day"`
			Count int    `json:"count"`
		} `json:"date_histogram"`
	}
	getJSON(t, handler, "/api/v1/sessions/facets", &payload)
	if payload.Total != 3 || len(payload.Projects) != 3 || len(payload.DateHistogram) != 3 {
		t.Fatalf("unfiltered facets = %+v", payload)
	}
	if payload.Friction.With != 1 || payload.Friction.Without != 2 {
		t.Fatalf("friction facet = %+v", payload.Friction)
	}

	// The harness facet must still count both harnesses while a harness filter
	// is applied; every other filter still narrows it.
	getJSON(t, handler, "/api/v1/sessions/facets?harness=codex", &payload)
	if payload.Total != 2 {
		t.Fatalf("filtered total = %d, want 2", payload.Total)
	}
	byHarness := map[string]int{}
	for _, item := range payload.Harnesses {
		byHarness[item.Key] = item.Count
	}
	if byHarness["codex"] != 2 || byHarness["claude_code"] != 1 {
		t.Fatalf("harness facet under its own filter = %+v", byHarness)
	}
	for _, item := range payload.Projects {
		if item.Key == "/home/fixture/proj-a" {
			t.Fatalf("project facet still counts a filtered-out harness: %+v", payload.Projects)
		}
	}
}

func TestSessionAnnotationWritesOnlyLocalRecord(t *testing.T) {
	db := sessionFixtureDB(t)
	server := NewServerWithDB(db)
	handler := server.Handler()
	before := server.dataVersion()

	body := strings.NewReader(`{"pinned":true,"note":"这次会话要回看","tags":["回看","重点"]}`)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/sessions/claude_code:alpha/annotation", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("annotation write = %d, body=%s", rec.Code, rec.Body.String())
	}
	var written struct {
		Annotation struct {
			Pinned    bool    `json:"pinned"`
			Note      *string `json:"note"`
			UpdatedAt *string `json:"updated_at"`
		} `json:"annotation"`
		Tags []struct {
			Tag  string `json:"tag"`
			Kind string `json:"kind"`
		} `json:"tags"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &written); err != nil {
		t.Fatalf("decode annotation: %v", err)
	}
	if !written.Annotation.Pinned || written.Annotation.Note == nil || *written.Annotation.Note != "这次会话要回看" {
		t.Fatalf("annotation = %+v", written.Annotation)
	}
	userTags := 0
	for _, tag := range written.Tags {
		if tag.Kind == "user" {
			userTags++
		}
	}
	if userTags != 2 {
		t.Fatalf("user tags = %+v", written.Tags)
	}
	if server.dataVersion() <= before {
		t.Fatalf("data version did not advance after a user write: %d -> %d", before, server.dataVersion())
	}

	// A partial update leaves the field it does not mention alone.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/sessions/claude_code:alpha/annotation", strings.NewReader(`{"pinned":false}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("second annotation write = %d, body=%s", rec.Code, rec.Body.String())
	}
	var detail struct {
		Session struct {
			Pinned     bool `json:"pinned"`
			Annotation struct {
				Pinned bool    `json:"pinned"`
				Note   *string `json:"note"`
			} `json:"annotation"`
			Tags []struct {
				Tag  string `json:"tag"`
				Kind string `json:"kind"`
			} `json:"tags"`
		} `json:"session"`
	}
	getJSON(t, handler, "/api/v1/sessions/claude_code:alpha", &detail)
	if detail.Session.Pinned || detail.Session.Annotation.Note == nil || *detail.Session.Annotation.Note != "这次会话要回看" {
		t.Fatalf("session after partial update = %+v", detail.Session)
	}
	if len(detail.Session.Tags) != 4 {
		t.Fatalf("detail tags = %+v, want two rule tags and two user tags", detail.Session.Tags)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/sessions/claude_code:missing/annotation", strings.NewReader(`{"pinned":true}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("annotation on an unknown session = %d", rec.Code)
	}
}

func TestSessionDetailKeepsExistingShapeAndAddsStats(t *testing.T) {
	handler := NewServerWithDB(sessionFixtureDB(t)).Handler()
	var detail struct {
		Session struct {
			ID            string `json:"id"`
			Title         string `json:"title"`
			EventCount    int    `json:"event_count"`
			FrictionCount int    `json:"friction_count"`
		} `json:"session"`
		Events   []map[string]any `json:"events"`
		Friction struct {
			Count   int              `json:"count"`
			Records []map[string]any `json:"records"`
		} `json:"friction"`
	}
	getJSON(t, handler, "/api/v1/sessions/claude_code:alpha", &detail)
	if detail.Session.ID != "claude_code:alpha" || detail.Session.Title != "重构登录页" {
		t.Fatalf("session = %+v", detail.Session)
	}
	if detail.Session.EventCount != 4 || detail.Session.FrictionCount != 2 {
		t.Fatalf("session stats = %+v", detail.Session)
	}
	if len(detail.Events) != 4 || detail.Friction.Count != 2 || len(detail.Friction.Records) != 2 {
		t.Fatalf("events=%d friction=%+v", len(detail.Events), detail.Friction)
	}
}

func TestProjectsAggregateSessionsAndFriction(t *testing.T) {
	handler := NewServerWithDB(sessionFixtureDB(t)).Handler()
	var payload struct {
		Projects []projectResponse `json:"projects"`
	}
	getJSON(t, handler, "/api/v1/projects", &payload)
	if len(payload.Projects) != 3 {
		t.Fatalf("projects = %+v", payload.Projects)
	}
	byKey := map[string]projectResponse{}
	for _, project := range payload.Projects {
		byKey[project.Key] = project
	}
	alpha := byKey["/home/fixture/proj-a"]
	if alpha.Label != "proj-a" || alpha.Sessions != 1 || alpha.FrictionCount != 2 || alpha.Harnesses["claude_code"] != 1 {
		t.Fatalf("proj-a = %+v", alpha)
	}
	if alpha.FirstStartedAt == nil || alpha.LastStartedAt == nil {
		t.Fatalf("proj-a活动时间 = %+v", alpha)
	}
	if byKey[unrecordedKey].Label != "项目未记录" {
		t.Fatalf("unrecorded project = %+v", byKey[unrecordedKey])
	}
	// Most recent activity first.
	if payload.Projects[0].Key != unrecordedKey {
		t.Fatalf("project order = %+v", payload.Projects)
	}
}

func TestOverviewReportsRangeCountsCategoriesAndTools(t *testing.T) {
	handler := NewServerWithDB(sessionFixtureDB(t)).Handler()
	var payload struct {
		Sessions struct {
			InRange   int            `json:"in_range"`
			Total     int            `json:"total"`
			ByHarness map[string]int `json:"by_harness"`
		} `json:"sessions"`
		Projects struct {
			InRange int `json:"in_range"`
			Total   int `json:"total"`
		} `json:"projects"`
		Events   int `json:"events"`
		Messages int `json:"messages"`
		Duration struct {
			KnownSessions int   `json:"known_sessions"`
			TotalMS       int64 `json:"total_ms"`
		} `json:"duration"`
		Friction struct {
			Total                int `json:"total"`
			ToolError            int `json:"tool_error"`
			NonzeroExit          int `json:"nonzero_exit"`
			UserInterrupt        int `json:"user_interrupt"`
			SessionsWithFriction int `json:"sessions_with_friction"`
		} `json:"friction"`
		ActivityByDay         map[string]activityDay  `json:"activity_by_day"`
		TopProjects           []projectResponse       `json:"top_projects"`
		TopFrictionTools      []frictionToolCount     `json:"top_friction_tools"`
		TopFrictionCategories []frictionCategoryCount `json:"top_friction_categories"`
		TopTags               []tagFacet              `json:"top_tags"`
		RecentSessions        []struct {
			ID string `json:"id"`
		} `json:"recent_sessions"`
		LastEventAt *string `json:"last_event_at"`
	}
	getJSON(t, handler, "/api/v1/overview?from=all", &payload)
	if payload.Sessions.InRange != 3 || payload.Sessions.Total != 3 || payload.Sessions.ByHarness["codex"] != 2 {
		t.Fatalf("sessions = %+v", payload.Sessions)
	}
	if payload.Projects.InRange != 3 || payload.Projects.Total != 3 {
		t.Fatalf("projects = %+v", payload.Projects)
	}
	if payload.Messages != 4 || payload.Events != 6 {
		t.Fatalf("events = %d messages = %d", payload.Events, payload.Messages)
	}
	if payload.Duration.KnownSessions != 2 || payload.Duration.TotalMS != int64(90*60*1000) {
		t.Fatalf("duration = %+v", payload.Duration)
	}
	if payload.Friction.Total != 2 || payload.Friction.ToolError != 2 || payload.Friction.NonzeroExit != 2 || payload.Friction.SessionsWithFriction != 1 {
		t.Fatalf("friction = %+v", payload.Friction)
	}
	if payload.Friction.UserInterrupt != 0 {
		t.Fatalf("user interrupts = %d, want none recorded in this fixture", payload.Friction.UserInterrupt)
	}
	if len(payload.ActivityByDay) != 3 || payload.ActivityByDay["2026-08-10"].Sessions != 1 {
		t.Fatalf("activity = %+v", payload.ActivityByDay)
	}
	if len(payload.TopFrictionTools) != 1 || payload.TopFrictionTools[0].ToolName != "Bash" || payload.TopFrictionTools[0].Count != 2 {
		t.Fatalf("friction tools = %+v", payload.TopFrictionTools)
	}
	if payload.TopFrictionCategories == nil {
		t.Fatal("top_friction_categories must be an empty array, never null")
	}
	if len(payload.TopFrictionCategories) != 1 || payload.TopFrictionCategories[0].Count != 2 {
		t.Fatalf("friction categories = %+v, want one category covering both source events", payload.TopFrictionCategories)
	}
	if len(payload.TopTags) == 0 || len(payload.TopProjects) != 3 || len(payload.RecentSessions) != 3 || payload.LastEventAt == nil {
		t.Fatalf("overview tail = tags %d projects %d recent %d last %v", len(payload.TopTags), len(payload.TopProjects), len(payload.RecentSessions), payload.LastEventAt)
	}

	// A narrow window must exclude what falls outside it while total stays whole.
	getJSON(t, handler, "/api/v1/overview?from=2026-08-12&to=2026-08-12", &payload)
	if payload.Sessions.InRange != 1 || payload.Sessions.Total != 3 {
		t.Fatalf("narrow window sessions = %+v", payload.Sessions)
	}
}

func TestCachedResponsesRevalidateAndInvalidateOnWrite(t *testing.T) {
	db := sessionFixtureDB(t)
	server := NewServerWithDB(db)
	handler := server.Handler()

	first := getJSON(t, handler, "/api/v1/overview?from=all", nil)
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on a cacheable response")
	}

	second := getJSON(t, handler, "/api/v1/overview?from=all", nil)
	if second.Body.String() != first.Body.String() {
		t.Fatal("cache hit returned a different body")
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/overview?from=all", nil)
	request.Header.Set("If-None-Match", etag)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request)
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Fatalf("revalidation = %d, body=%q", rec.Code, rec.Body.String())
	}

	// A user write publishes a new data version, so the same ETag must stop
	// matching and the cached body must be dropped.
	write := httptest.NewRecorder()
	handler.ServeHTTP(write, httptest.NewRequest(http.MethodPut, "/api/v1/sessions/claude_code:alpha/annotation", strings.NewReader(`{"pinned":true}`)))
	if write.Code != http.StatusOK {
		t.Fatalf("annotation write = %d", write.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/overview?from=all", nil)
	request.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, request)
	if rec.Code != http.StatusOK {
		t.Fatalf("stale revalidation = %d, want a fresh 200", rec.Code)
	}
	if rec.Header().Get("ETag") == etag {
		t.Fatal("ETag did not change after the data version advanced")
	}
}

func TestIngestStatusReportsImportProgress(t *testing.T) {
	handler := NewServerWithDB(sessionFixtureDB(t)).Handler()
	var payload struct {
		Status      string `json:"status"`
		DataVersion int64  `json:"data_version"`
		Import      struct {
			Phase        string  `json:"phase"`
			FilesSeen    int     `json:"files_seen"`
			FilesSkipped int     `json:"files_skipped"`
			LastError    *string `json:"last_error"`
		} `json:"import"`
		Sessions int `json:"sessions"`
		Events   int `json:"events"`
	}
	rec := getJSON(t, handler, "/api/v1/ingest/status", &payload)
	if rec.Header().Get("ETag") != "" {
		t.Fatal("ingest status must not be revalidated; it changes without a data version bump")
	}
	if payload.Status != "ready" || payload.Import.Phase != "idle" || payload.Import.LastError != nil {
		t.Fatalf("status = %+v", payload)
	}
	if payload.Sessions != 3 || payload.Events != 6 {
		t.Fatalf("counts = sessions %d events %d", payload.Sessions, payload.Events)
	}
}

// hierarchyFixtureDB extends the base corpus with the thread and projection
// facts the second-phase list needs: one subagent thread, one empty session,
// and recorded commands and file touches. It is fabricated, like the base.
func hierarchyFixtureDB(t *testing.T) *storage.DB {
	t.Helper()
	ctx := context.Background()
	db := sessionFixtureDB(t)
	store := eventstore.New(db)
	base := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)

	alphaCommands := []canonical.Event{
		toolCallEvent("claude_code:alpha", "alpha-c1", "call-a1", "Bash",
			`{"command":"cd /home/fixture/proj-a && sudo /usr/bin/git status"}`, base.Add(3*time.Minute)),
		toolResultEvent("claude_code:alpha", "alpha-r1", "call-a1", "fatal: not a git repository\nExit code: 128", base.Add(4*time.Minute)),
		toolCallEvent("claude_code:alpha", "alpha-c2", "call-a2", "Edit",
			`{"file_path":"internal/login.go"}`, base.Add(5*time.Minute)),
	}
	if _, err := store.IngestEvents(ctx, "claude_code:alpha", alphaCommands); err != nil {
		t.Fatalf("ingest alpha commands: %v", err)
	}

	started := base.AddDate(0, 0, 1)
	deltaID, err := store.IngestSession(ctx, adapters.SourceCodex, adapters.SessionMeta{
		SourceSessionID: "delta", StartedAt: &started, Title: "探查迁移脚本", CWD: "/home/fixture/proj-b",
		Model: "model-two", HarnessVersion: "fixture", ThreadKind: "subagent", ParentSessionID: "codex:beta",
		AgentRole: "explore", AgentNickname: "Ptolemy", Originator: "codex-tui"})
	if err != nil {
		t.Fatalf("ingest delta: %v", err)
	}
	deltaEvents := []canonical.Event{
		messageEvent(deltaID, "delta-m1", "user", "子线程：列出迁移脚本", started),
		toolCallEvent(deltaID, "delta-c1", "call-d1", "exec_command",
			`{"cmd":"ls migrations","workdir":"/home/fixture/proj-b"}`, started.Add(time.Minute)),
		toolResultEvent(deltaID, "delta-r1", "call-d1", "Chunk ID: aa\nProcess exited with code 0\nOutput:\n001.sql", started.Add(2*time.Minute)),
	}
	if _, err := store.IngestEvents(ctx, deltaID, deltaEvents); err != nil {
		t.Fatalf("ingest delta events: %v", err)
	}

	emptyStarted := base.AddDate(0, 0, 3)
	if _, err := store.IngestSession(ctx, adapters.SourceCodex, adapters.SessionMeta{
		SourceSessionID: "epsilon", StartedAt: &emptyStarted, CWD: "/home/fixture/proj-b",
		HarnessVersion: "fixture", ThreadKind: "main", Originator: "codex_exec"}); err != nil {
		t.Fatalf("ingest epsilon: %v", err)
	}

	for id, thread := range map[string]adapters.SessionMeta{
		"alpha": {ThreadKind: "main", Originator: "claude_code"},
		"beta":  {ThreadKind: "main", Originator: "codex-tui"},
	} {
		source := adapters.SourceClaudeCode
		if id == "beta" {
			source = adapters.SourceCodex
		}
		if _, err := db.ExecContext(ctx, `UPDATE sessions SET thread_kind = ?, originator = ? WHERE id = ?`,
			thread.ThreadKind, thread.Originator, string(source)+":"+id); err != nil {
			t.Fatalf("set thread facts %s: %v", id, err)
		}
	}
	if _, err := store.RecomputeAllSessionStats(ctx); err != nil {
		t.Fatalf("recompute stats: %v", err)
	}
	if _, err := store.RecomputeAllProjections(ctx); err != nil {
		t.Fatalf("recompute projections: %v", err)
	}
	return db
}

func messageEvent(sessionID, ref, role, text string, at time.Time) canonical.Event {
	return canonical.Event{SourceEventID: ref, SessionID: sessionID,
		EventType: canonical.EventTypeTranscriptMessage, ObservationLevel: canonical.LevelUnknown,
		Payload: map[string]any{"role": role, "text": text},
		Locator: canonical.Locator{Source: "fixture", SessionID: sessionID, RawRef: ref}, OccurredAt: &at}
}

func toolCallEvent(sessionID, ref, callID, tool, input string, at time.Time) canonical.Event {
	return canonical.Event{SourceEventID: ref, SessionID: sessionID,
		EventType: canonical.EventTypeTranscriptToolCall, ObservationLevel: canonical.LevelInvoked,
		Payload: map[string]any{"role": "assistant", "tool_name": tool, "tool_use_id": callID, "tool_input": input},
		Locator: canonical.Locator{Source: "fixture", SessionID: sessionID, RawRef: ref}, OccurredAt: &at}
}

func toolResultEvent(sessionID, ref, callID, output string, at time.Time) canonical.Event {
	return canonical.Event{SourceEventID: ref, SessionID: sessionID,
		EventType: canonical.EventTypeTranscriptResult, ObservationLevel: canonical.LevelInvoked,
		Payload: map[string]any{"role": "tool", "tool_use_id": callID, "tool_output": output},
		Locator: canonical.Locator{Source: "fixture", SessionID: sessionID, RawRef: ref}, OccurredAt: &at}
}

type threadListPayload struct {
	Sessions []struct {
		ID              string  `json:"id"`
		ThreadKind      *string `json:"thread_kind"`
		ParentSessionID *string `json:"parent_session_id"`
		AgentRole       *string `json:"agent_role"`
		AgentNickname   *string `json:"agent_nickname"`
		Originator      *string `json:"originator"`
		SubagentCount   int     `json:"subagent_count"`
		CommandCount    int     `json:"command_count"`
		FailedCommands  int     `json:"failed_command_count"`
		FileCount       int     `json:"file_count"`
		IsEmpty         bool    `json:"is_empty"`
	} `json:"sessions"`
	Pagination struct {
		Total int `json:"total"`
	} `json:"pagination"`
}

func TestSessionListDefaultsToMainNonEmptyThreads(t *testing.T) {
	handler := NewServerWithDB(hierarchyFixtureDB(t)).Handler()
	cases := []struct {
		name string
		path string
		want []string
	}{
		{"default hides subagents and empty sessions", "/api/v1/sessions?sort=oldest",
			[]string{"claude_code:alpha", "codex:beta", "codex:gamma"}},
		{"subagent threads only", "/api/v1/sessions?thread=subagent", []string{"codex:delta"}},
		{"every thread", "/api/v1/sessions?thread=all&empty=all&sort=oldest",
			[]string{"claude_code:alpha", "codex:beta", "codex:delta", "codex:gamma", "codex:epsilon"}},
		{"empty sessions only", "/api/v1/sessions?empty=1&thread=all", []string{"codex:epsilon"}},
		{"children of one session", "/api/v1/sessions?parent=codex:beta", []string{"codex:delta"}},
		{"by role", "/api/v1/sessions?role=explore&thread=all", []string{"codex:delta"}},
		{"by program", "/api/v1/sessions?program=git", []string{"claude_code:alpha"}},
		{"by program across threads", "/api/v1/sessions?program=ls&thread=all", []string{"codex:delta"}},
		{"by file substring", "/api/v1/sessions?file=login.go", []string{"claude_code:alpha"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var payload threadListPayload
			getJSON(t, handler, testCase.path, &payload)
			ids := make([]string, 0, len(payload.Sessions))
			for _, session := range payload.Sessions {
				ids = append(ids, session.ID)
			}
			if strings.Join(ids, ",") != strings.Join(testCase.want, ",") {
				t.Fatalf("ids = %v, want %v", ids, testCase.want)
			}
		})
	}
}

func TestSessionListCarriesThreadAndProjectionCounts(t *testing.T) {
	handler := NewServerWithDB(hierarchyFixtureDB(t)).Handler()
	var payload threadListPayload
	getJSON(t, handler, "/api/v1/sessions?thread=all&empty=all&sort=oldest", &payload)
	byID := map[string]int{}
	for i, session := range payload.Sessions {
		byID[session.ID] = i
	}
	alpha := payload.Sessions[byID["claude_code:alpha"]]
	if alpha.ThreadKind == nil || *alpha.ThreadKind != "main" || alpha.Originator == nil || *alpha.Originator != "claude_code" {
		t.Fatalf("alpha thread = %+v", alpha)
	}
	if alpha.CommandCount != 1 || alpha.FailedCommands != 1 || alpha.FileCount != 1 || alpha.IsEmpty {
		t.Fatalf("alpha projections = %+v", alpha)
	}
	beta := payload.Sessions[byID["codex:beta"]]
	if beta.SubagentCount != 1 {
		t.Fatalf("beta subagent_count = %d, want 1", beta.SubagentCount)
	}
	delta := payload.Sessions[byID["codex:delta"]]
	if delta.ParentSessionID == nil || *delta.ParentSessionID != "codex:beta" ||
		delta.AgentRole == nil || *delta.AgentRole != "explore" ||
		delta.AgentNickname == nil || *delta.AgentNickname != "Ptolemy" {
		t.Fatalf("delta thread = %+v", delta)
	}
	if delta.CommandCount != 1 || delta.FailedCommands != 0 {
		t.Fatalf("delta commands = %+v", delta)
	}
	epsilon := payload.Sessions[byID["codex:epsilon"]]
	if !epsilon.IsEmpty {
		t.Fatalf("a session with no message and no tool call is not marked empty: %+v", epsilon)
	}
}

func TestSessionFacetsCoverThreadsEmptinessRolesAndPrograms(t *testing.T) {
	handler := NewServerWithDB(hierarchyFixtureDB(t)).Handler()
	var payload struct {
		Total   int `json:"total"`
		Threads []struct {
			Key   string `json:"key"`
			Count int    `json:"count"`
		} `json:"threads"`
		Empty struct {
			Yes int `json:"yes"`
			No  int `json:"no"`
		} `json:"empty"`
		Roles []struct {
			Key   string `json:"key"`
			Count int    `json:"count"`
		} `json:"roles"`
		Programs []struct {
			Key   string `json:"key"`
			Count int    `json:"count"`
		} `json:"programs"`
	}
	getJSON(t, handler, "/api/v1/sessions/facets", &payload)
	if payload.Total != 3 {
		t.Fatalf("facet total under the defaults = %d, want 3", payload.Total)
	}
	threads := map[string]int{}
	for _, item := range payload.Threads {
		threads[item.Key] = item.Count
	}
	// The thread facet excludes its own filter, so every kind is still counted,
	// and a session whose thread kind was never recorded stays visible as
	// unrecorded rather than being counted as a main thread.
	if threads["main"] != 2 || threads["subagent"] != 1 || threads[unrecordedKey] != 1 {
		t.Fatalf("thread facet = %+v", threads)
	}
	if payload.Empty.Yes != 1 || payload.Empty.No != 3 {
		t.Fatalf("empty facet = %+v", payload.Empty)
	}
	programs := map[string]int{}
	for _, item := range payload.Programs {
		programs[item.Key] = item.Count
	}
	if programs["git"] != 1 {
		t.Fatalf("program facet under the defaults = %+v", programs)
	}

	getJSON(t, handler, "/api/v1/sessions/facets?thread=all&empty=all", &payload)
	roles := map[string]int{}
	for _, item := range payload.Roles {
		roles[item.Key] = item.Count
	}
	if roles["explore"] != 1 {
		t.Fatalf("role facet = %+v", roles)
	}
}

func TestSessionDetailCarriesThreadCommandsAndFiles(t *testing.T) {
	handler := NewServerWithDB(hierarchyFixtureDB(t)).Handler()
	var detail struct {
		Parent *struct {
			ID           string `json:"id"`
			ProjectLabel string `json:"project_label"`
		} `json:"parent"`
		Children []struct {
			ID string `json:"id"`
		} `json:"children"`
		Commands []struct {
			Program  *string `json:"program"`
			Command  string  `json:"command"`
			ExitCode *int    `json:"exit_code"`
		} `json:"commands"`
		CommandsTotal int `json:"commands_total"`
		Files         []struct {
			Path  string `json:"path"`
			Edits int    `json:"edits"`
		} `json:"files"`
		FilesTotal int `json:"files_total"`
	}

	getJSON(t, handler, "/api/v1/sessions/claude_code:alpha", &detail)
	if detail.Parent != nil {
		t.Fatalf("a main thread reported a parent: %+v", detail.Parent)
	}
	if detail.CommandsTotal != 1 || len(detail.Commands) != 1 {
		t.Fatalf("commands = %+v (total %d)", detail.Commands, detail.CommandsTotal)
	}
	command := detail.Commands[0]
	if command.Program == nil || *command.Program != "git" || command.ExitCode == nil || *command.ExitCode != 128 {
		t.Fatalf("command = %+v", command)
	}
	if detail.FilesTotal != 1 || len(detail.Files) != 1 || detail.Files[0].Path != "/home/fixture/proj-a/internal/login.go" || detail.Files[0].Edits != 1 {
		t.Fatalf("files = %+v (total %d)", detail.Files, detail.FilesTotal)
	}

	getJSON(t, handler, "/api/v1/sessions/codex:beta", &detail)
	if len(detail.Children) != 1 || detail.Children[0].ID != "codex:delta" {
		t.Fatalf("children = %+v", detail.Children)
	}

	getJSON(t, handler, "/api/v1/sessions/codex:delta", &detail)
	if detail.Parent == nil || detail.Parent.ID != "codex:beta" || detail.Parent.ProjectLabel != "proj-b" {
		t.Fatalf("parent = %+v", detail.Parent)
	}
}
