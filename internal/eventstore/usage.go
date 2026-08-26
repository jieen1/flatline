package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Which record the token numbers were read out of. A session whose source
// never wrote one is stored with UsageSourceUnrecorded and NULL token columns:
// "not recorded" is a different fact from "zero tokens" (AGENTS.md §2.4).
const (
	UsageSourceClaude     = "claude_usage"
	UsageSourceCodex      = "codex_token_count"
	UsageSourceOpenCode   = "opencode_session"
	UsageSourceDSH        = "dsh_message_usage"
	UsageSourceUnrecorded = "unrecorded"
)

// SessionUsage is what one transcript recorded about what a session cost and
// changed. Every measured field is a pointer: nil means the source did not
// record it.
type SessionUsage struct {
	InputTokens       *int64 `json:"input_tokens"`
	CachedInputTokens *int64 `json:"cached_input_tokens"`
	CacheWriteTokens  *int64 `json:"cache_write_tokens"`
	OutputTokens      *int64 `json:"output_tokens"`
	ReasoningTokens   *int64 `json:"reasoning_tokens"`
	TotalTokens       *int64 `json:"total_tokens"`
	AssistantTurns    *int64 `json:"assistant_turns"`
	UserTurns         *int64 `json:"user_turns"`
	LinesAdded        *int64 `json:"lines_added"`
	LinesRemoved      *int64 `json:"lines_removed"`
	FilesChanged      *int64 `json:"files_changed"`
	ActiveMS          *int64 `json:"active_ms"`
	ContextWindow     *int64 `json:"context_window"`
	// Cost is what the source itself says the session cost, in its own
	// currency unit. nil means the source records no cost at all.
	Cost   *float64 `json:"cost"`
	Source string   `json:"source"`
	// ByModel splits the turns and, where the source records them per turn,
	// the tokens across the models the transcript used.
	ByModel []ModelUsage `json:"by_model,omitempty"`
}

// ModelUsage is one transcript's use of one model. InputTokens follows the
// same definition as the session row: the input that was not served from
// cache. TotalTokens is the model's whole share — cached and cache-write input
// included — so the per-model totals still add up to the session total even
// though this row does not carry the cache columns.
type ModelUsage struct {
	Model        string `json:"model"`
	Turns        int64  `json:"turns"`
	InputTokens  *int64 `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"`
	TotalTokens  *int64 `json:"total_tokens"`
}

// TokenTotalRule is the one definition of a token total in this system, in the
// words the API hands to the page.
//
// Each harness means something different by its own "total": Codex counts
// cached input inside input_tokens and reports total = input + output, while
// Claude Code keeps cache reads and cache writes out of input_tokens and
// reports no session total at all. Adding the stored components of a mixed
// history therefore did not match the stored totals — on this machine the
// aggregate read total 20.11G against input 10.10G + cached 19.67G + output
// 0.05G, which is not an arithmetic that can be explained to anyone.
//
// So no harness's own total is stored. input_tokens always means the input
// that was not served from cache, and the total is always recomputed from the
// components. Reasoning tokens are a part of the output the model already
// counted, so they are reported and never added again.
const TokenTotalRule = "total_tokens = input_tokens + cached_input_tokens + cache_write_tokens + output_tokens；input_tokens 是未命中缓存的输入，reasoning_tokens 已包含在 output_tokens 内、不重复计入。"

// TokenTotalRuleEN is the same sentence for a reader in English.
const TokenTotalRuleEN = "total_tokens = input_tokens + cached_input_tokens + cache_write_tokens + output_tokens. input_tokens is the input that missed the cache; reasoning_tokens is already part of output_tokens and is not added twice."

// RecomputeTotal rewrites TotalTokens from the components. A usage with no
// token component recorded at all keeps a nil total: nothing was measured, and
// zero would be a different claim.
func (u *SessionUsage) RecomputeTotal() {
	if u == nil {
		return
	}
	var total int64
	measured := false
	for _, part := range []*int64{u.InputTokens, u.CachedInputTokens, u.CacheWriteTokens, u.OutputTokens} {
		if part == nil {
			continue
		}
		measured = true
		total += *part
	}
	if !measured {
		u.TotalTokens = nil
		return
	}
	u.TotalTokens = &total
}

// usageColumns are the measured columns, in the order every statement here
// writes and reads them.
var usageColumns = []string{
	"input_tokens", "cached_input_tokens", "cache_write_tokens", "output_tokens",
	"reasoning_tokens", "total_tokens", "assistant_turns", "user_turns",
	"lines_added", "lines_removed", "files_changed", "active_ms",
}

func (u *SessionUsage) fields() []*int64 {
	return []*int64{
		u.InputTokens, u.CachedInputTokens, u.CacheWriteTokens, u.OutputTokens,
		u.ReasoningTokens, u.TotalTokens, u.AssistantTurns, u.UserTurns,
		u.LinesAdded, u.LinesRemoved, u.FilesChanged, u.ActiveMS,
	}
}

func (u *SessionUsage) setFields(values []*int64) {
	u.InputTokens, u.CachedInputTokens, u.CacheWriteTokens, u.OutputTokens = values[0], values[1], values[2], values[3]
	u.ReasoningTokens, u.TotalTokens, u.AssistantTurns, u.UserTurns = values[4], values[5], values[6], values[7]
	u.LinesAdded, u.LinesRemoved, u.FilesChanged, u.ActiveMS = values[8], values[9], values[10], values[11]
}

// RecordFileUsage stores what one transcript recorded and refreshes the
// session roll-up it belongs to. It is written at the end of ingest, from the
// parse of the source text, because the stored tool payloads are bounded and
// cannot be measured from.
func (s *Store) RecordFileUsage(ctx context.Context, path, sessionID string, usage *SessionUsage, parserVersion string) error {
	if sessionID == "" || path == "" {
		return fmt.Errorf("eventstore: usage needs a session id and a transcript path")
	}
	if usage == nil {
		return nil
	}
	source := usage.Source
	if source == "" {
		source = UsageSourceUnrecorded
	}
	models, err := json.Marshal(usage.ByModel)
	if err != nil {
		return fmt.Errorf("eventstore: marshal model usage %s: %w", path, err)
	}
	args := []any{path, sessionID}
	for _, value := range usage.fields() {
		args = append(args, nullableInt64(value))
	}
	args = append(args, nullableInt64(usage.ContextWindow), nullableFloat64(usage.Cost),
		source, string(models), parserVersion, formatTime(time.Now().UTC()))
	assignments := ""
	for _, column := range usageColumns {
		assignments += "\n\t\t\t" + column + " = excluded." + column + ","
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO native_file_usage (path, session_id, `+joinColumns(usageColumns)+`,
			context_window, cost, usage_source, by_model_json, parser_version, computed_at)
		VALUES (?, ?, `+placeholders(len(usageColumns))+`, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (path) DO UPDATE SET
			session_id = excluded.session_id,`+assignments+`
			context_window = excluded.context_window,
			cost = excluded.cost,
			usage_source = excluded.usage_source,
			by_model_json = excluded.by_model_json,
			parser_version = excluded.parser_version,
			computed_at = excluded.computed_at`, args...); err != nil {
		return fmt.Errorf("eventstore: write file usage %s: %w", path, err)
	}
	return s.RollUpSessionUsage(ctx, sessionID, parserVersion)
}

// RollUpSessionUsage rebuilds one session's measurement row from the files it
// was read out of. A Claude Code session is one main transcript plus one file
// per subagent, so the session total is the sum over all of them.
//
// The same transcript can be on disk under two paths: Claude Code symlinks a
// subagent's transcript into a second parent's directory, and the daemon then
// has a measurement row for each path. Adding both doubled the session — one
// local subagent reported 20.3M tokens twice and 49 minutes of active time
// inside a 49-minute session. A transcript is identified by its file name, not
// its path, so the second copy is skipped.
func (s *Store) RollUpSessionUsage(ctx context.Context, sessionID, parserVersion string) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT path, `+joinColumns(usageColumns)+`, context_window, cost, usage_source, COALESCE(by_model_json, '')
		FROM native_file_usage WHERE session_id = ? ORDER BY path`, sessionID)
	if err != nil {
		return fmt.Errorf("eventstore: read file usage %s: %w", sessionID, err)
	}
	total := SessionUsage{Source: UsageSourceUnrecorded}
	totals := make([]*int64, len(usageColumns))
	models := make(map[string]*ModelUsage)
	var order []string
	seenTranscripts := make(map[string]struct{})
	for rows.Next() {
		values := make([]sql.NullInt64, len(usageColumns))
		scan := make([]any, 0, len(usageColumns)+5)
		var path string
		scan = append(scan, &path)
		for index := range values {
			scan = append(scan, &values[index])
		}
		var window sql.NullInt64
		var cost sql.NullFloat64
		var source, encoded string
		scan = append(scan, &window, &cost, &source, &encoded)
		if err := rows.Scan(scan...); err != nil {
			rows.Close()
			return fmt.Errorf("eventstore: scan file usage %s: %w", sessionID, err)
		}
		name := transcriptName(path)
		if _, seen := seenTranscripts[name]; seen {
			continue
		}
		seenTranscripts[name] = struct{}{}
		for index, value := range values {
			if value.Valid {
				totals[index] = addKnown(totals[index], value.Int64)
			}
		}
		if window.Valid {
			if total.ContextWindow == nil || *total.ContextWindow < window.Int64 {
				value := window.Int64
				total.ContextWindow = &value
			}
		}
		if cost.Valid {
			sum := cost.Float64
			if total.Cost != nil {
				sum += *total.Cost
			}
			total.Cost = &sum
		}
		if source != UsageSourceUnrecorded && total.Source == UsageSourceUnrecorded {
			total.Source = source
		}
		var perFile []ModelUsage
		if encoded != "" {
			_ = json.Unmarshal([]byte(encoded), &perFile)
		}
		for _, item := range perFile {
			if item.Model == "" {
				continue
			}
			entry, ok := models[item.Model]
			if !ok {
				entry = &ModelUsage{Model: item.Model}
				models[item.Model] = entry
				order = append(order, item.Model)
			}
			entry.Turns += item.Turns
			entry.InputTokens = addPointer(entry.InputTokens, item.InputTokens)
			entry.OutputTokens = addPointer(entry.OutputTokens, item.OutputTokens)
			entry.TotalTokens = addPointer(entry.TotalTokens, item.TotalTokens)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("eventstore: iterate file usage %s: %w", sessionID, err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("eventstore: close file usage %s: %w", sessionID, err)
	}
	total.setFields(totals)
	// The roll-up adds up components that were each normalised per file, so
	// the session total is derived once more here rather than being the sum of
	// per-file totals: one rule, applied at every level (TokenTotalRule).
	total.RecomputeTotal()
	sort.Strings(order)
	for _, model := range order {
		total.ByModel = append(total.ByModel, *models[model])
	}
	return s.writeSessionUsage(ctx, sessionID, &total, parserVersion)
}

func (s *Store) writeSessionUsage(ctx context.Context, sessionID string, usage *SessionUsage, parserVersion string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("eventstore: begin usage write: %w", err)
	}
	rollback := func(err error) error {
		_ = tx.Rollback()
		return err
	}
	args := []any{sessionID}
	for _, value := range usage.fields() {
		args = append(args, nullableInt64(value))
	}
	args = append(args, nullableInt64(usage.ContextWindow), nullableFloat64(usage.Cost),
		usage.Source, parserVersion, formatTime(time.Now().UTC()))
	assignments := ""
	for _, column := range usageColumns {
		assignments += "\n\t\t\t" + column + " = excluded." + column + ","
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO session_usage (session_id, `+joinColumns(usageColumns)+`,
			context_window, cost, usage_source, parser_version, computed_at)
		VALUES (?, `+placeholders(len(usageColumns))+`, ?, ?, ?, ?, ?)
		ON CONFLICT (session_id) DO UPDATE SET`+assignments+`
			context_window = excluded.context_window,
			cost = excluded.cost,
			usage_source = excluded.usage_source,
			parser_version = excluded.parser_version,
			computed_at = excluded.computed_at`, args...); err != nil {
		return rollback(fmt.Errorf("eventstore: write session usage %s: %w", sessionID, err))
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_model_usage WHERE session_id = ?`, sessionID); err != nil {
		return rollback(fmt.Errorf("eventstore: clear model usage %s: %w", sessionID, err))
	}
	for _, model := range usage.ByModel {
		if model.Model == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO session_model_usage (session_id, model, turns, input_tokens, output_tokens, total_tokens)
			VALUES (?, ?, ?, ?, ?, ?)`,
			sessionID, model.Model, model.Turns, nullableInt64(model.InputTokens),
			nullableInt64(model.OutputTokens), nullableInt64(model.TotalTokens)); err != nil {
			return rollback(fmt.Errorf("eventstore: write model usage %s/%s: %w", sessionID, model.Model, err))
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("eventstore: commit usage write: %w", err)
	}
	return nil
}

// UsageCandidate is one transcript that still has to be measured.
type UsageCandidate struct {
	Path      string
	SessionID string
	Source    string
}

// TranscriptsMissingUsage lists the transcripts that have never been measured,
// or were measured by a different parser. It is what fills the projection in
// for a database migrated before session_usage existed.
func (s *Store) TranscriptsMissingUsage(ctx context.Context, parserVersion string) ([]UsageCandidate, error) {
	return s.usageCandidates(ctx, `
		SELECT n.path, n.session_id, s.source
		FROM native_files n
		JOIN sessions s ON s.id = n.session_id
		WHERE NOT EXISTS (
			SELECT 1 FROM native_file_usage u
			WHERE u.path = n.path AND u.parser_version = ?)
		ORDER BY n.path`, parserVersion)
}

// TranscriptsWithStaleParser lists the transcripts a newer parser has not read
// yet. A file stamped with the current version is never read again.
func (s *Store) TranscriptsWithStaleParser(ctx context.Context, parserVersion string) ([]UsageCandidate, error) {
	return s.usageCandidates(ctx, `
		SELECT n.path, COALESCE(n.session_id, ''), COALESCE(s.source, '')
		FROM native_files n
		LEFT JOIN sessions s ON s.id = n.session_id
		WHERE n.parser_version IS NULL OR n.parser_version <> ?
		ORDER BY n.path`, parserVersion)
}

func (s *Store) usageCandidates(ctx context.Context, query, parserVersion string) ([]UsageCandidate, error) {
	rows, err := s.db.QueryContext(ctx, query, parserVersion)
	if err != nil {
		return nil, fmt.Errorf("eventstore: list transcripts to measure: %w", err)
	}
	defer rows.Close()
	out := make([]UsageCandidate, 0)
	for rows.Next() {
		var item UsageCandidate
		if err := rows.Scan(&item.Path, &item.SessionID, &item.Source); err != nil {
			return nil, fmt.Errorf("eventstore: scan transcript to measure: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// StampParserVersion records that this transcript has been read by the current
// parser, so the next daemon start does not read it again.
func (s *Store) StampParserVersion(ctx context.Context, path, parserVersion string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE native_files SET parser_version = ? WHERE path = ?`, parserVersion, path); err != nil {
		return fmt.Errorf("eventstore: stamp parser version %s: %w", path, err)
	}
	return nil
}

func addKnown(current *int64, value int64) *int64 {
	if current == nil {
		return &value
	}
	sum := *current + value
	return &sum
}

func addPointer(current, value *int64) *int64 {
	if value == nil {
		return current
	}
	return addKnown(current, *value)
}

func nullableFloat64(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func joinColumns(columns []string) string {
	out := ""
	for index, column := range columns {
		if index > 0 {
			out += ", "
		}
		out += column
	}
	return out
}

func placeholders(count int) string {
	out := ""
	for index := 0; index < count; index++ {
		if index > 0 {
			out += ", "
		}
		out += "?"
	}
	return out
}

// transcriptName identifies a transcript independently of where it sits. A
// Claude Code transcript is named after the session or the agent it belongs to,
// a Codex rollout after its own uuid, and an opencode session after its row id,
// so the file name is the transcript's identity and the directory is not.
func transcriptName(path string) string {
	if cut := strings.LastIndexAny(path, `/\`); cut >= 0 {
		return path[cut+1:]
	}
	return path
}
