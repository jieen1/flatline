package runtime

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"flatline/internal/history"
)

// nativeFixtureRoots copies the synthetic native transcripts into a temp tree,
// so a test run never reads or writes the checked-in fixtures in place.
func nativeFixtureRoots(t *testing.T) (string, string) {
	t.Helper()
	source := filepath.Join("..", "..", "testdata", "native")
	target := t.TempDir()
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, content, 0o644)
	})
	if err != nil {
		t.Fatalf("copy native fixtures: %v", err)
	}
	return filepath.Join(target, "claude"), filepath.Join(target, "codex")
}

func importNativeFixtures(t *testing.T, app *App) history.Config {
	t.Helper()
	claudeRoot, codexRoot := nativeFixtureRoots(t)
	config := history.Config{ClaudeRoot: claudeRoot, CodexRoot: codexRoot, IncludeSubagents: true}
	report, err := app.ImportNativeHistory(context.Background(), config)
	if err != nil {
		t.Fatalf("import native fixtures: %v", err)
	}
	// Ten transcript files, ten sessions: a Claude Code subagent writes its
	// own transcript, and that transcript is its own thread even though every
	// record in it carries the parent's session id.
	if report.SessionsIngested != 10 {
		t.Fatalf("sessions ingested = %d, want 10 (warnings: %v)", report.SessionsIngested, report.Warnings)
	}
	return config
}

func TestNativeImportRecordsThreadHierarchy(t *testing.T) {
	app, db := testApp(t)
	_ = importNativeFixtures(t, app)

	type row struct {
		kind, parent, role, nickname, originator sql.NullString
	}
	want := map[string]row{
		"claude_code:cc-hierarchy-fixture": {kind: nullText("main"), originator: nullText("claude_code")},
		"claude_code:fixtagent01": {kind: nullText("subagent"),
			parent: nullText("claude_code:cc-hierarchy-fixture"), originator: nullText("claude_code")},
		"claude_code:fixtagent02": {kind: nullText("subagent"),
			parent: nullText("claude_code:cc-hierarchy-fixture"), originator: nullText("claude_code")},
		"codex:codex-main-fixture": {kind: nullText("main"), originator: nullText("codex_exec")},
		"codex:codex-subagent-fixture": {kind: nullText("subagent"), parent: nullText("codex:codex-main-fixture"),
			role: nullText("explore"), nickname: nullText("Ptolemy"), originator: nullText("codex-tui")},
		"codex:codex-fork-fixture": {kind: nullText("main"), parent: nullText("codex:codex-main-fixture"),
			originator: nullText("codex-tui")},
		"codex:codex-empty-fixture": {kind: nullText("main"), originator: nullText("codex_exec")},
	}
	for id, expected := range want {
		var got row
		if err := db.QueryRowContext(context.Background(), `
			SELECT thread_kind, parent_session_id, agent_role, agent_nickname, originator
			FROM sessions WHERE id = ?`, id).
			Scan(&got.kind, &got.parent, &got.role, &got.nickname, &got.originator); err != nil {
			t.Fatalf("read session %s: %v", id, err)
		}
		if got != expected {
			t.Errorf("session %s thread facts = %+v, want %+v", id, got, expected)
		}
	}
}

func TestClaudeSubagentTranscriptsAreTheirOwnSessions(t *testing.T) {
	app, db := testApp(t)
	ctx := context.Background()
	_ = importNativeFixtures(t, app)

	// The parent counts only what it ran itself. Before this rule the two
	// subagent transcripts were merged into it, so its tool-call count meant
	// something different from a Codex session's.
	var parentCalls, parentSubagents int
	if err := db.QueryRowContext(ctx, `
		SELECT tool_call_count, subagent_count FROM session_stats
		WHERE session_id = 'claude_code:cc-hierarchy-fixture'`).Scan(&parentCalls, &parentSubagents); err != nil {
		t.Fatalf("read parent stats: %v", err)
	}
	if parentCalls != 5 {
		t.Errorf("parent tool_call_count = %d, want 5 (its own calls only)", parentCalls)
	}
	if parentSubagents != 2 {
		t.Errorf("parent subagent_count = %d, want 2", parentSubagents)
	}

	var children int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sessions WHERE parent_session_id = 'claude_code:cc-hierarchy-fixture'
		  AND source = 'claude_code'`).Scan(&children); err != nil {
		t.Fatalf("count children: %v", err)
	}
	if children != 2 {
		t.Errorf("child sessions = %d, want 2", children)
	}

	// The subagent's own records stay marked as what they are.
	var sidechain, agents int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*), COUNT(DISTINCT json_extract(payload_json, '$.agent_id'))
		FROM events WHERE session_id = 'claude_code:fixtagent01'
		  AND json_extract(payload_json, '$.sidechain') = 1`).Scan(&sidechain, &agents); err != nil {
		t.Fatalf("count sidechain events: %v", err)
	}
	if sidechain == 0 || agents != 1 {
		t.Errorf("subagent sidechain events = %d, distinct agent ids = %d", sidechain, agents)
	}

	// The parent's Agent call names what each thread was launched for.
	if _, err := app.BackfillSubagentIdentity(ctx); err != nil {
		t.Fatalf("BackfillSubagentIdentity: %v", err)
	}
	for id, want := range map[string][2]string{
		"claude_code:fixtagent01": {"general-purpose", "Run the tests"},
		"claude_code:fixtagent02": {"Explore", "Survey the repo"},
	} {
		var role, nickname sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT agent_role, agent_nickname FROM sessions WHERE id = ?`, id).
			Scan(&role, &nickname); err != nil {
			t.Fatalf("read identity %s: %v", id, err)
		}
		if role.String != want[0] || nickname.String != want[1] {
			t.Errorf("%s identity = (%q, %q), want %v", id, role.String, nickname.String, want)
		}
	}
}

// TestReReadingASubagentTranscriptDoesNotDuplicateItsEvents is the guarantee
// behind the attribution correction: replaying the whole local history through
// the newer parser inserts nothing twice.
func TestReReadingASubagentTranscriptDoesNotDuplicateItsEvents(t *testing.T) {
	app, db := testApp(t)
	ctx := context.Background()
	config := importNativeFixtures(t, app)

	count := func(query string, args ...any) int {
		var value int
		if err := db.QueryRowContext(ctx, query, args...).Scan(&value); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return value
	}
	before := count(`SELECT COUNT(*) FROM events`)

	// Forget the fingerprints so the whole fixture set is read again.
	if _, err := db.ExecContext(ctx, `UPDATE native_files SET size = 0, mtime_ns = 0`); err != nil {
		t.Fatalf("clear fingerprints: %v", err)
	}
	app.nativeFiles = map[string]history.FileStamp{}
	if _, err := app.ImportNativeHistory(ctx, config); err != nil {
		t.Fatalf("second import: %v", err)
	}
	if after := count(`SELECT COUNT(*) FROM events`); after != before {
		t.Fatalf("events = %d after a re-read, was %d", after, before)
	}
	if duplicates := count(`
		SELECT COUNT(*) FROM (
			SELECT session_id, source_event_id FROM events
			WHERE source_event_id IS NOT NULL
			GROUP BY session_id, source_event_id HAVING COUNT(*) > 1)`); duplicates != 0 {
		t.Fatalf("duplicate source event ids = %d", duplicates)
	}
}

func TestNativeImportProjectsCommandsAndFiles(t *testing.T) {
	app, db := testApp(t)
	_ = importNativeFixtures(t, app)
	ctx := context.Background()

	type command struct {
		program  sql.NullString
		exitCode sql.NullInt64
		isError  sql.NullInt64
	}
	commands := make(map[string]command)
	rows, err := db.QueryContext(ctx, `SELECT command, program, exit_code, is_error FROM session_commands`)
	if err != nil {
		t.Fatalf("read commands: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var line string
		var item command
		if err := rows.Scan(&line, &item.program, &item.exitCode, &item.isError); err != nil {
			t.Fatalf("scan command: %v", err)
		}
		commands[line] = item
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate commands: %v", err)
	}

	bash := "cd /fixture/proj && FOO=1 sudo /usr/bin/git status --short | head -3"
	for line, want := range map[string]command{
		bash:                       {program: nullText("git"), exitCode: nullInt(128), isError: nullInt(1)},
		"rg -n 'needle' internal/": {program: nullText("rg"), exitCode: nullInt(127)},
		"python3 scripts/check.py": {program: nullText("python3"), isError: nullInt(1)},
		"ls -la":                   {program: nullText("ls"), exitCode: nullInt(0)},
		"go test ./...":            {program: nullText("go")},
		// The two the user ran themselves; the harness records no outcome for
		// either, so both stay unrecorded rather than becoming a success.
		"git status --short":            {program: nullText("git")},
		"ls -la /fixture/shell/missing": {program: nullText("ls")},
	} {
		got, ok := commands[line]
		if !ok {
			t.Errorf("command %q not projected", line)
			continue
		}
		if got != want {
			t.Errorf("command %q = %+v, want %+v", line, got, want)
		}
	}
	if len(commands) != 7 {
		t.Errorf("projected %d commands, want 7", len(commands))
	}

	files := make(map[string]string)
	fileRows, err := db.QueryContext(ctx, `SELECT path, action FROM session_files ORDER BY path`)
	if err != nil {
		t.Fatalf("read files: %v", err)
	}
	defer fileRows.Close()
	for fileRows.Next() {
		var path, action string
		if err := fileRows.Scan(&path, &action); err != nil {
			t.Fatalf("scan file: %v", err)
		}
		files[path+" "+action] = action
	}
	for _, want := range []string{
		"/fixture/proj/internal/app.go edit", // Edit input, relative path resolved against cwd
		"/fixture/proj/README.md read",
		"/fixture/proj/docs/new.md write",
		"/fixture/proj/docs/old.md delete",
	} {
		if _, ok := files[want]; !ok {
			t.Errorf("file touch %q not projected; got %v", want, files)
		}
	}

	var commandCount, failed, fileCount, isEmpty int
	if err := db.QueryRowContext(ctx, `
		SELECT command_count, failed_command_count, file_count, is_empty
		FROM session_stats WHERE session_id = 'codex:codex-main-fixture'`).
		Scan(&commandCount, &failed, &fileCount, &isEmpty); err != nil {
		t.Fatalf("read codex stats: %v", err)
	}
	if commandCount != 2 || failed != 2 || fileCount != 3 || isEmpty != 0 {
		t.Errorf("codex main stats = commands %d failed %d files %d empty %d, want 2/2/3/0",
			commandCount, failed, fileCount, isEmpty)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT is_empty FROM session_stats WHERE session_id = 'codex:codex-empty-fixture'`).Scan(&isEmpty); err != nil {
		t.Fatalf("read empty session stats: %v", err)
	}
	if isEmpty != 1 {
		t.Errorf("session with no user message and no tool call is_empty = %d, want 1", isEmpty)
	}
}

// A command the user ran with `!` reaches the transcript under the user role
// as <bash-input>, with its output following as <bash-stdout>/<bash-stderr>.
// Nobody typed any of it as a message, so none of it is a user turn — but the
// input line is the only record that the command ran, so it becomes one
// command row under user_shell with no recorded outcome.
func TestUserRunCommandsAreCommandsAndNotTurns(t *testing.T) {
	app, db := testApp(t)
	_ = importNativeFixtures(t, app)
	ctx := context.Background()
	const session = "claude_code:cc-user-shell-fixture"

	var userMessages, toolCalls, commandCount, failed int
	if err := db.QueryRowContext(ctx, `
		SELECT user_message_count, tool_call_count, command_count, failed_command_count
		FROM session_stats WHERE session_id = ?`, session).
		Scan(&userMessages, &toolCalls, &commandCount, &failed); err != nil {
		t.Fatalf("read stats: %v", err)
	}
	// One typed message; two <bash-input> blocks, two output blocks, none of
	// them a turn.
	if userMessages != 1 {
		t.Errorf("user_message_count = %d, want 1", userMessages)
	}
	if toolCalls != 0 {
		t.Errorf("tool_call_count = %d, want 0: nothing called a tool", toolCalls)
	}
	if commandCount != 2 || failed != 0 {
		t.Errorf("command_count/failed = %d/%d, want 2/0", commandCount, failed)
	}

	rows, err := db.QueryContext(ctx, `
		SELECT tool_name, program, command, exit_code, is_error
		FROM session_commands WHERE session_id = ? ORDER BY command`, session)
	if err != nil {
		t.Fatalf("read commands: %v", err)
	}
	defer rows.Close()
	type row struct {
		tool, program, command string
		exitCode               sql.NullInt64
		isError                sql.NullInt64
	}
	var got []row
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.tool, &item.program, &item.command, &item.exitCode, &item.isError); err != nil {
			t.Fatalf("scan command: %v", err)
		}
		got = append(got, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate commands: %v", err)
	}
	want := []row{
		{tool: "user_shell", program: "git", command: "git status --short"},
		{tool: "user_shell", program: "ls", command: "ls -la /fixture/shell/missing"},
	}
	if len(got) != len(want) {
		t.Fatalf("projected %d user commands, want %d: %+v", len(got), len(want), got)
	}
	for index, item := range got {
		if item != want[index] {
			t.Errorf("command %d = %+v, want %+v", index, item, want[index])
		}
	}

	// The output blocks are not commands and carry no exit code of their own.
	var outputRows int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM session_commands WHERE session_id = ? AND command LIKE '<bash-%'`, session).
		Scan(&outputRows); err != nil {
		t.Fatalf("count output rows: %v", err)
	}
	if outputRows != 0 {
		t.Errorf("projected %d rows from the output blocks, want 0", outputRows)
	}
}

// TestParseRecordsCodexExitStatusLines covers the wrapper status lines Codex
// prints instead of an explicit field. They have to land on the event payload
// at parse time, because that is what the friction projection reads.
func TestParseRecordsCodexExitStatusLines(t *testing.T) {
	app, db := testApp(t)
	_ = importNativeFixtures(t, app)
	ctx := context.Background()

	var exitCode sql.NullInt64
	if err := db.QueryRowContext(ctx, `
		SELECT json_extract(payload_json, '$.exit_code') FROM events
		WHERE session_id = 'codex:codex-main-fixture' AND event_type = 'transcript_tool_result'
		  AND json_extract(payload_json, '$.tool_output') LIKE '%Process exited with code 127%'`).Scan(&exitCode); err != nil {
		t.Fatalf("read process-exit result: %v", err)
	}
	if !exitCode.Valid || exitCode.Int64 != 127 {
		t.Errorf("exit code from a \"Process exited with code\" line = %v, want 127", exitCode)
	}

	var isError sql.NullInt64
	if err := db.QueryRowContext(ctx, `
		SELECT json_extract(payload_json, '$.is_error') FROM events
		WHERE session_id = 'codex:codex-main-fixture' AND event_type = 'transcript_tool_result'
		  AND json_extract(payload_json, '$.tool_output') LIKE 'Script failed%'`).Scan(&isError); err != nil {
		t.Fatalf("read script-failed result: %v", err)
	}
	if !isError.Valid || isError.Int64 != 1 {
		t.Errorf("is_error from a \"Script failed\" output = %v, want true", isError)
	}

	var friction int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM friction_records WHERE session_id = 'codex:codex-main-fixture'`).Scan(&friction); err != nil {
		t.Fatalf("count codex friction: %v", err)
	}
	if friction != 2 {
		t.Errorf("codex friction rows = %d, want 2 (the non-zero exit and the failed script)", friction)
	}
}

// TestBackfillFromOlderSchema proves the recovery path a real database needs:
// the sessions were ingested while the hierarchy columns and the projections
// did not exist, and the transcripts are never replayed because their
// fingerprints did not change.
func TestBackfillFromOlderSchema(t *testing.T) {
	app, db := testApp(t)
	config := importNativeFixtures(t, app)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `
		UPDATE sessions SET thread_kind = NULL, parent_session_id = NULL, agent_role = NULL,
			agent_nickname = NULL, originator = NULL`); err != nil {
		t.Fatalf("clear thread facts: %v", err)
	}
	for _, statement := range []string{
		`DELETE FROM session_commands`, `DELETE FROM session_files`,
		`UPDATE session_stats SET projected_at = NULL, command_count = 0, failed_command_count = 0,
			file_count = 0, subagent_count = 0`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("reset projection (%s): %v", statement, err)
		}
	}

	// A second import must skip every file: the fingerprints are unchanged.
	second, err := app.ImportNativeHistory(ctx, config)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if second.FilesRead != 0 {
		t.Fatalf("second import read %d files, want 0", second.FilesRead)
	}

	if _, err := app.RecomputeMissingSessionStats(ctx); err != nil {
		t.Fatalf("startup recompute: %v", err)
	}

	var kind, parent sql.NullString
	if err := db.QueryRowContext(ctx, `
		SELECT thread_kind, parent_session_id FROM sessions WHERE id = 'codex:codex-subagent-fixture'`).
		Scan(&kind, &parent); err != nil {
		t.Fatalf("read backfilled session: %v", err)
	}
	if kind.String != "subagent" || parent.String != "codex:codex-main-fixture" {
		t.Errorf("backfilled thread = %q/%q, want subagent/codex:codex-main-fixture", kind.String, parent.String)
	}

	var commands, files int
	if err := db.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM session_commands), (SELECT COUNT(*) FROM session_files)`).
		Scan(&commands, &files); err != nil {
		t.Fatalf("count backfilled projections: %v", err)
	}
	// The second Claude subagent fixture adds one more command, and the
	// user-shell fixture two the user ran themselves.
	if commands != 8 || files != 5 {
		t.Errorf("backfilled %d commands and %d files, want 8 and 5", commands, files)
	}
}

func nullText(value string) sql.NullString { return sql.NullString{String: value, Valid: true} }
func nullInt(value int64) sql.NullInt64    { return sql.NullInt64{Int64: value, Valid: true} }

// TestSubagentIdentityComesFromTheMetaFileWhenItIsThere is the harness's own
// record of what a thread was launched for: kind, task, and the parent tool
// call that started it.
func TestSubagentIdentityComesFromTheMetaFileWhenItIsThere(t *testing.T) {
	app, db := testApp(t)
	ctx := context.Background()
	claudeRoot, codexRoot := nativeFixtureRoots(t)
	meta := filepath.Join(claudeRoot, "subagents", "agent-fixtagent01.meta.json")
	if err := os.WriteFile(meta, []byte(`{"agentType":"fork","isFork":true,`+
		`"description":"评估 block_size 迁移","toolUseId":"toolu_agent_1","spawnDepth":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ImportNativeHistory(ctx, history.Config{
		ClaudeRoot: claudeRoot, CodexRoot: codexRoot, IncludeSubagents: true}); err != nil {
		t.Fatalf("import: %v", err)
	}
	var role, nickname, title sql.NullString
	if err := db.QueryRowContext(ctx, `
		SELECT agent_role, agent_nickname, title FROM sessions WHERE id = 'claude_code:fixtagent01'`).
		Scan(&role, &nickname, &title); err != nil {
		t.Fatalf("read subagent: %v", err)
	}
	if role.String != "fork" || nickname.String != "评估 block_size 迁移" {
		t.Errorf("identity = (%q, %q), want (fork, 评估 block_size 迁移)", role.String, nickname.String)
	}
	// The launch description is what the thread is called, not the boilerplate
	// the harness prepends to its first user message.
	if title.String != "评估 block_size 迁移" {
		t.Errorf("title = %q, want the launch description", title.String)
	}
}

// TestClaudeAssistantMessagesCarryTheirOwnUsage pins the per-message token
// record (ADR-22): each assistant message's payload carries the usage the
// source wrote on it — the final values, since a streaming message's usage
// grows across its follow-up records — and the session totals still add up.
func TestClaudeAssistantMessagesCarryTheirOwnUsage(t *testing.T) {
	app, db := testApp(t)
	_ = importNativeFixtures(t, app)
	ctx := context.Background()
	const session = "claude_code:cc-message-usage-fixture"

	type usage struct {
		Total     int64
		Reasoning sql.NullInt64
	}
	byMessage := map[string]usage{}
	rows, err := db.QueryContext(ctx, `
		SELECT json_extract(payload_json, '$.message_id'),
		       json_extract(payload_json, '$.usage.total_tokens'),
		       json_extract(payload_json, '$.usage.reasoning_tokens')
		FROM events
		WHERE session_id = ? AND event_type = 'transcript_message'
		  AND json_extract(payload_json, '$.role') = 'assistant'`, session)
	if err != nil {
		t.Fatalf("read messages: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var item usage
		if err := rows.Scan(&id, &item.Total, &item.Reasoning); err != nil {
			t.Fatalf("scan message: %v", err)
		}
		byMessage[id] = item
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	// The payload's message_id is the record uuid plus the block index, so the
	// two messages are keyed "a1-0" and "a2-0". msg_1: 100 + 4000 + 50 + 200 =
	// 4350. msg_2: 150 + 5000 + 0 + 300 = 5450, with 80 reasoning tokens. A
	// message without usage would be absent from this map, not zero.
	if len(byMessage) != 2 {
		t.Fatalf("assistant messages with usage = %d, want 2 (%v)", len(byMessage), byMessage)
	}
	totals := map[int64]bool{}
	reasoning := int64(0)
	for _, item := range byMessage {
		totals[item.Total] = true
		if item.Reasoning.Valid && item.Reasoning.Int64 > reasoning {
			reasoning = item.Reasoning.Int64
		}
	}
	if !totals[4350] || !totals[5450] {
		t.Errorf("message totals = %v, want {4350 5450}", totals)
	}
	if reasoning != 80 {
		t.Errorf("reasoning = %d, want 80", reasoning)
	}

	var input, cached, cacheWrite, output, total int64
	if err := db.QueryRowContext(ctx, `
		SELECT input_tokens, cached_input_tokens, cache_write_tokens, output_tokens, total_tokens
		FROM session_usage WHERE session_id = ?`, session).
		Scan(&input, &cached, &cacheWrite, &output, &total); err != nil {
		t.Fatalf("read session usage: %v", err)
	}
	// The session totals are the messages summed: 250 input, 9000 cache read,
	// 50 cache write, 500 output.
	if input != 250 || cached != 9000 || cacheWrite != 50 || output != 500 || total != 9800 {
		t.Errorf("session usage = %d/%d/%d/%d/%d, want 250/9000/50/500/9800",
			input, cached, cacheWrite, output, total)
	}
}

// TestCodexTurnTokensAreAttributedBySubtraction pins the Codex half of the
// interrupt cost (ADR-23): each real user message carries what the harness's
// running total says its whole turn cost — turn 1 closed by the abort,
// turn 2 closed by the next user message, turn 3 by the end of the file. A
// session with no token_count at all would carry no turn_tokens, not zero.
func TestCodexTurnTokensAreAttributedBySubtraction(t *testing.T) {
	app, db := testApp(t)
	_ = importNativeFixtures(t, app)
	ctx := context.Background()
	const session = "codex:codex-turn-tokens-fixture"

	type turn struct {
		text string
		cost sql.NullInt64
	}
	byText := map[string]turn{}
	rows, err := db.QueryContext(ctx, `
		SELECT json_extract(payload_json, '$.text'),
		       json_extract(payload_json, '$.turn_tokens')
		FROM events
		WHERE session_id = ? AND event_type = 'transcript_message'
		  AND json_extract(payload_json, '$.role') = 'user'`, session)
	if err != nil {
		t.Fatalf("read user messages: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item turn
		var text string
		if err := rows.Scan(&text, &item.cost); err != nil {
			t.Fatalf("scan: %v", err)
		}
		byText[text] = item
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	// Turn 1: baseline 0, closed by the abort at total 2200 → 2200.
	if got := byText["第一轮：帮我跑一下测试"].cost; !got.Valid || got.Int64 != 2200 {
		t.Errorf("turn 1 cost = %v, want 2200", got)
	}
	// Turn 2: baseline 2200, closed by the next user message — the last
	// token_count before that is 3400 → 1200.
	if got := byText["第二轮：继续"].cost; !got.Valid || got.Int64 != 1200 {
		t.Errorf("turn 2 cost = %v, want 1200", got)
	}
	// Turn 3: baseline 3400, still open at end of file, closed at 5200 → 1800.
	if got := byText["第三轮：再继续"].cost; !got.Valid || got.Int64 != 1800 {
		t.Errorf("turn 3 cost = %v, want 1800", got)
	}
}
