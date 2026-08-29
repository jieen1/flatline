package api

import (
	"context"
	"testing"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/eventstore"
	"flatline/internal/storage"
)

const fleetProject = "/synthetic/project-fleet"

// fleetFixtureDB is one parent commanding three children — a dev that edited
// and committed, a reviewer, and a child still in progress — plus an unrelated
// session that must stay out of every rollup.
func fleetFixtureDB(t *testing.T) (*storage.DB, map[string]string) {
	t.Helper()
	db := testAPIDB(t)
	ctx := context.Background()
	store := eventstore.New(db)
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

	ids := make(map[string]string, 5)
	add := func(key, sourceID, title string, minutes int) {
		end := at.Add(time.Duration(minutes) * time.Minute)
		id, err := store.IngestSession(ctx, adapters.SourceClaudeCode, adapters.SessionMeta{
			SourceSessionID: sourceID, StartedAt: &at, EndedAt: &end, CWD: fleetProject, Title: title,
		})
		if err != nil {
			t.Fatalf("ingest %s: %v", sourceID, err)
		}
		if err := store.RecomputeSessionStats(ctx, id); err != nil {
			t.Fatalf("stats %s: %v", sourceID, err)
		}
		ids[key] = id
	}
	add("parent", "fleet-parent", "发一支小队修 issue-9", 300)
	add("dev", "fleet-dev", "", 200)
	add("reviewer", "fleet-reviewer", "", 60)
	add("live", "fleet-live", "", 30)
	add("stranger", "fleet-stranger", "隔壁无关会话", 60)

	exec(t, db, `UPDATE session_stats SET is_empty = 0`)
	exec(t, db, `UPDATE sessions SET thread_kind = 'main' WHERE id IN (?, ?)`, ids["parent"], ids["stranger"])
	for key, role := range map[string]string{"dev": "dev-9", "reviewer": "reviewer-9", "live": "qa-9"} {
		exec(t, db, `UPDATE sessions SET thread_kind = 'subagent', parent_session_id = ?, agent_role = ? WHERE id = ?`,
			ids["parent"], role, key2id(ids, key))
	}

	stamp := at.Format(time.RFC3339Nano)
	usage := func(id string, input, cached, write, output, added, removed, files int) {
		exec(t, db, `INSERT INTO session_usage
			(session_id, input_tokens, cached_input_tokens, cache_write_tokens, output_tokens, reasoning_tokens,
			 total_tokens, assistant_turns, user_turns, lines_added, lines_removed, files_changed, usage_source, parser_version, computed_at)
			VALUES (?, ?, ?, ?, ?, 0, ?, 3, 1, ?, ?, ?, 'claude_transcript', 'parser/test', ?)`,
			id, input, cached, write, output, input+cached+write+output, added, removed, files, stamp)
	}
	usage(ids["parent"], 1000, 100000, 5000, 2000, 0, 0, 0)
	usage(ids["dev"], 500, 50000, 2500, 1500, 120, 30, 4)
	usage(ids["reviewer"], 200, 20000, 1000, 800, 0, 0, 0)
	// The live child has no usage row: its numbers must read unrecorded, not 0.
	usage(ids["stranger"], 9999, 9999, 9999, 9999, 9, 9, 9)

	// Friction on the dev child only.
	exec(t, db, `INSERT INTO friction_records
		(session_id, source_event_id, friction_kind, event_type, observation_level, tool_name, category,
		 classifier_version, signature, payload_json, locator_json, occurred_at, created_at)
		VALUES (?, 'fleet-f1', 'tool_error', 'transcript_tool_result', 'invoked', 'Edit', 'tool_error',
		        'friction/test', 'tool_error|Edit|x', '{}', '{}', ?, ?)`, ids["dev"], stamp, stamp)
	exec(t, db, `UPDATE session_stats SET friction_count = 1 WHERE session_id = ?`, ids["dev"])

	// Outcome evidence: two commits (one failed) and one push in the tree,
	// plus a commit in the unrelated session that must not be counted.
	commands := func(id string, eventID int, command string, exitCode any, isError any) {
		exec(t, db, `INSERT INTO session_commands (session_id, event_id, ordinal, tool_name, program, command, exit_code, is_error, expected_exit, occurred_at)
			VALUES (?, ?, 0, 'Bash', 'git', ?, ?, ?, 0, ?)`, id, eventID, command, exitCode, isError, stamp)
	}
	commands(ids["dev"], 9001, "git commit -m fix", nil, nil)
	commands(ids["dev"], 9002, "git commit -m broken", 1, 1)
	commands(ids["parent"], 9003, "git push origin main", nil, nil)
	commands(ids["stranger"], 9004, "git commit -m elsewhere", nil, nil)
	return db, ids
}

func key2id(ids map[string]string, key string) string { return ids[key] }

type fleetChildRow struct {
	ID           string        `json:"id"`
	AgentRole    *string       `json:"agent_role"`
	DisplayTitle *string       `json:"display_title"`
	FrictionCnt  int           `json:"friction_count"`
	Usage        usageResponse `json:"usage"`
}

type fleetResponse struct {
	SessionID string          `json:"session_id"`
	Children  []fleetChildRow `json:"children"`
	Rollup    struct {
		Sessions          int    `json:"sessions"`
		InputTokens       *int64 `json:"input_tokens"`
		CachedInputTokens *int64 `json:"cached_input_tokens"`
		CacheWriteTokens  *int64 `json:"cache_write_tokens"`
		OutputTokens      *int64 `json:"output_tokens"`
		TotalTokens       *int64 `json:"total_tokens"`
		WorkTokens        *int64 `json:"work_tokens"`
		TokenSessions     int    `json:"token_sessions"`
		FrictionCount     int    `json:"friction_count"`
		LinesAdded        *int64 `json:"lines_added"`
		LinesRemoved      *int64 `json:"lines_removed"`
	} `json:"rollup"`
	Outcome struct {
		Commits          int    `json:"commits_recorded"`
		CommitsNoFailure int    `json:"commits_no_failure"`
		Pushes           int    `json:"pushes_recorded"`
		PushesNoFailure  int    `json:"pushes_no_failure"`
		Merges           int    `json:"merges_recorded"`
		Note             string `json:"note"`
	} `json:"outcome"`
	Complete bool `json:"complete"`
}

func TestFleetRollsUpTheWholeTreeAndOnlyTheTree(t *testing.T) {
	db, ids := fleetFixtureDB(t)
	handler := NewServerWithDB(db).Handler()
	var fleet fleetResponse
	getJSON(t, handler, "/api/v1/sessions/"+ids["parent"]+"/fleet", &fleet)

	if !fleet.Complete || fleet.SessionID != ids["parent"] {
		t.Fatalf("fleet header = %+v", fleet)
	}
	if len(fleet.Children) != 3 {
		t.Fatalf("children = %d, want 3 (the stranger stays out)", len(fleet.Children))
	}
	// Children come largest spender first; the child with no usage row last.
	if fleet.Children[0].AgentRole == nil || *fleet.Children[0].AgentRole != "dev-9" {
		t.Errorf("first child = %+v, want the dev (largest tokens)", fleet.Children[0])
	}
	if fleet.Children[2].Usage.TotalTokens != nil {
		t.Errorf("live child usage = %+v, want unrecorded (null), not zero", fleet.Children[2].Usage)
	}
	if fleet.Children[0].FrictionCnt != 1 {
		t.Errorf("dev friction = %d, want 1", fleet.Children[0].FrictionCnt)
	}

	r := fleet.Rollup
	if r.Sessions != 4 {
		t.Errorf("rollup sessions = %d, want parent + 3 children", r.Sessions)
	}
	// parent 1000+100000+5000+2000, dev 500+50000+2500+1500, reviewer 200+20000+1000+800.
	wants := map[string]struct {
		got  *int64
		want int64
	}{
		"input":  {r.InputTokens, 1700},
		"cached": {r.CachedInputTokens, 170000},
		"write":  {r.CacheWriteTokens, 8500},
		"output": {r.OutputTokens, 4300},
		"total":  {r.TotalTokens, 184500},
		// work = input + output + cache write: the tokens that are not a
		// cheap cache read — the number the 98%-cached total hides.
		"work":  {r.WorkTokens, 14500},
		"added": {r.LinesAdded, 120}, "removed": {r.LinesRemoved, 30},
	}
	for name, w := range wants {
		if w.got == nil || *w.got != w.want {
			t.Errorf("rollup %s = %v, want %d", name, w.got, w.want)
		}
	}
	if r.TokenSessions != 3 {
		t.Errorf("token_sessions = %d, want 3 of 4 recorded", r.TokenSessions)
	}
	if r.FrictionCount != 1 {
		t.Errorf("rollup friction = %d, want 1", r.FrictionCount)
	}

	o := fleet.Outcome
	if o.Commits != 2 || o.CommitsNoFailure != 1 || o.Pushes != 1 || o.PushesNoFailure != 1 || o.Merges != 0 {
		t.Errorf("outcome = %+v, want 2 commits (1 clean), 1 push, 0 merges; the stranger's commit stays out", o)
	}
	if o.Note == "" {
		t.Error("outcome carries no note; the no-exit-code caveat has to be stated")
	}
}

func TestFleetOfAChildlessSessionIsJustTheSession(t *testing.T) {
	db, ids := fleetFixtureDB(t)
	handler := NewServerWithDB(db).Handler()
	var fleet fleetResponse
	getJSON(t, handler, "/api/v1/sessions/"+ids["stranger"]+"/fleet", &fleet)
	if len(fleet.Children) != 0 || fleet.Rollup.Sessions != 1 {
		t.Fatalf("childless fleet = %+v, want zero children and a self-only rollup", fleet)
	}
	if fleet.Rollup.WorkTokens == nil || *fleet.Rollup.WorkTokens != 9999*3 {
		t.Errorf("self rollup work tokens = %v", fleet.Rollup.WorkTokens)
	}
	if fleet.Outcome.Commits != 1 {
		t.Errorf("stranger commits = %d, want its own 1", fleet.Outcome.Commits)
	}
}
