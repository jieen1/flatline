package api

import (
	"context"
	"database/sql"
	"fmt"

	"flatline/internal/eventstore"
)

// usageResponse is what a session cost and changed. Every measured field is
// nullable and is null when the source did not record it — the UI shows
// "未记录", never 0 (AGENTS.md §2.4). source names the record the tokens were
// read out of, or "unrecorded".
type usageResponse struct {
	Source            string               `json:"source"`
	InputTokens       *int64               `json:"input_tokens"`
	CachedInputTokens *int64               `json:"cached_input_tokens"`
	CacheWriteTokens  *int64               `json:"cache_write_tokens"`
	OutputTokens      *int64               `json:"output_tokens"`
	ReasoningTokens   *int64               `json:"reasoning_tokens"`
	TotalTokens       *int64               `json:"total_tokens"`
	AssistantTurns    *int64               `json:"assistant_turns"`
	UserTurns         *int64               `json:"user_turns"`
	LinesAdded        *int64               `json:"lines_added"`
	LinesRemoved      *int64               `json:"lines_removed"`
	FilesChanged      *int64               `json:"files_changed"`
	ActiveMS          *int64               `json:"active_ms"`
	ContextWindow     *int64               `json:"context_window"`
	Cost              *float64             `json:"cost"`
	ByModel           []modelUsageResponse `json:"by_model,omitempty"`
}

type modelUsageResponse struct {
	Model        string `json:"model"`
	Turns        int64  `json:"turns"`
	InputTokens  *int64 `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"`
	TotalTokens  *int64 `json:"total_tokens"`
}

// usageSourceUnrecorded is what a session with no measurement row reports. A
// session whose transcript is gone was never measured, and saying so is not
// the same as saying it cost nothing.
const usageSourceUnrecorded = "unrecorded"

// usageColumns are the measured columns of session_usage in the order
// scanUsage reads them.
const usageColumns = `u.input_tokens, u.cached_input_tokens, u.cache_write_tokens,
	u.output_tokens, u.reasoning_tokens, u.total_tokens, u.assistant_turns, u.user_turns,
	u.lines_added, u.lines_removed, u.files_changed, u.active_ms, u.context_window, u.cost, u.usage_source`

// scanUsage turns one session_usage row into the response object. A row that
// is entirely NULL (the LEFT JOIN found nothing) reports "unrecorded".
func scanUsage(values []sql.NullInt64, cost sql.NullFloat64, source sql.NullString) usageResponse {
	out := usageResponse{Source: usageSourceUnrecorded}
	if source.Valid && source.String != "" {
		out.Source = source.String
	}
	if cost.Valid {
		value := cost.Float64
		out.Cost = &value
	}
	targets := []**int64{
		&out.InputTokens, &out.CachedInputTokens, &out.CacheWriteTokens,
		&out.OutputTokens, &out.ReasoningTokens, &out.TotalTokens,
		&out.AssistantTurns, &out.UserTurns, &out.LinesAdded, &out.LinesRemoved,
		&out.FilesChanged, &out.ActiveMS, &out.ContextWindow,
	}
	for index, target := range targets {
		if index < len(values) && values[index].Valid {
			value := values[index].Int64
			*target = &value
		}
	}
	return out
}

// usageScanTargets are the scan destinations for usageColumns, in order.
func usageScanTargets(values []sql.NullInt64, cost *sql.NullFloat64, source *sql.NullString) []any {
	out := make([]any, 0, len(values)+2)
	for index := range values {
		out = append(out, &values[index])
	}
	return append(out, cost, source)
}

const usageValueCount = 13

// sessionModelUsage reads the per-model split of one session.
func (s *Server) sessionModelUsage(ctx context.Context, sessionID string) ([]modelUsageResponse, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT model, turns, input_tokens, output_tokens, total_tokens
		FROM session_model_usage WHERE session_id = ? ORDER BY model`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("api: session model usage: %w", err)
	}
	defer rows.Close()
	out := make([]modelUsageResponse, 0)
	for rows.Next() {
		var item modelUsageResponse
		var input, output, total sql.NullInt64
		if err := rows.Scan(&item.Model, &item.Turns, &input, &output, &total); err != nil {
			return nil, fmt.Errorf("api: scan session model usage: %w", err)
		}
		item.InputTokens, item.OutputTokens, item.TotalTokens = optionalInt64(input), optionalInt64(output), optionalInt64(total)
		out = append(out, item)
	}
	return out, rows.Err()
}

func optionalInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	out := value.Int64
	return &out
}

// usageTotals is the aggregate measurement of a set of sessions, with its own
// denominator: known_sessions out of in_range. A session with no measurement
// row is in the denominator and not in the numerator, which is what keeps
// "not recorded" from reading as zero.
type usageTotals struct {
	KnownSessions int   `json:"known_sessions"`
	TokenSessions int   `json:"token_sessions"`
	InRange       int   `json:"in_range"`
	TotalTokens   int64 `json:"total_tokens"`
	InputTokens   int64 `json:"input_tokens"`
	OutputTokens  int64 `json:"output_tokens"`
	CachedTokens  int64 `json:"cached_input_tokens"`
	// CacheWriteTokens completes the four components of Definition, so a
	// reader can add them up and land on TotalTokens.
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	// ReasoningTokens is the part of OutputTokens the source reported on its
	// own. It is reported, never added again.
	ReasoningTokens int64 `json:"reasoning_tokens"`
	LinesAdded      int64 `json:"lines_added"`
	LinesRemoved    int64 `json:"lines_removed"`
	FilesChanged    int64 `json:"files_changed"`
	ActiveMS        int64 `json:"active_ms"`
	// CostSessions is the denominator for Cost: only some sources record one.
	CostSessions int      `json:"cost_sessions"`
	Cost         *float64 `json:"cost"`
	Note         string   `json:"note"`
	NoteEN       string   `json:"note_en"`
	// Definition is how a token total is arrived at, in one sentence, so a
	// page can say what the number it prints actually counts. DefinitionEN is
	// the same sentence for a reader in English.
	Definition   string `json:"definition"`
	DefinitionEN string `json:"definition_en"`
}

const usageDenominatorNote = "known_sessions 是记录了度量的会话数，in_range 是筛选范围内的全部会话；未记录的会话不计入分子。"

const usageDenominatorNoteEN = "known_sessions is how many sessions recorded the measurement; in_range is every session in the filtered window. A session that recorded nothing is left out of the numerator."

// aggregateUsage sums the measurement over the sessions the caller's own
// WHERE clause selects. join is the FROM clause fragment that reaches sessions
// as `s`; where/args are that caller's filter.
func (s *Server) aggregateUsage(ctx context.Context, where string, args []any) (usageTotals, error) {
	out := usageTotals{Note: usageDenominatorNote, NoteEN: usageDenominatorNoteEN,
		Definition: eventstore.TokenTotalRule, DefinitionEN: eventstore.TokenTotalRuleEN}
	var cost sql.NullFloat64
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       SUM(CASE WHEN u.session_id IS NULL THEN 0 ELSE 1 END),
		       SUM(CASE WHEN u.total_tokens IS NULL THEN 0 ELSE 1 END),
		       SUM(COALESCE(u.total_tokens, 0)), SUM(COALESCE(u.input_tokens, 0)),
		       SUM(COALESCE(u.output_tokens, 0)), SUM(COALESCE(u.cached_input_tokens, 0)),
		       SUM(COALESCE(u.cache_write_tokens, 0)), SUM(COALESCE(u.reasoning_tokens, 0)),
		       SUM(COALESCE(u.lines_added, 0)), SUM(COALESCE(u.lines_removed, 0)),
		       SUM(COALESCE(u.files_changed, 0)), SUM(COALESCE(u.active_ms, 0)),
		       SUM(CASE WHEN u.cost IS NULL THEN 0 ELSE 1 END), SUM(u.cost)
		FROM sessions s
		LEFT JOIN session_stats st ON st.session_id = s.id
		LEFT JOIN session_usage u ON u.session_id = s.id`+where, args...).
		Scan(&out.InRange, &nullableInt64Target{&out.KnownSessions}, &nullableInt64Target{&out.TokenSessions},
			&nullableInt64Sum{&out.TotalTokens}, &nullableInt64Sum{&out.InputTokens},
			&nullableInt64Sum{&out.OutputTokens}, &nullableInt64Sum{&out.CachedTokens},
			&nullableInt64Sum{&out.CacheWriteTokens}, &nullableInt64Sum{&out.ReasoningTokens},
			&nullableInt64Sum{&out.LinesAdded}, &nullableInt64Sum{&out.LinesRemoved},
			&nullableInt64Sum{&out.FilesChanged}, &nullableInt64Sum{&out.ActiveMS},
			&nullableInt64Target{&out.CostSessions}, &cost)
	if err != nil {
		return usageTotals{}, fmt.Errorf("api: aggregate usage: %w", err)
	}
	if cost.Valid {
		value := cost.Float64
		out.Cost = &value
	}
	return out, nil
}

// modelUsageTotal is one model's share of a set of sessions.
type modelUsageTotal struct {
	Model       string `json:"model"`
	Sessions    int    `json:"sessions"`
	Turns       int64  `json:"turns"`
	TotalTokens int64  `json:"total_tokens"`
}

func (s *Server) aggregateModelUsage(ctx context.Context, where string, args []any) ([]modelUsageTotal, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.model, COUNT(DISTINCT m.session_id), SUM(m.turns), SUM(COALESCE(m.total_tokens, 0))
		FROM session_model_usage m
		JOIN sessions s ON s.id = m.session_id
		LEFT JOIN session_stats st ON st.session_id = s.id`+where+`
		GROUP BY m.model
		ORDER BY 4 DESC, 1
		LIMIT 50`, args...)
	if err != nil {
		return nil, fmt.Errorf("api: aggregate model usage: %w", err)
	}
	defer rows.Close()
	out := make([]modelUsageTotal, 0)
	for rows.Next() {
		var item modelUsageTotal
		if err := rows.Scan(&item.Model, &item.Sessions, &nullableInt64Sum{&item.Turns},
			&nullableInt64Sum{&item.TotalTokens}); err != nil {
			return nil, fmt.Errorf("api: scan model usage total: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// nullableInt64Sum lets a SUM over an empty set scan as zero: the set is
// empty, so zero is the sum, not a guess.
type nullableInt64Sum struct{ target *int64 }

func (n *nullableInt64Sum) Scan(value any) error {
	switch typed := value.(type) {
	case nil:
		*n.target = 0
	case int64:
		*n.target = typed
	case float64:
		*n.target = int64(typed)
	default:
		return fmt.Errorf("api: unexpected sum type %T", value)
	}
	return nil
}

type nullableInt64Target struct{ target *int }

func (n *nullableInt64Target) Scan(value any) error {
	switch typed := value.(type) {
	case nil:
		*n.target = 0
	case int64:
		*n.target = int(typed)
	case float64:
		*n.target = int(typed)
	default:
		return fmt.Errorf("api: unexpected count type %T", value)
	}
	return nil
}
