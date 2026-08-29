package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/canonical"
	"flatline/internal/eventstore"
)

func TestSessionDetailExposesSessionWideFrictionProjection(t *testing.T) {
	db := testAPIDB(t)
	ctx := context.Background()
	store := eventstore.New(db)
	sessionID := "claude_code:api-fixture"
	failureAt := time.Date(2026, 7, 1, 12, 0, 2, 0, time.UTC)
	event := canonical.Event{
		SourceEventID: "api-fixture-tool-error", SessionID: sessionID, EventType: canonical.EventTypeTranscriptResult,
		ObservationLevel: canonical.LevelUnknown, Payload: map[string]any{"role": "tool", "tool_output": "permission denied", "is_error": true},
		Locator: canonical.Locator{Source: string(adapters.SourceClaudeCode), SessionID: sessionID, RawRef: "fixture:tool-result"}, OccurredAt: &failureAt,
	}
	if _, err := store.IngestEvents(ctx, sessionID, []canonical.Event{event}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.IngestFriction(ctx, sessionID, []canonical.Event{event}); err != nil {
		t.Fatal(err)
	}
	handler := NewServerWithDB(db).Handler()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/claude_code:api-fixture?events=page&limit=1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("session detail status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var response map[string]json.RawMessage
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	var friction map[string]json.RawMessage
	if err := json.Unmarshal(response["friction"], &friction); err != nil {
		t.Fatal(err)
	}
	var count int
	var complete bool
	var records []map[string]json.RawMessage
	var events []json.RawMessage
	if err := json.Unmarshal(friction["count"], &count); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(friction["complete"], &complete); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(friction["records"], &records); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(response["events"], &events); err != nil {
		t.Fatal(err)
	}
	var sourceEventID string
	var isError bool
	if len(records) == 1 {
		_ = json.Unmarshal(records[0]["source_event_id"], &sourceEventID)
		_ = json.Unmarshal(records[0]["is_error"], &isError)
	}
	if count != 1 || !complete || len(records) != 1 || sourceEventID != event.SourceEventID || !isError || len(events) != 1 {
		t.Fatalf("friction count=%d complete=%v records=%v events=%d", count, complete, records, len(events))
	}
}

func TestFrictionAPIAggregatesByProjectAndHarnessAndDrillsIntoEvents(t *testing.T) {
	db := testAPIDB(t)
	ctx := context.Background()
	store := eventstore.New(db)
	projectRoot := "/synthetic/project-a"
	first := time.Date(2026, 7, 2, 9, 0, 0, 0, time.UTC)

	claudeID, err := store.IngestSession(ctx, adapters.SourceClaudeCode, adapters.SessionMeta{
		SourceSessionID: "friction-claude",
		StartedAt:       &first,
		CWD:             projectRoot,
		Title:           "Claude 工具失败会话",
		TaskText:        "读取并修改项目文件",
	})
	if err != nil {
		t.Fatal(err)
	}
	codexAt := first.Add(10 * time.Minute)
	codexID, err := store.IngestSession(ctx, adapters.SourceCodex, adapters.SessionMeta{
		SourceSessionID: "friction-codex",
		StartedAt:       &codexAt,
		CWD:             projectRoot,
		Title:           "Codex 非零退出会话",
	})
	if err != nil {
		t.Fatal(err)
	}

	claudeEvents := []canonical.Event{
		frictionTestEvent(claudeID, "claude-error", first.Add(1*time.Second), map[string]any{
			"tool_name": "Read", "tool_output": "permission denied", "is_error": true,
		}),
		frictionTestEvent(claudeID, "claude-exit", first.Add(2*time.Second), map[string]any{
			"tool_name": "Bash", "tool_output": "command failed", "exit_code": 7,
		}),
		frictionTestEvent(claudeID, "claude-overlap", first.Add(3*time.Second), map[string]any{
			"tool_name": "Write", "tool_output": "failed", "is_error": true, "exit_code": 1,
		}),
		{
			SourceEventID: "claude-bypass", SessionID: claudeID, EventType: canonical.EventTypeAssetViolation,
			ObservationLevel: canonical.LevelUnknown, Payload: map[string]any{"asset_id": "skill:project:fixture", "violated": true},
			Locator: canonical.Locator{Source: string(adapters.SourceClaudeCode), SessionID: claudeID, RawRef: "fixture:asset-violation"}, OccurredAt: timePtr(first.Add(4 * time.Second)),
		},
	}
	if _, err := store.IngestEvents(ctx, claudeID, claudeEvents); err != nil {
		t.Fatal(err)
	}
	if _, err := store.IngestFriction(ctx, claudeID, claudeEvents); err != nil {
		t.Fatal(err)
	}

	codexEvents := []canonical.Event{
		frictionTestEvent(codexID, "codex-exit", codexAt.Add(1*time.Second), map[string]any{
			"tool_name": "Shell", "tool_output": "Exit code 9", "exit_code": 9,
		}),
	}
	if _, err := store.IngestEvents(ctx, codexID, codexEvents); err != nil {
		t.Fatal(err)
	}
	if _, err := store.IngestFriction(ctx, codexID, codexEvents); err != nil {
		t.Fatal(err)
	}

	handler := NewServerWithDB(db).Handler()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/friction?limit=10&sort=count", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("friction overview status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var overview struct {
		Summary struct {
			TotalEvents     int `json:"total_events"`
			ToolErrors      int `json:"tool_error_count"`
			NonzeroExits    int `json:"nonzero_exit_count"`
			AssetViolations int `json:"asset_violation_count"`
			SessionCount    int `json:"session_count"`
			ProjectCount    int `json:"project_count"`
		} `json:"summary"`
		Groups []struct {
			ProjectKey          string `json:"project_key"`
			Harness             string `json:"harness"`
			FrictionCount       int    `json:"friction_count"`
			ToolErrorCount      int    `json:"tool_error_count"`
			NonzeroExitCount    int    `json:"nonzero_exit_count"`
			AssetViolationCount int    `json:"asset_violation_count"`
			SessionCount        int    `json:"session_count"`
		} `json:"groups"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	if overview.Summary.TotalEvents != 5 || overview.Summary.ToolErrors != 2 || overview.Summary.NonzeroExits != 3 || overview.Summary.AssetViolations != 1 || overview.Summary.SessionCount != 2 || overview.Summary.ProjectCount != 1 {
		t.Fatalf("friction summary = %+v", overview.Summary)
	}
	if len(overview.Groups) != 2 {
		t.Fatalf("friction groups = %+v, want Claude Code and Codex", overview.Groups)
	}
	claudeGroup := overview.Groups[0]
	if claudeGroup.Harness != string(adapters.SourceClaudeCode) || claudeGroup.ProjectKey != projectRoot || claudeGroup.FrictionCount != 4 || claudeGroup.ToolErrorCount != 2 || claudeGroup.NonzeroExitCount != 2 || claudeGroup.AssetViolationCount != 1 || claudeGroup.SessionCount != 1 {
		t.Fatalf("Claude friction group = %+v", claudeGroup)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/friction?view=detail&project="+projectRoot+"&harness=claude_code&kind=tool_error&limit=10", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("friction detail status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var detail struct {
		Summary struct {
			TotalEvents  int `json:"total_events"`
			ToolErrors   int `json:"tool_error_count"`
			NonzeroExits int `json:"nonzero_exit_count"`
		} `json:"summary"`
		Records []struct {
			SourceEventID string   `json:"source_event_id"`
			EventID       *int64   `json:"event_id"`
			FrictionKinds []string `json:"friction_kinds"`
			SessionTitle  string   `json:"session_title"`
			ToolName      string   `json:"tool_name"`
			Category      string   `json:"category"`
			CategoryRule  string   `json:"category_rule"`
			IsError       *bool    `json:"is_error"`
			ExitCode      *int     `json:"exit_code"`
		} `json:"records"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if detail.Summary.TotalEvents != 4 || detail.Summary.ToolErrors != 2 || detail.Summary.NonzeroExits != 2 || len(detail.Records) != 2 {
		t.Fatalf("friction detail summary=%+v records=%+v", detail.Summary, detail.Records)
	}
	if detail.Records[0].SessionTitle != "Claude 工具失败会话" || detail.Records[0].ToolName == "" {
		t.Fatalf("friction detail metadata = %+v", detail.Records)
	}
	for _, record := range detail.Records {
		if record.Category == "" || record.CategoryRule == "" || record.EventID == nil {
			t.Fatalf("friction detail record is missing category, rule or event id: %+v", record)
		}
	}
	for _, record := range detail.Records {
		if len(record.FrictionKinds) == 0 || record.FrictionKinds[0] != "tool_error" || record.IsError == nil || !*record.IsError {
			t.Fatalf("tool error detail record = %+v", record)
		}
	}
}

func TestFrictionAPIClassifiesGroupsAndFiltersByCategoryAndTool(t *testing.T) {
	db := testAPIDB(t)
	ctx := context.Background()
	store := eventstore.New(db)
	projectRoot := "/synthetic/project-b"
	at := time.Date(2026, 7, 3, 9, 0, 0, 0, time.UTC)
	sessionID, err := store.IngestSession(ctx, adapters.SourceClaudeCode, adapters.SessionMeta{
		SourceSessionID: "friction-categories", StartedAt: &at, CWD: projectRoot, Title: "分类会话",
	})
	if err != nil {
		t.Fatal(err)
	}
	events := []canonical.Event{
		frictionTestCallEvent(sessionID, "cat-call-1", at.Add(1*time.Second), map[string]any{
			"tool_name": "Bash", "tool_use_id": "toolu_1", "tool_input": `{"command":"go test ./..."}`,
		}),
		frictionTestEvent(sessionID, "cat-result-1", at.Add(2*time.Second), map[string]any{
			"tool_use_id": "toolu_1", "tool_output": "--- FAIL: TestSynthetic (0.01s)", "exit_code": 1,
		}),
		frictionTestCallEvent(sessionID, "cat-call-2", at.Add(3*time.Second), map[string]any{
			"tool_name": "Read", "tool_use_id": "toolu_2", "tool_input": `{"file_path":"/synthetic/project-b/missing.md"}`,
		}),
		frictionTestEvent(sessionID, "cat-result-2", at.Add(4*time.Second), map[string]any{
			"tool_use_id": "toolu_2", "tool_output": "File does not exist.", "is_error": true,
		}),
		frictionTestEvent(sessionID, "cat-result-3", at.Add(5*time.Second), map[string]any{
			"tool_use_id": "toolu_unlinked", "tool_output": "Exit code 9", "exit_code": 9,
		}),
		frictionTestMessageEvent(sessionID, "cat-interrupt", at.Add(6*time.Second), "[Request interrupted by user]"),
	}
	if _, err := store.IngestEvents(ctx, sessionID, events); err != nil {
		t.Fatal(err)
	}
	if _, err := store.IngestFriction(ctx, sessionID, events); err != nil {
		t.Fatal(err)
	}
	handler := NewServerWithDB(db).Handler()

	var overview struct {
		Summary struct {
			TotalEvents    int `json:"total_events"`
			ToolErrors     int `json:"tool_error_count"`
			NonzeroExits   int `json:"nonzero_exit_count"`
			UserInterrupts int `json:"user_interrupt_count"`
			ToolUnrecorded int `json:"tool_unrecorded_count"`
			ByCategory     []struct {
				Key   string `json:"key"`
				Rule  string `json:"rule"`
				Count int    `json:"count"`
			} `json:"by_category"`
			ByTool []struct {
				Key   string `json:"key"`
				Count int    `json:"count"`
			} `json:"by_tool"`
		} `json:"summary"`
		GroupBy string `json:"group_by"`
		Groups  []struct {
			Key           string `json:"key"`
			GroupBy       string `json:"group_by"`
			Category      string `json:"category"`
			CategoryRule  string `json:"category_rule"`
			ToolName      string `json:"tool_name"`
			FrictionCount int    `json:"friction_count"`
		} `json:"groups"`
	}
	getJSON(t, handler, "/api/v1/friction?limit=50", &overview)
	if overview.Summary.TotalEvents != 4 || overview.Summary.UserInterrupts != 1 || overview.Summary.NonzeroExits != 2 || overview.Summary.ToolErrors != 1 {
		t.Fatalf("friction summary = %+v", overview.Summary)
	}
	if overview.Summary.ToolUnrecorded != 2 {
		t.Fatalf("tool_unrecorded_count = %d, want 2 (one unlinked result and one interrupt)", overview.Summary.ToolUnrecorded)
	}
	categories := map[string]int{}
	rules := map[string]string{}
	for _, item := range overview.Summary.ByCategory {
		categories[item.Key] = item.Count
		rules[item.Key] = item.Rule
	}
	want := map[string]int{"test_failure": 1, "file_not_found": 1, "nonzero_exit": 1, "user_interrupt": 1}
	for key, count := range want {
		if categories[key] != count {
			t.Fatalf("by_category = %+v, want %+v", categories, want)
		}
		if rules[key] == "" {
			t.Fatalf("category %q has no one-line rule", key)
		}
	}
	tools := map[string]int{}
	for _, item := range overview.Summary.ByTool {
		tools[item.Key] = item.Count
	}
	if tools["Bash"] != 1 || tools["Read"] != 1 || tools["__unrecorded__"] != 2 {
		t.Fatalf("by_tool = %+v", tools)
	}

	getJSON(t, handler, "/api/v1/friction?group=category&limit=50", &overview)
	if overview.GroupBy != "category" || len(overview.Groups) != 4 {
		t.Fatalf("category groups = %+v", overview.Groups)
	}
	for _, group := range overview.Groups {
		if group.GroupBy != "category" || group.Category == "" || group.CategoryRule == "" {
			t.Fatalf("category group = %+v", group)
		}
	}

	getJSON(t, handler, "/api/v1/friction?group=tool&limit=50", &overview)
	if overview.GroupBy != "tool" || len(overview.Groups) != 3 {
		t.Fatalf("tool groups = %+v", overview.Groups)
	}

	var detail struct {
		Records []struct {
			EventID      *int64 `json:"event_id"`
			ToolName     string `json:"tool_name"`
			Category     string `json:"category"`
			CategoryRule string `json:"category_rule"`
		} `json:"records"`
	}
	getJSON(t, handler, "/api/v1/friction?view=detail&project="+projectRoot+"&harness=claude_code&category=test_failure&limit=10", &detail)
	if len(detail.Records) != 1 {
		t.Fatalf("category filtered records = %+v", detail.Records)
	}
	record := detail.Records[0]
	if record.ToolName != "Bash" || record.Category != "test_failure" || record.EventID == nil || !strings.Contains(record.CategoryRule, "FAIL") {
		t.Fatalf("category filtered record = %+v", record)
	}

	detail.Records = nil
	getJSON(t, handler, "/api/v1/friction?view=detail&project="+projectRoot+"&harness=claude_code&tool=__unrecorded__&limit=10", &detail)
	if len(detail.Records) != 2 {
		t.Fatalf("unrecorded tool records = %+v", detail.Records)
	}
	for _, item := range detail.Records {
		if item.ToolName != "" {
			t.Fatalf("unrecorded tool record carries a tool name: %+v", item)
		}
	}
}

func TestFrictionAPIGroupsRecurringSignaturesAndFiltersByThem(t *testing.T) {
	db := testAPIDB(t)
	ctx := context.Background()
	store := eventstore.New(db)
	projectRoot := "/synthetic/project-signature"
	at := time.Date(2026, 7, 4, 8, 0, 0, 0, time.UTC)
	// The same failing command in two sessions, once with a different run
	// number and a different absolute path: normalization is what makes them
	// one signature rather than two.
	outputs := []string{
		"ls: cannot access '/synthetic/project-signature/run-1/notes.md': No such file or directory",
		"ls: cannot access '/srv/checkout/run-42/notes.md': No such file or directory",
	}
	for index, output := range outputs {
		sessionID, err := store.IngestSession(ctx, adapters.SourceClaudeCode, adapters.SessionMeta{
			SourceSessionID: "signature-" + strconv.Itoa(index), StartedAt: &at, CWD: projectRoot, Title: "签名会话",
		})
		if err != nil {
			t.Fatal(err)
		}
		events := []canonical.Event{
			frictionTestCallEvent(sessionID, "sig-call-"+strconv.Itoa(index), at.Add(time.Second), map[string]any{
				"tool_name": "Bash", "tool_use_id": "toolu_sig_" + strconv.Itoa(index), "tool_input": `{"command":"ls notes.md"}`,
			}),
			frictionTestEvent(sessionID, "sig-result-"+strconv.Itoa(index), at.Add(2*time.Second), map[string]any{
				"tool_use_id": "toolu_sig_" + strconv.Itoa(index), "tool_output": output, "exit_code": 2,
			}),
		}
		if index == 0 {
			// One-off friction, so exactly one of the two signatures recurs.
			events = append(events, frictionTestEvent(sessionID, "sig-other", at.Add(3*time.Second), map[string]any{
				"tool_use_id": "toolu_unlinked", "tool_output": "make: *** [Makefile:3] Error 2", "exit_code": 2,
			}))
		}
		if _, err := store.IngestEvents(ctx, sessionID, events); err != nil {
			t.Fatal(err)
		}
		if _, err := store.IngestFriction(ctx, sessionID, events); err != nil {
			t.Fatal(err)
		}
	}
	handler := NewServerWithDB(db).Handler()

	var overview struct {
		Summary struct {
			RecurringSignatures int `json:"recurring_signatures"`
		} `json:"summary"`
		GroupBy string `json:"group_by"`
		Groups  []struct {
			Key          string `json:"key"`
			Signature    string `json:"signature"`
			SampleLine   string `json:"sample_line"`
			Category     string `json:"category"`
			CategoryRule string `json:"category_rule"`
			ToolName     string `json:"tool_name"`
			Count        int    `json:"count"`
			SessionCount int    `json:"session_count"`
			ProjectCount int    `json:"project_count"`
			FirstAt      string `json:"first_occurred_at"`
			LastAt       string `json:"last_occurred_at"`
		} `json:"groups"`
	}
	getJSON(t, handler, "/api/v1/friction?group=signature&project="+projectRoot+"&limit=50", &overview)
	if overview.GroupBy != "signature" || len(overview.Groups) != 2 {
		t.Fatalf("signature groups = %+v", overview.Groups)
	}
	// Two sessions beats the single-session signature: that is the default sort.
	recurring := overview.Groups[0]
	if recurring.SessionCount != 2 || recurring.Count != 2 || recurring.ProjectCount != 1 {
		t.Fatalf("recurring group = %+v", recurring)
	}
	if recurring.Category != "file_not_found" || recurring.ToolName != "Bash" {
		t.Fatalf("recurring group classification = %+v", recurring)
	}
	if recurring.Signature != "file_not_found|Bash|"+recurring.SampleLine {
		t.Fatalf("signature %q does not carry sample line %q", recurring.Signature, recurring.SampleLine)
	}
	if !strings.Contains(recurring.SampleLine, "notes.md") || strings.Contains(recurring.SampleLine, "/srv/") {
		t.Fatalf("sample line = %q, want the normalized line with the path reduced", recurring.SampleLine)
	}
	if recurring.CategoryRule == "" || recurring.FirstAt == "" || recurring.LastAt == "" {
		t.Fatalf("recurring group metadata = %+v", recurring)
	}
	if overview.Summary.RecurringSignatures != 1 {
		t.Fatalf("recurring_signatures = %d, want 1", overview.Summary.RecurringSignatures)
	}

	var filtered struct {
		Summary struct {
			TotalEvents int `json:"total_events"`
		} `json:"summary"`
		Records []struct {
			Signature string `json:"signature"`
			Category  string `json:"category"`
		} `json:"records"`
	}
	getJSON(t, handler,
		"/api/v1/friction?view=detail&project="+projectRoot+"&harness=claude_code&signature="+url.QueryEscape(recurring.Signature)+"&limit=10",
		&filtered)
	if filtered.Summary.TotalEvents != 2 || len(filtered.Records) != 2 {
		t.Fatalf("signature filter kept %d records (summary %+v)", len(filtered.Records), filtered.Summary)
	}
	for _, record := range filtered.Records {
		if record.Signature != recurring.Signature || record.Category != "file_not_found" {
			t.Fatalf("filtered record = %+v", record)
		}
	}
}

func frictionTestCallEvent(sessionID, sourceEventID string, occurredAt time.Time, payload map[string]any) canonical.Event {
	return canonical.Event{
		SourceEventID: sourceEventID, SessionID: sessionID, EventType: canonical.EventTypeTranscriptToolCall,
		ObservationLevel: canonical.LevelUnknown, Payload: payload,
		Locator: canonical.Locator{Source: "fixture", SessionID: sessionID, RawRef: "fixture:" + sourceEventID}, OccurredAt: &occurredAt,
	}
}

func frictionTestMessageEvent(sessionID, sourceEventID string, occurredAt time.Time, text string) canonical.Event {
	return canonical.Event{
		SourceEventID: sourceEventID, SessionID: sessionID, EventType: canonical.EventTypeTranscriptMessage,
		ObservationLevel: canonical.LevelUnknown, Payload: map[string]any{"role": "user", "text": text},
		Locator: canonical.Locator{Source: "fixture", SessionID: sessionID, RawRef: "fixture:" + sourceEventID}, OccurredAt: &occurredAt,
	}
}

func frictionTestEvent(sessionID, sourceEventID string, occurredAt time.Time, payload map[string]any) canonical.Event {
	return canonical.Event{
		SourceEventID: sourceEventID, SessionID: sessionID, EventType: canonical.EventTypeTranscriptResult,
		ObservationLevel: canonical.LevelUnknown, Payload: payload,
		Locator: canonical.Locator{Source: "fixture", SessionID: sessionID, RawRef: "fixture:" + sourceEventID}, OccurredAt: &occurredAt,
	}
}

// The friction endpoint used to compare from/to as literal text, so from=all
// asked for "records after the string 'all'" and returned nothing. It now
// reads the window the same way every other endpoint does, relative forms
// included, and carries the English rendering of the rule beside the Chinese
// one.
func TestFrictionAPIReadsTheSameTimeWindowAsEveryOtherEndpoint(t *testing.T) {
	db := testAPIDB(t)
	ctx := context.Background()
	store := eventstore.New(db)
	now := time.Now().UTC()
	recent, old := now.AddDate(0, 0, -3), now.AddDate(0, 0, -200)
	sessionID, err := store.IngestSession(ctx, adapters.SourceClaudeCode, adapters.SessionMeta{
		SourceSessionID: "friction-window", StartedAt: &old, CWD: "/synthetic/project-w", Title: "窗口会话",
	})
	if err != nil {
		t.Fatal(err)
	}
	events := []canonical.Event{
		frictionTestCallEvent(sessionID, "win-call-old", old, map[string]any{
			"tool_name": "Bash", "tool_use_id": "toolu_old", "tool_input": `{"command":"ruff check ."}`,
		}),
		frictionTestEvent(sessionID, "win-result-old", old.Add(time.Second), map[string]any{
			"tool_use_id": "toolu_old", "tool_output": "ruff: command not found", "exit_code": 127,
		}),
		frictionTestCallEvent(sessionID, "win-call-recent", recent, map[string]any{
			"tool_name": "Bash", "tool_use_id": "toolu_recent", "tool_input": `{"command":"pytest -q"}`,
		}),
		frictionTestEvent(sessionID, "win-result-recent", recent.Add(time.Second), map[string]any{
			"tool_use_id": "toolu_recent", "tool_output": "Exit code 9", "exit_code": 9,
		}),
	}
	if _, err := store.IngestEvents(ctx, sessionID, events); err != nil {
		t.Fatal(err)
	}
	if _, err := store.IngestFriction(ctx, sessionID, events); err != nil {
		t.Fatal(err)
	}
	handler := NewServerWithDB(db).Handler()

	type overview struct {
		Summary struct {
			TotalEvents int `json:"total_events"`
			ByCategory  []struct {
				Key    string `json:"key"`
				Rule   string `json:"rule"`
				RuleEN string `json:"rule_en"`
			} `json:"by_category"`
		} `json:"summary"`
		Groups []struct {
			CategoryRule   string `json:"category_rule"`
			CategoryRuleEN string `json:"category_rule_en"`
		} `json:"groups"`
	}
	counts := map[string]int{}
	for _, window := range []string{"", "all", "90d", "30d", "1d"} {
		var body overview
		getJSON(t, handler, "/api/v1/friction?limit=50&from="+window, &body)
		counts[window] = body.Summary.TotalEvents
	}
	if counts[""] != 2 || counts["all"] != 2 {
		t.Fatalf("unfiltered = %d and from=all = %d, want 2 and 2", counts[""], counts["all"])
	}
	if counts["90d"] != 1 || counts["30d"] != 1 {
		t.Fatalf("from=90d = %d and from=30d = %d, want 1 each (only the recent record)",
			counts["90d"], counts["30d"])
	}
	if counts["1d"] != 0 {
		t.Fatalf("from=1d = %d, want 0", counts["1d"])
	}
	if counts["30d"] > counts["all"] {
		t.Fatalf("a narrower window returned more records: %d > %d", counts["30d"], counts["all"])
	}

	var body overview
	getJSON(t, handler, "/api/v1/friction?from=all&group=signature&limit=50", &body)
	if len(body.Groups) == 0 {
		t.Fatal("from=all returned no signature groups")
	}
	for _, group := range body.Groups {
		if group.CategoryRule == "" || group.CategoryRuleEN == "" {
			t.Errorf("group rule = %q / %q, want both languages", group.CategoryRule, group.CategoryRuleEN)
		}
	}
	for _, item := range body.Summary.ByCategory {
		if item.Rule == "" || item.RuleEN == "" {
			t.Errorf("category %q rule = %q / %q, want both languages", item.Key, item.Rule, item.RuleEN)
		}
	}
}

// The project's name comes from its recorded key. A worktree's cwd sorting
// alphabetically last was winning MAX(cwd) and renaming cognode to
// "wechat-mp-ui-prototype" on both the list and the detail header — cwd is
// only the name when no project key was recorded at all.
func TestFrictionProjectLabelPrefersTheRecordedKey(t *testing.T) {
	worktree := sql.NullString{String: "/home/tongyu/project/cognode/.claude/worktrees/wechat-mp-ui-prototype", Valid: true}
	if got := frictionProjectLabel("/home/tongyu/project/cognode", worktree); got != "cognode" {
		t.Errorf("label = %q, want the key's own name", got)
	}
	if got := frictionProjectLabel(frictionUnrecordedKey, worktree); got != "wechat-mp-ui-prototype" {
		t.Errorf("unrecorded-key label = %q, want the cwd fallback", got)
	}
	if got := frictionProjectLabel(frictionUnrecordedKey, sql.NullString{}); got != "项目未记录" {
		t.Errorf("nothing-recorded label = %q", got)
	}
}
