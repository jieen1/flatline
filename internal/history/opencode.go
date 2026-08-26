package history

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"flatline/internal/adapters"
	"flatline/internal/eventstore"
	"flatline/internal/ingest"
)

// opencode stores its history in a single SQLite database that the running
// harness writes to. This reader opens it read-only with a busy timeout and
// never writes: mode=ro makes an accidental write fail at the driver rather
// than corrupt a live session (AGENTS.md §2).
const openCodeDSN = "?mode=ro&_pragma=busy_timeout(5000)"

type openCodeRow struct {
	ID               string
	ParentID         sql.NullString
	Directory        string
	Title            string
	Version          string
	Agent            sql.NullString
	Model            sql.NullString
	TokensInput      int64
	TokensOutput     int64
	TokensReasoning  int64
	TokensCacheRead  int64
	TokensCacheWrite int64
	Cost             float64
	SummaryAdditions sql.NullInt64
	SummaryDeletions sql.NullInt64
	SummaryFiles     sql.NullInt64
	TimeCreated      int64
	TimeUpdated      int64
}

type openCodeModel struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
}

type openCodeMessage struct {
	Role   string `json:"role"`
	Agent  string `json:"agent"`
	Finish string `json:"finish"`
	Time   struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed"`
	} `json:"time"`
}

type openCodePart struct {
	Type   string           `json:"type"`
	Text   string           `json:"text"`
	Tool   string           `json:"tool"`
	CallID string           `json:"callID"`
	State  openCodeState    `json:"state"`
	Time   openCodePartTime `json:"time"`
}

type openCodePartTime struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

type openCodeState struct {
	Status   string          `json:"status"`
	Input    json.RawMessage `json:"input"`
	Output   string          `json:"output"`
	Error    string          `json:"error"`
	Metadata struct {
		Exit        *int  `json:"exit"`
		Interrupted *bool `json:"interrupted"`
	} `json:"metadata"`
	Time openCodePartTime `json:"time"`
}

// readOpenCode reads every session row in the opencode database. It returns
// one normalized session per row that changed since the recorded fingerprint.
func readOpenCode(dbPath string, index assetIndex, projectRoot string, known map[string]FileStamp, report *Report, notify func()) ([]Session, error) {
	db, err := sql.Open("sqlite", "file:"+dbPath+openCodeDSN)
	if err != nil {
		return nil, fmt.Errorf("history: open opencode database: %w", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("history: open opencode database: %w", err)
	}

	rows, err := db.Query(`
		SELECT id, parent_id, directory, title, version, agent, model,
		       tokens_input, tokens_output, tokens_reasoning, tokens_cache_read, tokens_cache_write,
		       cost, summary_additions, summary_deletions, summary_files, time_created, time_updated
		FROM session ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("history: list opencode sessions: %w", err)
	}
	var sessions []openCodeRow
	for rows.Next() {
		var row openCodeRow
		if err := rows.Scan(&row.ID, &row.ParentID, &row.Directory, &row.Title, &row.Version,
			&row.Agent, &row.Model, &row.TokensInput, &row.TokensOutput, &row.TokensReasoning,
			&row.TokensCacheRead, &row.TokensCacheWrite, &row.Cost, &row.SummaryAdditions,
			&row.SummaryDeletions, &row.SummaryFiles, &row.TimeCreated, &row.TimeUpdated); err != nil {
			rows.Close()
			return nil, fmt.Errorf("history: scan opencode session: %w", err)
		}
		sessions = append(sessions, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("history: iterate opencode sessions: %w", err)
	}

	out := make([]Session, 0, len(sessions))
	for _, row := range sessions {
		path := rowPath(dbPath, row.ID)
		report.FilesSeen++
		notify()
		stamp := rowStamp(row.TimeUpdated)
		if unchangedRow(path, stamp, known) {
			report.FilesSkipped++
			report.FileStamps[path] = stamp
			continue
		}
		if projectRoot != "" && !within(projectRoot, row.Directory) {
			continue
		}
		session, evidence, warning := openCodeSession(db, dbPath, row, index)
		if warning != "" {
			report.Warnings = append(report.Warnings, warning)
			continue
		}
		report.FileStamps[path] = stamp
		report.FilesRead++
		report.SessionsFound++
		report.AssetEvidenceFound += evidence
		out = append(out, session)
	}
	return out, nil
}

func openCodeSession(db *sql.DB, dbPath string, row openCodeRow, index assetIndex) (Session, int, string) {
	path := rowPath(dbPath, row.ID)
	messages, taskText, turns, evidence, err := openCodeMessages(db, row.ID, index)
	if err != nil {
		return Session{}, 0, warn(path, err)
	}

	model := ""
	if row.Model.Valid {
		var parsed openCodeModel
		if json.Unmarshal([]byte(row.Model.String), &parsed) == nil {
			model = parsed.ID
		}
	}
	thread := threadInfo{Kind: threadKindMain, Originator: string(adapters.SourceOpenCode)}
	if row.ParentID.Valid && strings.TrimSpace(row.ParentID.String) != "" {
		thread.Kind = threadKindSubagent
		thread.ParentSessionID = string(adapters.SourceOpenCode) + ":" + strings.TrimSpace(row.ParentID.String)
	}
	if row.Agent.Valid {
		thread.AgentRole = strings.TrimSpace(row.Agent.String)
	}

	title := ""
	if openCodeTitleIsRecorded(row.Title) {
		title, _ = boundText(row.Title, maxSessionTitle)
	}
	if title == "" && taskText != "" {
		title, _ = boundText(taskText, maxSessionTitle)
	}

	usage, cost := openCodeUsage(row, turns, model)
	raw, err := marshalNormalized(row.ID, row.Directory, row.Version, model, title, taskText,
		millisToTime(row.TimeCreated), millisToTime(row.TimeUpdated), thread, usage, cost, messages)
	if err != nil {
		return Session{}, 0, fmt.Sprintf("%s: normalize: %v", path, err)
	}
	return Session{Input: ingest.SessionInput{
		Raw: adapters.RawSession{Source: adapters.SourceOpenCode, SessionID: row.ID,
			RawJSON: raw, SourcePath: path},
		TaskTags:            nativeTaskTags(taskText, row.Directory),
		OpportunityAssetIDs: nativeOpportunityAssetIDs(taskText, messages, index),
		Usage:               usage,
		ParserVersion:       ParserVersion,
	}, SourcePath: path}, evidence, ""
}

type turnCounts struct{ assistant, user int64 }

// openCodeMessages walks the session's messages and their parts in recorded
// order. A tool part carries the call, the result, and the outcome in one row,
// so it becomes two transcript records sharing one call_id.
func openCodeMessages(db *sql.DB, sessionID string, index assetIndex) ([]normalizedMessage, string, turnCounts, int, error) {
	rows, err := db.Query(`
		SELECT m.id, m.time_created, m.data, p.id, p.data
		FROM message m LEFT JOIN part p ON p.message_id = m.id
		WHERE m.session_id = ?
		ORDER BY m.time_created, m.id, p.time_created, p.id`, sessionID)
	if err != nil {
		return nil, "", turnCounts{}, 0, fmt.Errorf("read messages: %w", err)
	}
	defer rows.Close()

	var out []normalizedMessage
	var counts turnCounts
	taskText := ""
	evidence := 0
	seenMessage := make(map[string]bool)
	for rows.Next() {
		var messageID, messageData string
		var messageCreated int64
		var partID, partData sql.NullString
		if err := rows.Scan(&messageID, &messageCreated, &messageData, &partID, &partData); err != nil {
			return nil, "", counts, 0, fmt.Errorf("scan message: %w", err)
		}
		var message openCodeMessage
		if err := json.Unmarshal([]byte(messageData), &message); err != nil {
			continue
		}
		if !seenMessage[messageID] {
			seenMessage[messageID] = true
			switch message.Role {
			case "assistant":
				counts.assistant++
			case "user":
				counts.user++
			}
		}
		if !partData.Valid {
			continue
		}
		var part openCodePart
		if err := json.Unmarshal([]byte(partData.String), &part); err != nil {
			continue
		}
		ref := partID.String
		if ref == "" {
			ref = messageID
		}
		at := firstMillis(part.Time.Start, part.State.Time.Start, message.Time.Created, messageCreated)

		switch part.Type {
		case "text":
			if !isTranscriptRole(message.Role) {
				continue
			}
			text, truncated := boundText(part.Text, maxTranscriptText)
			if text == "" || isGeneratedNoise(text) {
				continue
			}
			if taskText == "" && message.Role == "user" && meaningfulTaskText(text) {
				taskText, _ = boundText(text, maxTaskText)
			}
			out = append(out, normalizedMessage{ID: ref, Timestamp: formatMillis(at),
				Role: message.Role, Kind: "message", Text: text, Truncated: truncated})
		case "tool":
			call, result, found := openCodeTool(ref, at, part, index)
			out = append(out, call)
			evidence += len(call.Invocations)
			if found {
				out = append(out, result)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, "", counts, 0, fmt.Errorf("iterate messages: %w", err)
	}
	return out, taskText, counts, evidence, nil
}

// openCodeTool splits one tool part into the call and, when the state reached a
// terminal status, its result. A still-running tool has no result: that is
// unrecorded, not a success.
func openCodeTool(ref string, at int64, part openCodePart, index assetIndex) (normalizedMessage, normalizedMessage, bool) {
	input := ""
	if len(part.State.Input) > 0 && string(part.State.Input) != "null" {
		input = marshalBoundedJSON(part.State.Input, maxToolPayload)
	}
	invocations := index.invocationsInText(input)
	call := normalizedMessage{ID: ref, Timestamp: formatMillis(at), Role: "assistant", Kind: "tool_call",
		ToolName: part.Tool, CallID: part.CallID, ToolInput: input,
		Truncated:   input != "" && jsonWasTruncated(input, part.State.Input, maxToolPayload),
		Invocations: invocations}

	if part.State.Status != "completed" && part.State.Status != "error" {
		return call, normalizedMessage{}, false
	}
	text := part.State.Output
	if part.State.Status == "error" {
		text = firstNonEmpty(part.State.Error, part.State.Output)
	}
	output, truncated := boundText(text, maxToolPayload)
	failed := part.State.Status == "error"
	isError, exitCode := normalizeToolFailure(output, &failed, part.State.Metadata.Exit)
	end := firstMillis(part.State.Time.End, part.Time.End, at)
	result := normalizedMessage{ID: ref, Timestamp: formatMillis(end), Role: "tool", Kind: "tool_result",
		CallID: part.CallID, ToolOutput: output, Truncated: truncated, IsError: isError, ExitCode: exitCode}
	if part.State.Metadata.Interrupted != nil && *part.State.Metadata.Interrupted {
		result.AbortReason = "interrupted"
	}
	return call, result, true
}

// openCodeUsage takes the session row's own totals. §19.5 is explicit that
// these are used directly rather than re-derived from parts, because the row is
// what opencode itself accumulated. The returned cost is separate because
// session_usage has no cost column.
func openCodeUsage(row openCodeRow, turns turnCounts, model string) (*eventstore.SessionUsage, *float64) {
	usage := &eventstore.SessionUsage{
		InputTokens:       positiveInt64(row.TokensInput),
		CachedInputTokens: positiveInt64(row.TokensCacheRead),
		CacheWriteTokens:  positiveInt64(row.TokensCacheWrite),
		OutputTokens:      positiveInt64(row.TokensOutput),
		ReasoningTokens:   positiveInt64(row.TokensReasoning),
		AssistantTurns:    int64Ptr(turns.assistant),
		UserTurns:         int64Ptr(turns.user),
		Source:            UsageSourceOpenCode,
	}
	// The five columns are separate counters (A5 field matrix §opencode), and
	// tokens_reasoning is a part of tokens_output that opencode reports on its
	// own, so adding it again would count it twice. TokenTotalRule is applied
	// here like everywhere else.
	usage.RecomputeTotal()
	// summary_* is NULL until opencode computes a diff summary, so a NULL here
	// is "no summary yet" while a 0 is a real "changed nothing".
	if row.SummaryAdditions.Valid {
		usage.LinesAdded = int64Ptr(row.SummaryAdditions.Int64)
	}
	if row.SummaryDeletions.Valid {
		usage.LinesRemoved = int64Ptr(row.SummaryDeletions.Int64)
	}
	if row.SummaryFiles.Valid {
		usage.FilesChanged = int64Ptr(row.SummaryFiles.Int64)
	}
	// opencode records one model per session, so the whole turn count and the
	// whole token total belong to it. A session with no model recorded gets no
	// by-model row rather than one attributed to "".
	if model != "" && usage.TotalTokens != nil {
		usage.ByModel = []eventstore.ModelUsage{{
			Model: model, Turns: turns.assistant,
			InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, TotalTokens: usage.TotalTokens,
		}}
	}
	if usage.TotalTokens == nil {
		usage.Source = eventstore.UsageSourceUnrecorded
	}
	var cost *float64
	if row.Cost > 0 {
		value := row.Cost
		cost = &value
	}
	return usage, cost
}

// openCodeTitleIsRecorded rejects the placeholder opencode writes before a
// session has been named. "New session - <timestamp>" is not evidence of what
// the session was about, so the title stays unrecorded (ADR-13).
func openCodeTitleIsRecorded(title string) bool {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" || isGeneratedNoise(trimmed) {
		return false
	}
	return !strings.HasPrefix(trimmed, "New session - ")
}

func firstMillis(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

// openCodeProbe reports what the database holds without parsing any session,
// for /ingest/health.
func openCodeProbe(dbPath string) (int, *time.Time, error) {
	db, err := sql.Open("sqlite", "file:"+dbPath+openCodeDSN)
	if err != nil {
		return 0, nil, err
	}
	defer db.Close()
	var count int
	var updated sql.NullInt64
	if err := db.QueryRow(`SELECT COUNT(*), MAX(time_updated) FROM session`).Scan(&count, &updated); err != nil {
		return 0, nil, err
	}
	if !updated.Valid {
		return count, nil, nil
	}
	return count, millisToTime(updated.Int64), nil
}

// readOpenCodeRow re-reads one opencode session out of the database by the
// pseudo path the discovery pass recorded for it (`<db>#<session id>`). It is
// what the versioned re-read calls: an opencode session is a row rather than a
// file, so it has no path of its own, but it is still re-readable and still
// has to be re-read when a parser rule changes.
func readOpenCodeRow(path string, index assetIndex, projectRoot string) (Session, bool, string) {
	cut := strings.LastIndex(path, "#")
	if cut < 0 {
		return Session{}, false, ""
	}
	dbPath, sessionID := path[:cut], path[cut+1:]
	if dbPath == "" || sessionID == "" {
		return Session{}, false, ""
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+openCodeDSN)
	if err != nil {
		return Session{}, false, warn(path, err)
	}
	defer db.Close()
	var row openCodeRow
	if err := db.QueryRow(`
		SELECT id, parent_id, directory, title, version, agent, model,
		       tokens_input, tokens_output, tokens_reasoning, tokens_cache_read, tokens_cache_write,
		       cost, summary_additions, summary_deletions, summary_files, time_created, time_updated
		FROM session WHERE id = ?`, sessionID).
		Scan(&row.ID, &row.ParentID, &row.Directory, &row.Title, &row.Version,
			&row.Agent, &row.Model, &row.TokensInput, &row.TokensOutput, &row.TokensReasoning,
			&row.TokensCacheRead, &row.TokensCacheWrite, &row.Cost, &row.SummaryAdditions,
			&row.SummaryDeletions, &row.SummaryFiles, &row.TimeCreated, &row.TimeUpdated); err != nil {
		return Session{}, false, ""
	}
	if projectRoot != "" && !within(projectRoot, row.Directory) {
		return Session{}, false, ""
	}
	session, _, warning := openCodeSession(db, dbPath, row, index)
	if warning != "" {
		return Session{}, false, warning
	}
	return session, true, ""
}
