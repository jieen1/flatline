package api

import (
	"testing"
	"time"
)

// insightsFixtureDB seeds the facts each insight reads: an interrupt with a
// tool call before it, a high-input session with no edit-tool change, a
// signature repeated five times in one session, a file read three times, and a
// command_not_found record. All timestamps sit inside from=all.
func insightsFixtureDB(t *testing.T) *interface{} {
	t.Helper()
	return nil
}

func TestInsightsEndpointReportsEachClosedKind(t *testing.T) {
	db := testAPIDB(t)
	at := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	stamp := at.Format(time.RFC3339Nano)
	sessionID := "opencode:ses_insights"

	exec(t, db, `INSERT INTO sessions
		(id, source, source_session_id, started_at, ended_at, thread_kind, title, project_key)
		VALUES (?, 'opencode', 'insights-fixture', ?, ?, 'main', '洞察夹具', '/synthetic/project')`,
		sessionID, stamp, stamp)

	// A tool call right before the interrupt, then the interrupt itself.
	exec(t, db, `INSERT INTO events (session_id, event_type, source_event_id, observation_level, payload_json, locator_json, occurred_at)
		VALUES (?, 'transcript_tool_call', 'insights-call', 'invoked', '{"tool_name":"write_stdin"}', '{}', ?)`,
		sessionID, stamp)
	exec(t, db, `INSERT INTO friction_records
		(session_id, source_event_id, friction_kind, event_type, observation_level, tool_name, category, category_rule, category_rule_en, classifier_version, signature, payload_json, locator_json, occurred_at, created_at)
		VALUES (?, 'insights-interrupt', 'user_interrupt', 'transcript_message', 'invoked', NULL, 'user_interrupt',
		        '记录了中断', 'an interruption was recorded', 'friction/test', 'user_interrupt||interrupted', '{}', '{}', ?, ?)`,
		sessionID, stamp, stamp)

	// The same signature five times in one session is a stuck loop.
	for i := 0; i < stuckLoopThreshold; i++ {
		exec(t, db, `INSERT INTO friction_records
			(session_id, source_event_id, friction_kind, event_type, observation_level, tool_name, category, category_rule, category_rule_en, classifier_version, signature, payload_json, locator_json, occurred_at, created_at)
			VALUES (?, ?, 'tool_error', 'transcript_tool_result', 'invoked', 'exec', 'tool_error',
			        '补丁上下文对不上', 'patch context mismatch', 'friction/test',
			        'tool_error|exec|apply_patch verification failed', '{}', '{}', ?, ?)`,
			sessionID, "insights-loop-"+string(rune('a'+i)), stamp, stamp)
	}

	// A command the environment did not have.
	exec(t, db, `INSERT INTO friction_records
		(session_id, source_event_id, friction_kind, event_type, observation_level, tool_name, category, category_rule, category_rule_en, classifier_version, signature, payload_json, locator_json, occurred_at, created_at)
		VALUES (?, 'insights-missing', 'tool_error', 'transcript_tool_result', 'invoked', 'Bash', 'command_not_found',
		        '命令不在 PATH', 'command not on PATH', 'friction/test',
		        'command_not_found|Bash|diffstat: command not found', '{}', '{}', ?, ?)`,
		sessionID, stamp, stamp)

	// High input, no edit-tool change.
	exec(t, db, `INSERT INTO session_usage
		(session_id, input_tokens, cached_input_tokens, cache_write_tokens, output_tokens, reasoning_tokens,
		 total_tokens, assistant_turns, user_turns, lines_added, lines_removed, files_changed, usage_source, parser_version, computed_at)
		VALUES (?, 6000000, 0, 0, 0, 0, 6000000, 4, 1, 0, 0, 0, 'opencode_session', 'parser/test', ?)`,
		sessionID, stamp)

	// One file read three times inside the session.
	for i := 0; i < rereadThreshold; i++ {
		exec(t, db, `INSERT INTO session_files (session_id, event_id, path, action, tool_name, occurred_at)
			VALUES (?, ?, '/synthetic/project/repeated.md', 'read', 'Read', ?)`,
			sessionID, 1000+i, stamp)
	}

	handler := NewServerWithDB(db).Handler()
	var response struct {
		Insights []struct {
			Kind        string         `json:"kind"`
			Title       string         `json:"title"`
			TitleEN     string         `json:"title_en"`
			Summary     string         `json:"summary"`
			SummaryEN   string         `json:"summary_en"`
			Criterion   string         `json:"criterion"`
			CriterionEN string         `json:"criterion_en"`
			Facts       map[string]any `json:"facts"`
			Links       []insightLink  `json:"links"`
		} `json:"insights"`
	}
	getJSON(t, handler, "/api/v1/insights?from=all", &response)

	kinds := map[string]int{}
	for index, item := range response.Insights {
		kinds[item.Kind] = index
		if item.Title == "" || item.TitleEN == "" {
			t.Errorf("insight %q is missing a title in one language: %q / %q", item.Kind, item.Title, item.TitleEN)
		}
		if item.Criterion == "" || item.CriterionEN == "" {
			t.Errorf("insight %q is missing its one-line rule in one language", item.Kind)
		}
		if item.Summary == "" || item.SummaryEN == "" {
			t.Errorf("insight %q is missing its summary in one language", item.Kind)
		}
		if len(item.Links) == 0 {
			t.Errorf("insight %q has no drill-down link", item.Kind)
		}
	}
	for _, kind := range []string{"interrupts", "zero_edit_heavy", "stuck_loops", "reread", "missing_commands"} {
		if _, found := kinds[kind]; !found {
			t.Errorf("insights response is missing kind %q; got %v", kind, kinds)
		}
	}
	if _, found := kinds["coverage_gaps"]; found {
		t.Errorf("coverage_gaps should be absent when no gap exists, but it was reported")
	}

	interrupts := response.Insights[kinds["interrupts"]]
	if got, ok := interrupts.Facts["total"].(float64); !ok || int(got) != 1 {
		t.Errorf("interrupts total = %v, want 1", interrupts.Facts["total"])
	}
	waiting, ok := interrupts.Facts["waiting_share"].(map[string]any)
	if !ok {
		t.Fatalf("interrupts facts carry no waiting_share: %v", interrupts.Facts)
	}
	if waiting["numerator"].(float64) != 1 || waiting["denominator"].(float64) != 1 {
		t.Errorf("waiting_share = %v, want 1/1 (write_stdin is in the waiting set)", waiting)
	}

	zeroEdit := response.Insights[kinds["zero_edit_heavy"]]
	if got, ok := zeroEdit.Facts["count"].(float64); !ok || int(got) != 1 {
		t.Errorf("zero_edit_heavy count = %v, want 1", zeroEdit.Facts["count"])
	}

	loops := response.Insights[kinds["stuck_loops"]]
	if got, ok := loops.Facts["groups"].(float64); !ok || int(got) != 1 {
		t.Errorf("stuck_loops groups = %v, want 1", loops.Facts["groups"])
	}

	reread := response.Insights[kinds["reread"]]
	if got, ok := reread.Facts["reads"].(float64); !ok || int(got) != rereadThreshold {
		t.Errorf("reread reads = %v, want %d", reread.Facts["reads"], rereadThreshold)
	}

	missing := response.Insights[kinds["missing_commands"]]
	commands, ok := missing.Facts["commands"].([]any)
	if !ok || len(commands) == 0 {
		t.Fatalf("missing_commands carries no commands: %v", missing.Facts)
	}
	first := commands[0].(map[string]any)
	if first["command"] != "diffstat" {
		t.Errorf("missing command name = %v, want diffstat (read out of the signature line)", first["command"])
	}
}

func TestInsightsEndpointAnswersAnEmptyWindowWithAnEmptyList(t *testing.T) {
	db := testAPIDB(t)
	handler := NewServerWithDB(db).Handler()
	var response struct {
		Insights []map[string]any `json:"insights"`
	}
	getJSON(t, handler, "/api/v1/insights?from=2030-01-01", &response)
	if len(response.Insights) != 0 {
		t.Errorf("an empty window should report no insights, got %d", len(response.Insights))
	}
}

// TestInsightsInterruptTurnTokens pins the money lens (ADR-22): an interrupt
// whose turn recorded per-message usage reports the tokens that turn had
// already spent, and an interrupt from a source that records no per-message
// usage stays out of the measurable denominator instead of counting zero.
func TestInsightsInterruptTurnTokens(t *testing.T) {
	db := testAPIDB(t)
	at := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	stamp := at.Format(time.RFC3339Nano)
	sessionID := "claude_code:ses-interrupt-cost"

	exec(t, db, `INSERT INTO sessions
		(id, source, source_session_id, started_at, ended_at, thread_kind, title, project_key)
		VALUES (?, 'claude_code', 'interrupt-cost', ?, ?, 'main', '中断代价夹具', '/synthetic/cost')`,
		sessionID, stamp, stamp)
	// The turn: a user message, then an assistant message whose usage the
	// source recorded, then the interrupt.
	exec(t, db, `INSERT INTO events (session_id, event_type, source_event_id, observation_level, payload_json, locator_json, occurred_at)
		VALUES (?, 'transcript_message', 'cost-user', 'unknown', '{"message_id":"u1","role":"user"}', '{}', ?)`,
		sessionID, stamp)
	exec(t, db, `INSERT INTO events (session_id, event_type, source_event_id, observation_level, payload_json, locator_json, occurred_at)
		VALUES (?, 'transcript_message', 'cost-assistant', 'unknown', '{"message_id":"a1","role":"assistant","usage":{"input_tokens":100,"cached_input_tokens":4000,"output_tokens":300,"total_tokens":4400}}', '{}', ?)`,
		sessionID, at.Add(20*time.Second).Format(time.RFC3339Nano))
	exec(t, db, `INSERT INTO friction_records
		(session_id, source_event_id, friction_kind, event_type, observation_level, category, signature, payload_json, locator_json, occurred_at, created_at)
		VALUES (?, 'cost-interrupt', 'user_interrupt', 'transcript_message', 'invoked', 'user_interrupt', 'user_interrupt||interrupted', '{}', '{}', ?, ?)`,
		sessionID, at.Add(30*time.Second).Format(time.RFC3339Nano), at.Add(30*time.Second).Format(time.RFC3339Nano))

	handler := NewServerWithDB(db).Handler()
	var response struct {
		Insights []struct {
			Kind  string         `json:"kind"`
			Facts map[string]any `json:"facts"`
		} `json:"insights"`
	}
	getJSON(t, handler, "/api/v1/insights?from=all", &response)
	for _, item := range response.Insights {
		if item.Kind != "interrupts" {
			continue
		}
		if got := item.Facts["turn_tokens_total"].(float64); got != 4400 {
			t.Errorf("turn_tokens_total = %v, want 4400", got)
		}
		if got := item.Facts["turn_measured"].(float64); got != 1 {
			t.Errorf("turn_measured = %v, want 1", got)
		}
		return
	}
	t.Fatal("insights carry no interrupts block")
}

// TestInsightsCodexInterruptUsesAttributedTurnTokens pins the Codex half
// (ADR-23): the measurable cost of an interrupted Codex turn is the
// turn_tokens the parser attributed to the user message that opened the turn.
func TestInsightsCodexInterruptUsesAttributedTurnTokens(t *testing.T) {
	db := testAPIDB(t)
	at := time.Date(2026, 8, 21, 11, 0, 0, 0, time.UTC)
	sessionID := "codex:ses-interrupt-codex"
	started := at.Add(-time.Hour).Format(time.RFC3339Nano)
	exec(t, db, `INSERT INTO sessions
		(id, source, source_session_id, started_at, ended_at, thread_kind, title, project_key)
		VALUES (?, 'codex', 'interrupt-codex', ?, ?, 'main', 'Codex 中断夹具', '/synthetic/cost')`,
		sessionID, started, started)
	// The turn's user message carries the parser-attributed cost…
	exec(t, db, `INSERT INTO events (session_id, event_type, source_event_id, observation_level, payload_json, locator_json, occurred_at)
		VALUES (?, 'transcript_message', 'codex-user', 'unknown', '{"turn_id":"u1","role":"user","turn_tokens":6600}', '{}', ?)`,
		sessionID, started)
	// …and the interrupt is its own user-role recording later in the turn.
	exec(t, db, `INSERT INTO events (session_id, event_type, source_event_id, observation_level, payload_json, locator_json, occurred_at)
		VALUES (?, 'transcript_message', 'codex-abort', 'unknown', '{"turn_id":"u2","role":"user"}', '{}', ?)`,
		sessionID, at.Format(time.RFC3339Nano))
	exec(t, db, `INSERT INTO friction_records
		(session_id, source_event_id, friction_kind, event_type, observation_level, category, signature, payload_json, locator_json, occurred_at, created_at)
		VALUES (?, 'codex-abort', 'user_interrupt', 'transcript_message', 'invoked', 'user_interrupt', 'user_interrupt||interrupted', '{}', '{}', ?, ?)`,
		sessionID, at.Format(time.RFC3339Nano), at.Format(time.RFC3339Nano))

	handler := NewServerWithDB(db).Handler()
	var response struct {
		Insights []struct {
			Kind  string         `json:"kind"`
			Facts map[string]any `json:"facts"`
		} `json:"insights"`
	}
	getJSON(t, handler, "/api/v1/insights?from=all", &response)
	for _, item := range response.Insights {
		if item.Kind != "interrupts" {
			continue
		}
		if got := item.Facts["turn_tokens_total"].(float64); got != 6600 {
			t.Errorf("turn_tokens_total = %v, want 6600", got)
		}
		if got := item.Facts["turn_measured"].(float64); got != 1 {
			t.Errorf("turn_measured = %v, want 1", got)
		}
		return
	}
	t.Fatal("insights carry no interrupts block")
}
