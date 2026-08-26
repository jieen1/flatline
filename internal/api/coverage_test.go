package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"flatline/internal/canonical"
	"flatline/internal/eventstore"
	"flatline/internal/storage"
)

func coverageFixtureDB(t *testing.T, ruleText string) *storage.DB {
	t.Helper()
	db := periodFixtureDB(t)
	ctx := context.Background()
	store := eventstore.New(db)
	dir := t.TempDir()
	rulePath := filepath.Join(dir, "workflow.md")
	if err := os.WriteFile(rulePath, []byte(ruleText), 0o644); err != nil {
		t.Fatalf("write rule: %v", err)
	}
	// A user-scope rule applies to every project, which is what these two
	// tests vary; the per-project scoping has its own test below.
	exec(t, db, `INSERT INTO assets (id, kind, scope, name, source_path, first_seen_at)
		VALUES ('rule:user:workflow', 'rule', 'user', 'workflow', ?, ?)`,
		rulePath, periodStart.Format(time.RFC3339Nano))

	for index, name := range []string{"period-a", "period-b"} {
		var sessionID string
		if err := db.QueryRowContext(ctx, `SELECT id FROM sessions WHERE source_session_id = ?`, name).Scan(&sessionID); err != nil {
			t.Fatalf("session %s: %v", name, err)
		}
		events := []canonical.Event{
			frictionTestCallEvent(sessionID, sessionID+"-hr-call", periodStart.Add(3*time.Minute), map[string]any{
				"tool_name": "Edit", "tool_use_id": sessionID + "-hr", "tool_input": `{"file_path":"/synthetic/x.go"}`,
			}),
			frictionTestEvent(sessionID, sessionID+"-hr-result", periodStart.Add(4*time.Minute), map[string]any{
				"tool_use_id": sessionID + "-hr", "is_error": true,
				"tool_output": "<tool_use_error>File has not been read yet. Read it first before writing to it.</tool_use_error>",
			}),
		}
		if _, err := store.IngestEvents(ctx, sessionID, events); err != nil {
			t.Fatalf("ingest events %d: %v", index, err)
		}
		if _, err := store.IngestFriction(ctx, sessionID, events); err != nil {
			t.Fatalf("ingest friction %d: %v", index, err)
		}
	}
	return db
}

func coverageGapsOf(t *testing.T, db *storage.DB) []frictionCoverageGap {
	t.Helper()
	handler := NewServerWithDB(db).Handler()
	var page struct {
		Summary frictionSummaryResponse `json:"summary"`
	}
	getJSON(t, handler, "/api/v1/friction?group=signature&limit=50", &page)
	return page.Summary.CoverageGaps
}

func TestCoverageGapNamesARecurringHarnessRuleNoUserRuleMentions(t *testing.T) {
	gaps := coverageGapsOf(t, coverageFixtureDB(t, "# 工作方式\n\n- 小步提交。\n"))
	if len(gaps) != 1 {
		t.Fatalf("coverage_gaps = %+v, want the one recurring harness rule", gaps)
	}
	gap := gaps[0]
	if gap.HintKind != "harness_rule" || gap.SessionCount != 2 {
		t.Fatalf("gap = %+v, want harness_rule over 2 sessions", gap)
	}
	if gap.Mechanism == "" || gap.SampleLine == "" {
		t.Fatalf("gap = %+v, want the mechanism and the sample line filled in", gap)
	}
}

func TestCoverageGapDisappearsWhenARuleMentionsTheMechanism(t *testing.T) {
	gaps := coverageGapsOf(t, coverageFixtureDB(t, "# Workflow\n\n- Read before you edit: read the code you are about to change first.\n"))
	if len(gaps) != 0 {
		t.Fatalf("coverage_gaps = %+v, want none: a rule asset mentions the mechanism", gaps)
	}
}

// TestCoverageGapIsScopedToTheProject pins the scoping that keeps one project's
// rules from shielding another: a rule under project A covers the gap there,
// while the same recurring signature stays a gap in project B.
func TestCoverageGapIsScopedToTheProject(t *testing.T) {
	db := coverageFixtureDB(t, "# 工作方式\n\n- 小步提交。\n")
	// Project A becomes a real temp directory so the project rule can live at
	// its registered path; project B stays synthetic and rule-less.
	covered := t.TempDir()
	exec(t, db, `UPDATE sessions SET project_key = ? WHERE project_key = ?`,
		covered, periodProject)
	ruleDir := filepath.Join(covered, ".claude", "rules")
	if err := os.MkdirAll(ruleDir, 0o755); err != nil {
		t.Fatalf("mkdir rules: %v", err)
	}
	rulePath := filepath.Join(ruleDir, "local.md")
	if err := os.WriteFile(rulePath, []byte("# Local\n\n- Read before you edit, always, in this project.\n"), 0o644); err != nil {
		t.Fatalf("write project rule: %v", err)
	}
	exec(t, db, `INSERT INTO assets (id, kind, scope, name, source_path, first_seen_at)
		VALUES ('rule:project:local', 'rule', 'project', 'local', ?, ?)`,
		rulePath, periodStart.Format(time.RFC3339Nano))

	other := filepath.Join(t.TempDir(), "project-other")
	for index := 0; index < 2; index++ {
		sessionID := "opencode:ses-cov-" + string(rune('a'+index))
		at := periodStart.Add(time.Duration(index) * time.Hour)
		exec(t, db, `INSERT INTO sessions
			(id, source, source_session_id, started_at, ended_at, thread_kind, title, project_key)
			VALUES (?, 'opencode', ?, ?, ?, 'main', ?, ?)`,
			sessionID, "cov-"+string(rune('a'+index)), at.Format(time.RFC3339Nano),
			at.Add(time.Hour).Format(time.RFC3339Nano), "覆盖夹具", other)
		exec(t, db, `INSERT INTO friction_records
			(session_id, source_event_id, friction_kind, event_type, observation_level, tool_name, category,
			 category_rule, category_rule_en, classifier_version, signature, is_error, payload_json, locator_json, occurred_at, created_at)
			VALUES (?, ?, 'tool_error', 'transcript_tool_result', 'invoked', 'Edit', 'tool_error',
			        '先读后写', 'read before edit', 'friction/test',
			        'tool_error|Edit|<tool_use_error>file has not been read yet. read it f', 1,
			        '{"tool_output":"<tool_use_error>File has not been read yet."}', '{}', ?, ?)`,
			sessionID, "cov-rec-"+string(rune('a'+index)), at.Format(time.RFC3339Nano), at.Format(time.RFC3339Nano))
	}

	gaps := coverageGapsOf(t, db)
	byProject := map[string][]frictionCoverageGap{}
	for _, gap := range gaps {
		byProject[gap.ProjectKey] = append(byProject[gap.ProjectKey], gap)
	}
	if len(byProject[covered]) != 0 {
		t.Errorf("project with a mentioning rule still reports gaps: %+v", byProject[covered])
	}
	if len(byProject[other]) != 1 {
		t.Fatalf("project without coverage reports %+v, want exactly one gap", byProject[other])
	}
	gap := byProject[other][0]
	if gap.SessionCount != 2 || gap.ProjectKey != other {
		t.Errorf("gap = %+v, want 2 sessions in project %s", gap, other)
	}
}
