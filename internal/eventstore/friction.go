package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"flatline/internal/canonical"
	"flatline/internal/friction"
)

const FrictionKindUserInterrupt = friction.KindUserInterrupt

const frictionToolInputBound = 512

const interruptTextPrefix = "[Request interrupted by user"

// abortReasonInterrupted is the reason Codex writes into turn_aborted when the
// user stopped the turn.
const abortReasonInterrupted = "interrupted"

type frictionToolCall struct {
	name  string
	input string
}

// IngestFriction returns the number of newly recorded friction rows. It
// records only source-backed, explicit friction: a tool result
// that carries is_error or a non-zero exit code, and a transcript message that
// records a user interrupt. Generic text containing words such as "error" is
// deliberately not classified.
//
// The record is keyed by (session, source event, friction kind) and is a
// daemon-owned derived projection (ADR-17): replaying a source file with a
// newer parser refreshes the resolved tool name, the bounded evidence and the
// category on rows that already exist, without touching the canonical event.
func (s *Store) IngestFriction(ctx context.Context, sessionID string, events []canonical.Event) (int, error) {
	if sessionID == "" {
		return 0, fmt.Errorf("eventstore: session id is required")
	}
	calls := frictionToolCalls(events)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("eventstore: begin friction transaction: %w", err)
	}
	rollback := func(err error) (int, error) {
		_ = tx.Rollback()
		return 0, err
	}
	inserted := 0
	for _, event := range events {
		if event.SessionID != sessionID {
			return rollback(fmt.Errorf("eventstore: friction event session %q does not match %q", event.SessionID, sessionID))
		}
		kind, ok := frictionKindOf(event)
		if !ok {
			continue
		}
		call, linked := calls[frictionCallID(event.Payload)]
		toolName, payload := frictionEvidence(event, call, linked)
		locator, err := json.Marshal(event.Locator)
		if err != nil {
			return rollback(fmt.Errorf("eventstore: marshal friction locator %q: %w", event.SourceEventID, err))
		}
		var occurred any
		if event.OccurredAt != nil {
			occurred = formatTime(*event.OccurredAt)
		}
		isNew, err := upsertFrictionRow(ctx, tx, frictionRow{
			sessionID: sessionID, sourceEventID: event.SourceEventID, kind: kind,
			eventType: event.EventType, observation: string(event.ObservationLevel),
			toolName: toolName, payload: payload, locator: string(locator), occurredAt: occurred,
		})
		if err != nil {
			return rollback(err)
		}
		if isNew {
			inserted++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("eventstore: commit friction: %w", err)
	}
	return inserted, nil
}

// frictionRow is one friction record about to be written: the classification
// is derived from payload here, so every writer produces the same columns.
type frictionRow struct {
	sessionID     string
	sourceEventID string
	kind          string
	eventType     string
	observation   string
	toolName      string
	payload       map[string]any
	locator       string
	occurredAt    any
}

// upsertFrictionRow writes one record and reports whether it did not exist
// before. A record that already exists has its derived columns refreshed; the
// canonical event it points at is never touched (ADR-17).
func upsertFrictionRow(ctx context.Context, tx *sql.Tx, row frictionRow) (bool, error) {
	category, rule := friction.Classify(row.kind, row.toolName, row.payload)
	signature := friction.Signature(category, row.toolName, row.payload, frictionProgram(row.payload))
	encodedPayload, err := json.Marshal(row.payload)
	if err != nil {
		return false, fmt.Errorf("eventstore: marshal friction payload %q: %w", row.sourceEventID, err)
	}
	var recordedIsError any
	if isError, has := payloadBool(row.payload, "is_error"); has {
		recordedIsError = boolInt(isError)
	}
	var recordedExitCode any
	if exitCode, has := payloadInt(row.payload, "exit_code"); has {
		recordedExitCode = exitCode
	}
	existing := 0
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM friction_records
		WHERE session_id = ? AND source_event_id = ? AND friction_kind = ?`,
		row.sessionID, row.sourceEventID, row.kind).Scan(&existing); err != nil {
		return false, fmt.Errorf("eventstore: friction lookup %q: %w", row.sourceEventID, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO friction_records
		(session_id, source_event_id, friction_kind, event_type, observation_level,
		 tool_name, category, category_rule, category_rule_en, classifier_version, signature,
		 is_error, exit_code, payload_json, locator_json, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (session_id, source_event_id, friction_kind) DO UPDATE SET
			tool_name          = excluded.tool_name,
			category           = excluded.category,
			category_rule      = excluded.category_rule,
			category_rule_en   = excluded.category_rule_en,
			classifier_version = excluded.classifier_version,
			signature          = excluded.signature,
			is_error           = excluded.is_error,
			exit_code          = excluded.exit_code,
			payload_json       = excluded.payload_json`,
		row.sessionID, row.sourceEventID, row.kind, row.eventType, row.observation,
		nullableString(row.toolName), nullableString(category), nullableString(rule.Text),
		nullableString(rule.EN), friction.ClassifierVersion, nullableString(signature),
		recordedIsError, recordedExitCode, string(encodedPayload), row.locator, row.occurredAt); err != nil {
		return false, fmt.Errorf("eventstore: insert friction %q: %w", row.sourceEventID, err)
	}
	return existing == 0, nil
}

// derivableFrictionSessions is the set of sessions holding a tool result whose
// outcome the harness printed into the output text instead of recording as a
// field. The literals are the ones ExtractOutcome reads; a zero exit code is
// excluded here so a healthy result never becomes a repeated candidate.
const derivableFrictionPredicate = `
	  AND e.event_type = 'transcript_tool_result'
	  AND e.payload_json IS NOT NULL
	  AND e.source_event_id IS NOT NULL
	  AND e.payload_json NOT LIKE '%"exit_code"%'
	  AND e.payload_json NOT LIKE '%"is_error"%'
	  AND (e.payload_json GLOB '*Process exited with code [1-9]*'
	    OR e.payload_json LIKE '%"tool_output":"Script failed%'
	    OR e.payload_json LIKE '%<tool_use_error>%')
	  AND NOT EXISTS (
	      SELECT 1 FROM friction_records fr
	      WHERE fr.session_id = e.session_id AND fr.source_event_id = e.source_event_id)`

const derivableFrictionSessions = `SELECT DISTINCT e.session_id FROM events e WHERE 1 = 1` + derivableFrictionPredicate

// DeriveMissingFriction records friction for tool results whose outcome was
// only ever printed into the output text. A transcript file whose fingerprint
// has not changed is never re-read, so those already-stored events would
// otherwise never be revisited by a newer parser. The pass is idempotent: a
// result that already has a record is left alone by the candidate query.
func (s *Store) DeriveMissingFriction(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, derivableFrictionSessions)
	if err != nil {
		return 0, fmt.Errorf("eventstore: select derivable friction sessions: %w", err)
	}
	var sessions []string
	for rows.Next() {
		var sessionID string
		if err := rows.Scan(&sessionID); err != nil {
			rows.Close()
			return 0, fmt.Errorf("eventstore: scan derivable friction session: %w", err)
		}
		sessions = append(sessions, sessionID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("eventstore: iterate derivable friction sessions: %w", err)
	}
	rows.Close()
	total := 0
	for _, sessionID := range sessions {
		inserted, err := s.deriveSessionFriction(ctx, sessionID)
		if err != nil {
			return total, err
		}
		if inserted == 0 {
			continue
		}
		// The session's friction count is a projection of these rows, so it is
		// wrong the moment new ones are recorded.
		if err := s.RecomputeSessionStats(ctx, sessionID); err != nil {
			return total, fmt.Errorf("eventstore: recompute stats after derived friction %q: %w", sessionID, err)
		}
		total += inserted
	}
	return total, nil
}

func (s *Store) deriveSessionFriction(ctx context.Context, sessionID string) (int, error) {
	// The tool identity is read through event_pairs. A session that has never
	// been projected has none, and the projection is what builds them.
	paired, err := s.SessionHasPairs(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	if !paired {
		if err := s.RecomputeSessionProjections(ctx, sessionID); err != nil {
			return 0, err
		}
	}
	calls, err := s.pairedCalls(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.source_event_id, e.event_type, e.observation_level, e.payload_json,
		       COALESCE(e.locator_json, ''), e.occurred_at
		FROM events e
		WHERE e.session_id = ?`+derivableFrictionPredicate+`
		ORDER BY e.id`, sessionID)
	if err != nil {
		return 0, fmt.Errorf("eventstore: select derivable friction events: %w", err)
	}
	type candidate struct {
		sourceEventID string
		eventType     string
		observation   string
		payload       map[string]any
		locator       string
		occurredAt    any
	}
	var candidates []candidate
	for rows.Next() {
		var item candidate
		var payload, occurred sql.NullString
		if err := rows.Scan(&item.sourceEventID, &item.eventType, &item.observation, &payload, &item.locator, &occurred); err != nil {
			rows.Close()
			return 0, fmt.Errorf("eventstore: scan derivable friction event: %w", err)
		}
		if payload.Valid && payload.String != "" {
			_ = json.Unmarshal([]byte(payload.String), &item.payload)
		}
		if item.payload == nil {
			continue
		}
		if occurred.Valid && occurred.String != "" {
			item.occurredAt = occurred.String
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("eventstore: iterate derivable friction events: %w", err)
	}
	rows.Close()
	if len(candidates) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("eventstore: begin derived friction transaction: %w", err)
	}
	inserted := 0
	for _, item := range candidates {
		call, linked := calls[item.sourceEventID]
		toolName, payload := frictionEvidence(canonical.Event{
			SourceEventID: item.sourceEventID, SessionID: sessionID,
			EventType: item.eventType, Payload: item.payload,
		}, call, linked)
		isError, hasIsError := payloadBool(payload, "is_error")
		exitCode, hasExitCode := payloadInt(payload, "exit_code")
		if !(hasIsError && isError) && !(hasExitCode && exitCode != 0) {
			continue
		}
		isNew, err := upsertFrictionRow(ctx, tx, frictionRow{
			sessionID: sessionID, sourceEventID: item.sourceEventID, kind: FrictionKindToolError,
			eventType: item.eventType, observation: item.observation, toolName: toolName,
			payload: payload, locator: item.locator, occurredAt: item.occurredAt,
		})
		if err != nil {
			_ = tx.Rollback()
			return 0, err
		}
		if isNew {
			inserted++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("eventstore: commit derived friction: %w", err)
	}
	return inserted, nil
}

// pairedCalls indexes one session's tool calls by the source event id of the
// result they produced. The link comes from event_pairs, which is the only
// record that covers both the ids the harness wrote and the pairs recovered by
// re-reading the transcript.
func (s *Store) pairedCalls(ctx context.Context, sessionID string) (map[string]frictionToolCall, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT re.source_event_id, COALESCE(p.tool_name, ''),
		       substr(COALESCE(json_extract(ce.payload_json, '$.tool_input'), ''), 1, ?)
		FROM event_pairs p
		JOIN events re ON re.id = p.result_event_id
		JOIN events ce ON ce.id = p.call_event_id
		WHERE p.session_id = ? AND re.source_event_id IS NOT NULL`,
		frictionToolInputBound, sessionID)
	if err != nil {
		return nil, fmt.Errorf("eventstore: select paired tool calls: %w", err)
	}
	defer rows.Close()
	calls := make(map[string]frictionToolCall)
	for rows.Next() {
		var sourceEventID, toolName, input string
		if err := rows.Scan(&sourceEventID, &toolName, &input); err != nil {
			return nil, fmt.Errorf("eventstore: scan paired tool call: %w", err)
		}
		calls[sourceEventID] = frictionToolCall{name: toolName, input: input}
	}
	return calls, rows.Err()
}

// frictionProgram names the program the paired tool call ran, read with the
// same parser the command projection uses so the two cannot disagree about
// what a command line runs.
func frictionProgram(payload map[string]any) string {
	input, _ := payload["tool_input"].(string)
	if strings.TrimSpace(input) == "" {
		return ""
	}
	toolName, _ := payload["tool_name"].(string)
	// A call that ran several commands reports one status, and it is the last
	// statement's, so that is the program the outcome belongs to.
	command := ""
	if commands := extractCommands(toolName, input); len(commands) > 0 {
		command = commands[len(commands)-1]
	}
	if command == "" {
		for _, field := range []string{"command", "cmd", "script"} {
			if value := strings.TrimSpace(jsonStringField(input, field)); value != "" {
				command = value
				break
			}
		}
	}
	return friction.Program(command)
}

// ReclassifyFriction recomputes every row whose classifier_version differs from
// the current one, and every row whose tool identity event_pairs has since
// recovered. The projection is derived, so a rule change is applied to the
// whole table rather than only to newly ingested sessions (ADR-10). The tool
// name comes from the pair rather than from the result's own payload: a result
// that names an opaque harness id is not naming a tool.
func (s *Store) ReclassifyFriction(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT fr.id, fr.friction_kind, fr.tool_name, fr.payload_json,
		       COALESCE(p.tool_name, ''),
		       substr(COALESCE(json_extract(ce.payload_json, '$.tool_input'), ''), 1, ?)
		FROM friction_records fr
		LEFT JOIN events re ON re.session_id = fr.session_id AND re.source_event_id = fr.source_event_id
		LEFT JOIN event_pairs p ON p.session_id = fr.session_id AND p.result_event_id = re.id
		LEFT JOIN events ce ON ce.id = p.call_event_id
		WHERE fr.classifier_version IS NULL OR fr.classifier_version != ?
		   OR (fr.category IS NOT NULL AND fr.signature IS NULL)
		   OR (fr.tool_name IS NULL AND NULLIF(TRIM(COALESCE(p.tool_name, '')), '') IS NOT NULL)`,
		frictionToolInputBound, friction.ClassifierVersion)
	if err != nil {
		return 0, fmt.Errorf("eventstore: select stale friction rows: %w", err)
	}
	type pending struct {
		id        int64
		toolName  string
		category  string
		rule      friction.Rule
		signature string
		payload   string
	}
	var stale []pending
	for rows.Next() {
		var id int64
		var kind string
		var storedTool, payload sql.NullString
		var pairedTool, pairedInput string
		if err := rows.Scan(&id, &kind, &storedTool, &payload, &pairedTool, &pairedInput); err != nil {
			rows.Close()
			return 0, fmt.Errorf("eventstore: scan stale friction row: %w", err)
		}
		var decoded map[string]any
		if payload.Valid && payload.String != "" {
			_ = json.Unmarshal([]byte(payload.String), &decoded)
		}
		if decoded == nil {
			decoded = make(map[string]any)
		}
		toolName := resolveFrictionToolName(storedTool.String, pairedTool, decoded)
		applyPairedEvidence(decoded, toolName, pairedInput)
		category, rule := friction.Classify(kind, toolName, decoded)
		encoded, err := json.Marshal(decoded)
		if err != nil {
			rows.Close()
			return 0, fmt.Errorf("eventstore: marshal reclassified payload %d: %w", id, err)
		}
		stale = append(stale, pending{id: id, toolName: toolName, category: category, rule: rule,
			signature: friction.Signature(category, toolName, decoded, frictionProgram(decoded)),
			payload:   string(encoded)})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("eventstore: iterate stale friction rows: %w", err)
	}
	rows.Close()
	if len(stale) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("eventstore: begin reclassify transaction: %w", err)
	}
	for _, item := range stale {
		if _, err := tx.ExecContext(ctx, `
			UPDATE friction_records
			SET tool_name = ?, category = ?, category_rule = ?, category_rule_en = ?,
			    classifier_version = ?, signature = ?, payload_json = ?
			WHERE id = ?`, nullableString(item.toolName), nullableString(item.category),
			nullableString(item.rule.Text), nullableString(item.rule.EN), friction.ClassifierVersion,
			nullableString(item.signature), item.payload, item.id); err != nil {
			_ = tx.Rollback()
			return 0, fmt.Errorf("eventstore: reclassify friction row %d: %w", item.id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("eventstore: commit reclassify: %w", err)
	}
	return len(stale), nil
}

// resolveFrictionToolName prefers the paired call's own name. Without a pair it
// keeps the stored name, unless that name is an opaque harness id, which is
// not a tool identity and is reported as unrecorded.
func resolveFrictionToolName(stored, paired string, payload map[string]any) string {
	if name := strings.TrimSpace(paired); name != "" {
		return name
	}
	name := strings.TrimSpace(stored)
	if name == "" {
		name, _ = payload["tool_name"].(string)
		name = strings.TrimSpace(name)
	}
	if isOpaqueCallID(name, frictionCallID(payload)) {
		return ""
	}
	return name
}

// applyPairedEvidence writes the resolved identity back into the stored
// evidence, so the payload the UI shows and the columns the API filters on
// cannot drift apart. The tool input arrives only from the paired call; it is
// what names the program in a signature that has no distinguishing line.
func applyPairedEvidence(payload map[string]any, toolName, pairedInput string) {
	if toolName == "" {
		delete(payload, "tool_name")
	} else {
		payload["tool_name"] = toolName
	}
	if _, has := payload["tool_input"]; !has && strings.TrimSpace(pairedInput) != "" {
		payload["tool_input"] = boundFrictionText(pairedInput, frictionToolInputBound)
	}
}

// frictionKindOf reports the friction kind an event carries, if any.
func frictionKindOf(event canonical.Event) (string, bool) {
	switch event.EventType {
	case canonical.EventTypeTranscriptResult:
		isError, hasIsError := payloadBool(event.Payload, "is_error")
		exitCode, hasExitCode := payloadInt(event.Payload, "exit_code")
		if !hasIsError && !hasExitCode {
			derivedIsError, derivedExitCode := derivedOutcome(event.Payload)
			isError, hasIsError = derivedIsError != nil && *derivedIsError, derivedIsError != nil
			exitCode, hasExitCode = intOrZero(derivedExitCode), derivedExitCode != nil
		}
		if (hasIsError && isError) || (hasExitCode && exitCode != 0) {
			return FrictionKindToolError, true
		}
	case canonical.EventTypeTranscriptMessage:
		// Claude Code records an interrupt as message text; Codex records it as
		// a turn_aborted reason. These are the two explicit records; nothing is
		// inferred from a turn that simply stops.
		if reason, _ := event.Payload["abort_reason"].(string); strings.TrimSpace(reason) == abortReasonInterrupted {
			return FrictionKindUserInterrupt, true
		}
		text, _ := event.Payload["text"].(string)
		if strings.HasPrefix(strings.TrimSpace(text), interruptTextPrefix) {
			return FrictionKindUserInterrupt, true
		}
	}
	return "", false
}

// frictionToolCalls indexes the tool calls of one session by the id the harness
// uses to link a result back to its call: tool_use_id for Claude Code,
// call_id for Codex.
func frictionToolCalls(events []canonical.Event) map[string]frictionToolCall {
	calls := make(map[string]frictionToolCall)
	for _, event := range events {
		if event.EventType != canonical.EventTypeTranscriptToolCall {
			continue
		}
		id := frictionCallID(event.Payload)
		if id == "" {
			continue
		}
		name, _ := event.Payload["tool_name"].(string)
		input, _ := event.Payload["tool_input"].(string)
		calls[id] = frictionToolCall{name: name, input: input}
	}
	return calls
}

// frictionCallID reads the id that links a result back to its call. Codex
// records it as turn_id (the call_… id of the response item); Claude Code
// records it as tool_use_id.
func frictionCallID(payload map[string]any) string {
	for _, key := range []string{"tool_use_id", "call_id", "turn_id"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// frictionEvidence resolves the human-readable tool name through the linked
// tool call and returns the bounded payload the rules read. An opaque harness
// id is never presented as a tool name: an unlinked result stays unrecorded.
func frictionEvidence(event canonical.Event, call frictionToolCall, linked bool) (string, map[string]any) {
	payload := copyFrictionPayload(boundedFrictionPayload(event.Payload))
	callID := frictionCallID(event.Payload)
	toolName, _ := payload["tool_name"].(string)
	if linked && strings.TrimSpace(call.name) != "" {
		toolName = call.name
	} else if isOpaqueCallID(toolName, callID) {
		toolName = ""
	}
	if toolName == "" {
		delete(payload, "tool_name")
	} else {
		payload["tool_name"] = toolName
	}
	if _, ok := payload["tool_input"]; !ok && linked && call.input != "" {
		payload["tool_input"] = boundFrictionText(call.input, frictionToolInputBound)
	}
	if isError, exitCode := derivedOutcome(payload); isError != nil || exitCode != nil {
		if isError != nil {
			payload["is_error"] = *isError
		}
		if exitCode != nil {
			payload["exit_code"] = *exitCode
		}
		// The outcome was printed into the output text rather than recorded as
		// a field. Marking it keeps the evidence honest about where it came from.
		payload["outcome_source"] = "tool_output"
	}
	return toolName, payload
}

// derivedOutcome reads the outcome out of the recorded output text, but only
// when the harness recorded neither field itself. A payload that already
// carries is_error or exit_code is left exactly as the source wrote it. The
// rules live in canonical.NormalizeToolFailure so the parser and this
// back-fill cannot reach different conclusions about the same output.
func derivedOutcome(payload map[string]any) (*bool, *int) {
	if _, has := payload["is_error"]; has {
		return nil, nil
	}
	if _, has := payload["exit_code"]; has {
		return nil, nil
	}
	output, _ := payload["tool_output"].(string)
	return canonical.NormalizeToolFailure(output, nil, nil)
}

func intOrZero(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func isOpaqueCallID(value, callID string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	return value == callID || strings.HasPrefix(value, "toolu_") || strings.HasPrefix(value, "call_")
}

func copyFrictionPayload(payload map[string]any) map[string]any {
	out := make(map[string]any, len(payload)+1)
	for key, value := range payload {
		out[key] = value
	}
	return out
}

func boundFrictionText(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

// boundedFrictionPayload keeps the stored evidence small enough to inspect
// without dropping the fields the rules and the UI depend on.
func boundedFrictionPayload(payload map[string]any) map[string]any {
	encoded, err := json.Marshal(payload)
	if err == nil && len(encoded) <= 16*1024 {
		return payload
	}
	out := make(map[string]any)
	for _, key := range []string{"message_id", "turn_id", "tool_name", "tool_use_id", "call_id", "is_error", "exit_code", "truncated"} {
		if value, ok := payload[key]; ok {
			out[key] = value
		}
	}
	if value, ok := payload["tool_output"].(string); ok {
		out["tool_output"] = boundFrictionText(value, 8192)
	}
	if value, ok := payload["text"].(string); ok {
		out["text"] = boundFrictionText(value, 8192)
	}
	out["friction_evidence_truncated"] = true
	return out
}

func payloadBool(payload map[string]any, key string) (bool, bool) {
	value, ok := payload[key]
	if !ok {
		return false, false
	}
	switch value := value.(type) {
	case bool:
		return value, true
	case *bool:
		return value != nil && *value, value != nil
	default:
		return false, false
	}
}

func payloadInt(payload map[string]any, key string) (int, bool) {
	value, ok := payload[key]
	if !ok {
		return 0, false
	}
	switch value := value.(type) {
	case int:
		return value, true
	case int8:
		return int(value), true
	case int16:
		return int(value), true
	case int32:
		return int(value), true
	case int64:
		return int(value), true
	case uint:
		return int(value), true
	case uint8:
		return int(value), true
	case uint16:
		return int(value), true
	case uint32:
		return int(value), true
	case uint64:
		if uint64(int(value)) != value {
			return 0, false
		}
		return int(value), true
	case float64:
		if value != float64(int(value)) {
			return 0, false
		}
		return int(value), true
	case json.Number:
		parsed, err := strconv.Atoi(string(value))
		return parsed, err == nil
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		return parsed, err == nil
	default:
		return 0, false
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
