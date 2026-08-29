package api

import (
	"context"
	"net/url"
	"testing"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/eventstore"
	"flatline/internal/friction"
	"flatline/internal/storage"
)

const knowledgeProject = "/synthetic/project-knowledge"

// knowledgeFixtureDB is the playbook shape (P18-3): one real working command
// used across sessions, plus the three shapes that must stay out — a
// navigation command, a single-session command, and a rarely-run one.
func knowledgeFixtureDB(t *testing.T) (*storage.DB, map[string]string) {
	t.Helper()
	db := testAPIDB(t)
	ctx := context.Background()
	store := eventstore.New(db)
	base := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)

	ids := make(map[string]string, 3)
	session := func(key string, start time.Time, cwd string) string {
		end := start.Add(time.Hour)
		id, err := store.IngestSession(ctx, adapters.SourceClaudeCode, adapters.SessionMeta{
			SourceSessionID: "kn-" + key, StartedAt: &start, EndedAt: &end, CWD: cwd})
		if err != nil {
			t.Fatalf("ingest %s: %v", key, err)
		}
		if err := store.RecomputeSessionStats(ctx, id); err != nil {
			t.Fatalf("stats %s: %v", key, err)
		}
		ids[key] = id
		return id
	}
	command := func(id string, eventID int, program, cmd string, exitCode any, isError any, at time.Time) {
		exec(t, db, `INSERT INTO session_commands (session_id, event_id, ordinal, tool_name, program, command, exit_code, is_error, expected_exit, occurred_at)
			VALUES (?, ?, 0, 'Bash', ?, ?, ?, ?, 0, ?)`, id, eventID, program, cmd, exitCode, isError, at.Format(time.RFC3339Nano))
	}

	one := session("one", base, knowledgeProject)
	two := session("two", base.Add(2*time.Hour), knowledgeProject)
	elsewhere := session("other", base.Add(3*time.Hour), "/synthetic/project-elsewhere")

	// The working command: 4 runs, 2 sessions, one recorded failure.
	command(one, 9401, "just", "just ci-fast", nil, nil, base.Add(5*time.Minute))
	command(one, 9402, "just", "just ci-fast", 1, 1, base.Add(10*time.Minute))
	command(two, 9403, "just", "just ci-fast", 0, nil, base.Add(125*time.Minute))
	command(two, 9404, "just", "just ci-fast", nil, nil, base.Add(130*time.Minute))
	// Navigation: many runs, many sessions — exploration, not a method.
	command(one, 9405, "grep", "grep -rn foo src", nil, nil, base.Add(6*time.Minute))
	command(two, 9406, "grep", "grep -rn foo src", nil, nil, base.Add(126*time.Minute))
	command(two, 9407, "grep", "grep -rn foo src", nil, nil, base.Add(127*time.Minute))
	// One session only: no corroboration yet.
	command(one, 9408, "npm", "npm test", nil, nil, base.Add(7*time.Minute))
	command(one, 9409, "npm", "npm test", nil, nil, base.Add(8*time.Minute))
	command(one, 9410, "npm", "npm test", nil, nil, base.Add(9*time.Minute))
	// Too few runs.
	command(one, 9411, "docker", "docker compose up -d", nil, nil, base.Add(11*time.Minute))
	command(two, 9412, "docker", "docker compose up -d", nil, nil, base.Add(131*time.Minute))
	// Waiting is not method: sleep would otherwise rank on real data.
	command(one, 9420, "sleep", "sleep 5", nil, nil, base.Add(12*time.Minute))
	command(one, 9421, "sleep", "sleep 5", nil, nil, base.Add(13*time.Minute))
	command(two, 9422, "sleep", "sleep 5", nil, nil, base.Add(132*time.Minute))
	// Another project's command must never leak in.
	command(elsewhere, 9413, "just", "just ci-fast", nil, nil, base.Add(200*time.Minute))
	command(elsewhere, 9414, "just", "just ci-fast", nil, nil, base.Add(201*time.Minute))
	command(elsewhere, 9415, "just", "just ci-fast", nil, nil, base.Add(202*time.Minute))

	exec(t, db, `UPDATE session_stats SET is_empty = 0`)
	return db, ids
}

func TestProjectKnowledgeListsWorkingCommandsWithTheirRecord(t *testing.T) {
	db, _ := knowledgeFixtureDB(t)
	handler := NewServerWithDB(db).Handler()
	var body struct {
		ProjectKey      string `json:"project_key"`
		WorkingCommands []struct {
			Label    string `json:"label"`
			Program  string `json:"program"`
			Runs     int    `json:"runs"`
			Sessions int    `json:"sessions"`
			Failures int    `json:"failures_recorded"`
			LastAt   string `json:"last_at"`
		} `json:"working_commands"`
		CommandsSeen int    `json:"commands_seen"`
		Note         string `json:"note"`
		NoteEN       string `json:"note_en"`
		Complete     bool   `json:"complete"`
	}
	getJSON(t, handler, "/api/v1/projects/"+url.QueryEscape(knowledgeProject)+"/knowledge", &body)
	if !body.Complete || body.Note == "" || body.NoteEN == "" {
		t.Fatalf("header = %+v, want the selection rule stated", body)
	}
	if len(body.WorkingCommands) != 1 {
		t.Fatalf("working_commands = %+v, want exactly the corroborated method; navigation, waiting, single-session, and rare commands stay out", body.WorkingCommands)
	}
	if body.CommandsSeen == 0 {
		t.Error("commands_seen = 0; the empty state has no denominator to explain itself with")
	}
	item := body.WorkingCommands[0]
	if item.Label != friction.NormalizeLine("just ci-fast") || item.Program != "just" {
		t.Errorf("item = %+v, want the normalized just ci-fast", item)
	}
	// Failures are reported, never used to hide the command: a test command
	// failing sometimes is normal use of a test command.
	if item.Runs != 4 || item.Sessions != 2 || item.Failures != 1 {
		t.Errorf("record = %+v, want 4 runs / 2 sessions / 1 recorded failure", item)
	}
	if item.LastAt == "" {
		t.Error("last_at missing")
	}
}
