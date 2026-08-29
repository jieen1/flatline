package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"flatline/internal/canonical"
	"flatline/internal/friction"
)

const (
	maxCommandText = 512
	patchHeader    = "*** Begin Patch"
)

// ProjectionVersion changes whenever the rules behind session_commands,
// session_files or tool_call_stats change — a new tool-name match, a new
// failure rule — or a counting rule the projection re-derives, such as which
// blocks under the user role are not user turns. A session stamped with an
// older version is projected again on the next start; a session stamped with
// this one is left alone.
const ProjectionVersion = "projection/7"

type commandRow struct {
	eventID int64
	// ordinal is the position of this command inside its own tool call. A Codex
	// exec script can carry several commands in one call, and until this column
	// existed the unique key (session_id, event_id) meant every command after
	// the first was silently dropped.
	ordinal  int
	toolName string
	program  string
	command  string
	exitCode *int
	isError  *bool
	// expectedExit marks an exit code the program uses as an answer rather
	// than as a failure: `rg` exiting 1 means nothing matched. The row is kept
	// — it happened — but every failure count leaves it out.
	expectedExit bool
	occurred     sql.NullString
}

type fileRow struct {
	eventID  int64
	path     string
	action   string
	toolName string
	occurred sql.NullString
}

type toolOutcome struct {
	exitCode *int
	isError  *bool
}

// RecomputeSessionProjections rebuilds event_pairs (its id-matched half),
// session_commands, session_files and tool_call_stats for one session, and
// refreshes the session_stats columns derived from them. It reads only the
// bounded tool payloads already in the event store; no source file is opened.
func (s *Store) RecomputeSessionProjections(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("eventstore: session id is required")
	}
	var cwd, source sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT cwd, source FROM sessions WHERE id = ?`, sessionID).Scan(&cwd, &source); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("eventstore: project session %s: session not found", sessionID)
		}
		return fmt.Errorf("eventstore: project session %s: %w", sessionID, err)
	}
	projected, err := s.projectToolEvents(ctx, sessionID, cwd.String)
	if err != nil {
		return err
	}
	if err := s.writeSessionProjections(ctx, sessionID, source.String, projected); err != nil {
		return err
	}
	// Re-projecting a session re-derives everything derived for it, the event
	// counts included: a counting rule can change without a single new event.
	if err := s.RecomputeSessionStats(ctx, sessionID); err != nil {
		return err
	}
	return s.refreshProjectedStats(ctx, sessionID, projected.agents)
}

// RecomputeAllProjections rebuilds every session's command and file
// projection. It is the ADR-10 "recompute everything" entry point for these
// tables.
func (s *Store) RecomputeAllProjections(ctx context.Context) (int, error) {
	return s.recomputeProjectionsFor(ctx, `SELECT id FROM sessions ORDER BY id`)
}

// RecomputeMissingProjections rebuilds only sessions that have never been
// projected. A session with no commands and no files is still projected: the
// projected_at stamp is what separates "nothing to record" from "not looked
// at yet".
func (s *Store) RecomputeMissingProjections(ctx context.Context) (int, error) {
	return s.recomputeProjectionsFor(ctx, `
		SELECT s.id FROM sessions s
		LEFT JOIN session_stats st ON st.session_id = s.id
		WHERE st.session_id IS NULL OR st.projected_at IS NULL
		   OR st.projection_version IS NULL OR st.projection_version <> ?
		ORDER BY s.id`, ProjectionVersion)
}

func (s *Store) recomputeProjectionsFor(ctx context.Context, query string, args ...any) (int, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("eventstore: list sessions to project: %w", err)
	}
	var pending []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("eventstore: scan session to project: %w", err)
		}
		pending = append(pending, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("eventstore: iterate sessions to project: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("eventstore: close sessions to project: %w", err)
	}
	for _, id := range pending {
		if err := s.RecomputeSessionProjections(ctx, id); err != nil {
			return 0, err
		}
	}
	return len(pending), nil
}

// sessionProjection is everything one pass over a session's tool events
// produces: the commands, the file touches, the per-tool counts, and the
// distinct sidechain agent ids recorded on those events.
type sessionProjection struct {
	commands  []commandRow
	files     []fileRow
	toolStats []toolStatRow
	idPairs   []pairRow
	agents    map[string]struct{}
}

// toolStatRow is one tool's usage inside one session. knownOutcomes is the
// denominator failures is counted out of: a call whose result the source never
// recorded is neither a success nor a failure.
type toolStatRow struct {
	toolName      string
	calls         int
	knownOutcomes int
	failures      int
}

// callCommand is the last command line one tool call ran, kept so the exit
// code of its result can be read with that program's own conventions. It is
// the last one because that is the statement whose status a shell reports.
type callCommand struct {
	command string
	// ordinal is that command's position in the call, so the recorded outcome
	// lands on the row it actually describes.
	ordinal int
}

// projectToolEvents reads one session's tool calls and results in a single
// pass and matches each result to its call on the id both recorded. Pairs the
// ids cannot establish were recovered earlier by re-reading the transcript;
// they are loaded from event_pairs and win over the id match.
func (s *Store) projectToolEvents(ctx context.Context, sessionID, cwd string) (sessionProjection, error) {
	reparsed, err := s.reparsedPairs(ctx, sessionID)
	if err != nil {
		return sessionProjection{}, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, event_type, COALESCE(payload_json, ''), occurred_at
		FROM events
		WHERE session_id = ?
		  AND (event_type IN (?, ?)
		    OR (event_type = ? AND json_extract(payload_json, '$.text') LIKE ?))
		ORDER BY id`, sessionID, canonical.EventTypeTranscriptToolCall, canonical.EventTypeTranscriptResult,
		canonical.EventTypeTranscriptMessage, canonical.UserShellTextLike())
	if err != nil {
		return sessionProjection{}, fmt.Errorf("eventstore: read tool events %s: %w", sessionID, err)
	}
	defer rows.Close()

	out := sessionProjection{files: make([]fileRow, 0), idPairs: make([]pairRow, 0), agents: make(map[string]struct{})}
	var pendingCommands []commandRow
	var callOrder []int64
	callNames := make(map[int64]string)
	callIDs := make(map[string]int64)
	results := make(map[int64]map[string]any)
	resultLinks := make(map[int64]string)
	callCommands := make(map[int64]callCommand)

	for rows.Next() {
		var eventID int64
		var eventType, encoded string
		var occurred sql.NullString
		if err := rows.Scan(&eventID, &eventType, &encoded, &occurred); err != nil {
			return sessionProjection{}, fmt.Errorf("eventstore: scan tool event: %w", err)
		}
		var payload map[string]any
		if encoded != "" {
			if err := json.Unmarshal([]byte(encoded), &payload); err != nil {
				continue
			}
		}
		if agent, ok := payload["agent_id"].(string); ok && strings.TrimSpace(agent) != "" {
			out.agents[strings.TrimSpace(agent)] = struct{}{}
		}
		if eventType == canonical.EventTypeTranscriptResult {
			results[eventID] = payload
			resultLinks[eventID] = linkID(payload)
			continue
		}
		if eventType == canonical.EventTypeTranscriptMessage {
			// A command the user ran themselves. Nothing called a tool, so it
			// is not counted as one and never reaches the per-tool stats; the
			// harness records no exit code for it either, which is why the row
			// carries none and adds nothing to any known-outcome denominator.
			text, _ := payload["text"].(string)
			if command, ok := canonical.UserShellCommand(text); ok {
				pendingCommands = append(pendingCommands, commandRow{eventID: eventID, toolName: canonical.UserShellTool,
					program: friction.Program(command), command: boundRunes(command, maxCommandText), occurred: occurred})
			}
			continue
		}
		toolName, _ := payload["tool_name"].(string)
		callOrder = append(callOrder, eventID)
		callNames[eventID] = strings.TrimSpace(toolName)
		if id := linkID(payload); id != "" {
			if _, seen := callIDs[id]; !seen {
				callIDs[id] = eventID
			}
		}
		input, _ := payload["tool_input"].(string)
		if toolName == "" || input == "" {
			continue
		}
		for ordinal, command := range extractCommands(toolName, input) {
			callCommands[eventID] = callCommand{command: command, ordinal: ordinal}
			pendingCommands = append(pendingCommands, commandRow{eventID: eventID, ordinal: ordinal, toolName: toolName,
				program: friction.Program(command), command: boundRunes(command, maxCommandText), occurred: occurred})
		}
		for _, touch := range extractFiles(toolName, input) {
			out.files = append(out.files, fileRow{eventID: eventID, path: absolutePath(touch.path, cwd),
				action: touch.action, toolName: toolName, occurred: occurred})
		}
	}
	if err := rows.Err(); err != nil {
		return sessionProjection{}, fmt.Errorf("eventstore: iterate tool events: %w", err)
	}

	outcomes := make(map[int64]toolOutcome, len(results))
	for resultEventID, payload := range results {
		callEventID, matched := reparsed[resultEventID]
		if !matched {
			callEventID, matched = callIDs[resultLinks[resultEventID]]
			if !matched || resultLinks[resultEventID] == "" {
				continue
			}
			out.idPairs = append(out.idPairs, pairRow{resultEventID: resultEventID,
				callEventID: callEventID, toolName: callNames[callEventID]})
		}
		outcomes[callEventID] = resultOutcome(payload)
	}
	sort.Slice(out.idPairs, func(i, j int) bool { return out.idPairs[i].resultEventID < out.idPairs[j].resultEventID })

	out.commands = make([]commandRow, 0, len(pendingCommands))
	for _, row := range pendingCommands {
		// One tool call reports one status, and it is the last statement's. It
		// is recorded on that statement's row; the earlier commands of the same
		// call have no recorded outcome, which is not the same as succeeding.
		if outcome, ok := outcomes[row.eventID]; ok && callCommands[row.eventID].ordinal == row.ordinal {
			row.exitCode, row.isError = outcome.exitCode, outcome.isError
			row.expectedExit = expectedOutcome(callCommands[row.eventID].command, outcome)
		}
		out.commands = append(out.commands, row)
	}
	out.toolStats = aggregateToolStats(callOrder, callNames, outcomes, callCommands)
	return out, nil
}

// expectedOutcome reports whether a recorded nonzero exit is one the program
// documents as an answer. is_error is the harness's own verdict and is never
// overridden here.
func expectedOutcome(command string, outcome toolOutcome) bool {
	if outcome.isError != nil && *outcome.isError {
		return false
	}
	return outcome.exitCode != nil && friction.ExpectedExit(command, *outcome.exitCode)
}

// aggregateToolStats counts one session's calls per tool. A tool the source
// never named is counted under the explicit unrecorded key rather than dropped.
func aggregateToolStats(callOrder []int64, callNames map[int64]string, outcomes map[int64]toolOutcome, commands map[int64]callCommand) []toolStatRow {
	index := make(map[string]int)
	stats := make([]toolStatRow, 0)
	for _, eventID := range callOrder {
		name := callNames[eventID]
		if name == "" {
			name = unrecordedToolName
		}
		position, ok := index[name]
		if !ok {
			position = len(stats)
			index[name] = position
			stats = append(stats, toolStatRow{toolName: name})
		}
		stats[position].calls++
		outcome, paired := outcomes[eventID]
		if !paired || (outcome.exitCode == nil && outcome.isError == nil) {
			continue
		}
		stats[position].knownOutcomes++
		if expectedOutcome(commands[eventID].command, outcome) {
			continue
		}
		if (outcome.isError != nil && *outcome.isError) || (outcome.exitCode != nil && *outcome.exitCode != 0) {
			stats[position].failures++
		}
	}
	return stats
}

func (s *Store) writeSessionProjections(ctx context.Context, sessionID, harness string, projected sessionProjection) error {
	commands, files := projected.commands, projected.files
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("eventstore: begin projection write: %w", err)
	}
	rollback := func(err error) error {
		_ = tx.Rollback()
		return err
	}
	for _, table := range []string{"session_commands", "session_files", "tool_call_stats"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE session_id = ?`, sessionID); err != nil {
			return rollback(fmt.Errorf("eventstore: clear %s %s: %w", table, sessionID, err))
		}
	}
	if err := writeSessionIDPairs(ctx, tx, sessionID, projected.idPairs); err != nil {
		return rollback(err)
	}
	for _, row := range projected.toolStats {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO tool_call_stats (session_id, tool_name, harness, calls, known_outcomes, failures)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (session_id, tool_name) DO NOTHING`,
			sessionID, row.toolName, harness, row.calls, row.knownOutcomes, row.failures); err != nil {
			return rollback(fmt.Errorf("eventstore: insert tool stat %s/%s: %w", sessionID, row.toolName, err))
		}
	}
	for _, row := range commands {
		var isError any
		if row.isError != nil {
			isError = boolInt(*row.isError)
		}
		var exitCode any
		if row.exitCode != nil {
			exitCode = *row.exitCode
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO session_commands (session_id, event_id, ordinal, tool_name, program, command, exit_code, is_error, expected_exit, occurred_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (session_id, event_id, ordinal) DO NOTHING`,
			sessionID, row.eventID, row.ordinal, row.toolName, nullableString(row.program), row.command,
			exitCode, isError, boolInt(row.expectedExit), nullableSQLString(row.occurred)); err != nil {
			return rollback(fmt.Errorf("eventstore: insert command %s/%d: %w", sessionID, row.eventID, err))
		}
	}
	for _, row := range files {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO session_files (session_id, event_id, path, action, tool_name, occurred_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (session_id, event_id, path, action) DO NOTHING`,
			sessionID, row.eventID, row.path, row.action, row.toolName, nullableSQLString(row.occurred)); err != nil {
			return rollback(fmt.Errorf("eventstore: insert file %s/%d: %w", sessionID, row.eventID, err))
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("eventstore: commit projection write: %w", err)
	}
	return nil
}

// refreshProjectedStats rewrites the session_stats columns that depend on the
// two projections. subagent_count counts child threads for a source that spawns
// them as their own sessions (Codex) and distinct sidechain agents for a source
// that merges them into the parent transcript (Claude Code); the two are
// mutually exclusive per session, so the sum is the number of agents observed.
func (s *Store) refreshProjectedStats(ctx context.Context, sessionID string, agents map[string]struct{}) error {
	subagents, err := s.subagentCount(ctx, sessionID, agents)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE session_stats SET
			subagent_count = ?,
			command_count = (SELECT COUNT(*) FROM session_commands WHERE session_id = ?),
			failed_command_count = (SELECT COUNT(*) FROM session_commands
				WHERE session_id = ? AND expected_exit = 0
				  AND ((exit_code IS NOT NULL AND exit_code <> 0) OR is_error = 1)),
			file_count = (SELECT COUNT(DISTINCT path) FROM session_files WHERE session_id = ?),
			is_empty = CASE WHEN transcript_count = 0 OR (user_message_count = 0 AND tool_call_count = 0) THEN 1 ELSE 0 END,
			projected_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
			projection_version = ?
		WHERE session_id = ?`,
		subagents, sessionID, sessionID, sessionID, ProjectionVersion, sessionID); err != nil {
		return fmt.Errorf("eventstore: update projected stats %s: %w", sessionID, err)
	}
	// A child thread ingested after its parent would leave the parent's count
	// stale, so the parent's one derived number is refreshed here too.
	if _, err := s.db.ExecContext(ctx, `
		UPDATE session_stats SET subagent_count = (
			SELECT COUNT(*) FROM sessions c WHERE c.parent_session_id = session_stats.session_id)
		WHERE session_id = (SELECT parent_session_id FROM sessions WHERE id = ?)`, sessionID); err != nil {
		return fmt.Errorf("eventstore: update parent subagent count %s: %w", sessionID, err)
	}
	return nil
}

func (s *Store) subagentCount(ctx context.Context, sessionID string, agents map[string]struct{}) (int, error) {
	var children int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE parent_session_id = ?`, sessionID).Scan(&children); err != nil {
		return 0, fmt.Errorf("eventstore: count child sessions %s: %w", sessionID, err)
	}
	// A merged sidechain transcript is recorded as its own native file even
	// when its events predate the agent_id payload, so the file names are a
	// second, equally explicit record of the same agents.
	rows, err := s.db.QueryContext(ctx, `SELECT path FROM native_files WHERE session_id = ? AND instr(path, '/subagents/') > 0`, sessionID)
	if err != nil {
		return 0, fmt.Errorf("eventstore: list subagent files %s: %w", sessionID, err)
	}
	defer rows.Close()
	seen := make(map[string]struct{}, len(agents))
	for agent := range agents {
		seen[agent] = struct{}{}
	}
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return 0, fmt.Errorf("eventstore: scan subagent file: %w", err)
		}
		name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		name = strings.TrimPrefix(name, "agent-")
		if name != "" {
			seen[name] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("eventstore: iterate subagent files: %w", err)
	}
	return children + len(seen), nil
}

func resultOutcome(payload map[string]any) toolOutcome {
	var explicitIsError *bool
	if value, ok := payloadBool(payload, "is_error"); ok {
		explicitIsError = &value
	}
	var explicitExitCode *int
	if value, ok := payloadInt(payload, "exit_code"); ok {
		explicitExitCode = &value
	}
	output, _ := payload["tool_output"].(string)
	isError, exitCode := canonical.NormalizeToolFailure(output, explicitIsError, explicitExitCode)
	return toolOutcome{exitCode: exitCode, isError: isError}
}

// extractCommands returns the shell commands a tool call recorded, in the order
// the call runs them, and nothing when the tool does not run one. Only the
// field the harness itself names as the command is read.
func extractCommands(toolName, input string) []string {
	// Harnesses disagree on capitalisation for the same tool: Claude Code
	// writes Bash, opencode and dsh write bash. The stored tool name keeps the
	// source's own spelling; only the match here is case-insensitive.
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "bash", "shell":
		return oneCommand(jsonStringField(input, "command"))
	case "exec_command":
		return oneCommand(jsonStringField(input, "cmd"))
	case "exec":
		if value := oneCommand(jsonStringField(input, "cmd")); len(value) > 0 {
			return value
		}
		return scriptCommands(input)
	default:
		// write_stdin and wait continue an already-recorded command; they do
		// not start one.
		return nil
	}
}

func oneCommand(value string) []string {
	if value = strings.TrimSpace(value); value == "" {
		return nil
	}
	return []string{value}
}

func jsonStringField(input, field string) string {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(input), &decoded); err != nil {
		return ""
	}
	value, _ := decoded[field].(string)
	return value
}

// scriptCommands reads the commands out of a Codex exec script: either a
// `cmd:` property, or every `["label", "cmd", cwd]` tuple of a
// `const cmds = [...]` array, in the order the script runs them.
func scriptCommands(input string) []string {
	if value, ok := literalAfter(input, "cmd"); ok {
		return oneCommand(value)
	}
	var out []string
	for i := 0; i < len(input); i++ {
		if input[i] != '[' {
			continue
		}
		rest := skipSpace(input, i+1)
		if rest >= len(input) || !isQuote(input[rest]) {
			continue
		}
		_, after, ok := readLiteral(input, rest)
		if !ok {
			continue
		}
		after = skipSpace(input, after)
		if after >= len(input) || input[after] != ',' {
			continue
		}
		after = skipSpace(input, after+1)
		if after >= len(input) || !isQuote(input[after]) {
			continue
		}
		value, end, ok := readLiteral(input, after)
		if !ok {
			continue
		}
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
		i = end - 1
	}
	return out
}

// literalAfter finds `key` used as an object property and returns the string
// literal assigned to it.
func literalAfter(input, key string) (string, bool) {
	for i := 0; i+len(key) < len(input); i++ {
		if !strings.HasPrefix(input[i:], key) {
			continue
		}
		if i > 0 && isIdentByte(input[i-1]) {
			continue
		}
		next := skipSpace(input, i+len(key))
		if next >= len(input) || (input[next] != ':' && input[next] != '=') {
			continue
		}
		next = skipSpace(input, next+1)
		if next >= len(input) || !isQuote(input[next]) {
			continue
		}
		if value, _, ok := readLiteral(input, next); ok {
			return value, true
		}
	}
	return "", false
}

func readLiteral(input string, start int) (string, int, bool) {
	quote := input[start]
	var builder strings.Builder
	for i := start + 1; i < len(input); i++ {
		switch input[i] {
		case '\\':
			if i+1 >= len(input) {
				return "", 0, false
			}
			i++
			switch input[i] {
			case 'n':
				builder.WriteByte('\n')
			case 't':
				builder.WriteByte('\t')
			case 'r':
				builder.WriteByte('\r')
			default:
				builder.WriteByte(input[i])
			}
		case quote:
			return builder.String(), i + 1, true
		default:
			builder.WriteByte(input[i])
		}
	}
	return "", 0, false
}

func isQuote(b byte) bool { return b == '"' || b == '\'' || b == '`' }

func isIdentByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func skipSpace(input string, i int) int {
	for i < len(input) && (input[i] == ' ' || input[i] == '\t' || input[i] == '\n' || input[i] == '\r') {
		i++
	}
	return i
}

type fileTouch struct {
	path   string
	action string
}

// extractFiles returns the files a tool call names. Claude Code records the
// path as a tool input field; Codex records it in the patch headers, which can
// appear either as an apply_patch input or inside an exec script that calls it.
func extractFiles(toolName, input string) []fileTouch {
	if strings.Contains(input, patchHeader) {
		return patchTouches(input)
	}
	var fields []string
	var action string
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "read":
		fields, action = []string{"file_path", "filePath", "path"}, "read"
	case "edit", "multiedit":
		fields, action = []string{"file_path", "filePath", "path"}, "edit"
	case "write":
		fields, action = []string{"file_path", "filePath", "path"}, "write"
	case "notebookedit":
		fields, action = []string{"notebook_path", "notebookPath"}, "edit"
	default:
		return nil
	}
	for _, field := range fields {
		if path := strings.TrimSpace(jsonStringField(input, field)); path != "" {
			return []fileTouch{{path: path, action: action}}
		}
	}
	return nil
}

var patchActions = map[string]string{
	"*** Update File:": "edit",
	"*** Add File:":    "write",
	"*** Delete File:": "delete",
}

func patchTouches(input string) []fileTouch {
	out := make([]fileTouch, 0, 1)
	for _, line := range strings.Split(input, "\n") {
		line = strings.TrimSpace(line)
		for prefix, action := range patchActions {
			if !strings.HasPrefix(line, prefix) {
				continue
			}
			path := strings.TrimSpace(strings.TrimPrefix(line, prefix))
			path = strings.Trim(path, `"'`)
			if path != "" {
				out = append(out, fileTouch{path: path, action: action})
			}
		}
	}
	return out
}

// absolutePath resolves a relative path against the session's working
// directory. A path that cannot be resolved is kept exactly as recorded.
func absolutePath(path, cwd string) string {
	if filepath.IsAbs(path) || strings.TrimSpace(cwd) == "" {
		return path
	}
	return filepath.Join(cwd, path)
}

func boundRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func nullableSQLString(value sql.NullString) any {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	return value.String
}
