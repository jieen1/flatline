package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"flatline/internal/friction"
)

// The adherence curve (P17-1) is the longitudinal half of the coverage
// question: coverage says whether a user rule mentions a mechanism at all,
// the curve says how often that mechanism kept occurring, week by week. It
// states alignment and stops there — a drop after a rule landed can just as
// well be a drop in workload, so the denominator rides with every point and
// the criterion sentence says so.

const adherenceWeeks = 12

const weeklyNote = "每周从周一起算；count = 该签名当周的摩擦记录数，session_count = 出现它的会话数，week_sessions = 同一筛选范围内当周开始的全部会话数（分母）。曲线只陈述对齐，不判定因果——规则落笔前后的变化也可能来自工作量变化。"

const weeklyNoteEN = "Weeks start on Monday; count is the signature's friction records that week, session_count the sessions it occurred in, week_sessions every session that started that week under the same filter (the denominator). The curve states alignment only — a change around a rule landing can just as well be a change in workload."

const assetAdherenceNote = "只列这份规则正文提到了关键词的机制（判定与 coverage 相同：大小写不敏感、逐关键词查找）；每条机制的曲线合并它匹配到的全部签名。项目级规则只统计其项目内的会话，用户级规则统计全部。曲线只陈述对齐，不判定因果。"

const assetAdherenceNoteEN = "Only mechanisms whose keywords appear in this rule's text are listed (the same case-insensitive keyword check coverage uses); each mechanism's curve merges every signature it matches. A project-scope rule counts only its project's sessions; a user-scope rule counts all. The curve states alignment, never cause."

type weekPoint struct {
	// Week is the Monday the week starts on, as a date.
	Week         string `json:"week"`
	Count        int    `json:"count"`
	SessionCount int    `json:"session_count"`
	WeekSessions int    `json:"week_sessions"`
}

type weeklyResponse struct {
	Signature  string      `json:"signature"`
	SampleLine string      `json:"sample_line"`
	Weeks      []weekPoint `json:"weeks"`
	Note       string      `json:"note"`
	NoteEN     string      `json:"note_en"`
	Complete   bool        `json:"complete"`
}

func (s *Server) handleFrictionWeekly(w http.ResponseWriter, r *http.Request) {
	signature := strings.TrimSpace(r.URL.Query().Get("signature"))
	if signature == "" {
		http.Error(w, "signature is required", http.StatusBadRequest)
		return
	}
	projectKey := strings.TrimSpace(r.URL.Query().Get("project"))
	weeks, err := s.weeklySeries(r.Context(), []string{signature}, projectKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, weeklyResponse{
		Signature: signature, SampleLine: frictionSignatureLine(signature),
		Weeks: weeks, Note: weeklyNote, NoteEN: weeklyNoteEN, Complete: true,
	})
}

// weekMonday is the Monday of the week holding t, as a date string. It has to
// agree with the SQL below, which normalizes any date to its Monday the same
// way: forward to Sunday ('weekday 0' stays put on a Sunday), then back six.
func weekMonday(t time.Time) string {
	return t.AddDate(0, 0, -((int(t.Weekday()) + 6) % 7)).Format("2006-01-02")
}

const sqlWeekMonday = "date(%s, 'weekday 0', '-6 days')"

// weeklySeries builds the continuous last-N-weeks axis for a set of
// signatures. Empty weeks are present with a zero count and their real
// denominator: the silent week is exactly the reading the curve exists for.
func (s *Server) weeklySeries(ctx context.Context, signatures []string, projectKey string) ([]weekPoint, error) {
	now := time.Now().UTC()
	out := make([]weekPoint, adherenceWeeks)
	index := make(map[string]int, adherenceWeeks)
	for i := 0; i < adherenceWeeks; i++ {
		monday := weekMonday(now.AddDate(0, 0, -7*(adherenceWeeks-1-i)))
		out[i] = weekPoint{Week: monday}
		index[monday] = i
	}
	firstMonday := out[0].Week

	placeholders := strings.Repeat("?,", len(signatures))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, 0, len(signatures)+2)
	for _, signature := range signatures {
		args = append(args, signature)
	}
	args = append(args, firstMonday)
	projectFilter := ""
	if projectKey != "" {
		projectFilter = " AND s.project_key = ?"
		args = append(args, projectKey)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+fmt.Sprintf(sqlWeekMonday, "f.occurred_at")+` AS wk,
		       COUNT(*), COUNT(DISTINCT f.session_id)
		FROM friction_records f JOIN sessions s ON s.id = f.session_id
		WHERE f.signature IN (`+placeholders+`) AND f.occurred_at >= ?`+projectFilter+`
		GROUP BY wk`, args...)
	if err != nil {
		return nil, fmt.Errorf("api: weekly friction counts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var week string
		var count, sessions int
		if err := rows.Scan(&week, &count, &sessions); err != nil {
			return nil, fmt.Errorf("api: scan weekly count: %w", err)
		}
		if position, ok := index[week]; ok {
			out[position].Count = count
			out[position].SessionCount = sessions
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	denominatorArgs := []any{firstMonday}
	denominatorFilter := ""
	if projectKey != "" {
		denominatorFilter = " AND s.project_key = ?"
		denominatorArgs = append(denominatorArgs, projectKey)
	}
	denominators, err := s.db.QueryContext(ctx, `
		SELECT `+fmt.Sprintf(sqlWeekMonday, "s.started_at")+` AS wk, COUNT(*)
		FROM sessions s
		WHERE s.started_at IS NOT NULL AND s.started_at >= ?`+denominatorFilter+`
		GROUP BY wk`, denominatorArgs...)
	if err != nil {
		return nil, fmt.Errorf("api: weekly session denominators: %w", err)
	}
	defer denominators.Close()
	for denominators.Next() {
		var week string
		var sessions int
		if err := denominators.Scan(&week, &sessions); err != nil {
			return nil, fmt.Errorf("api: scan weekly denominator: %w", err)
		}
		if position, ok := index[week]; ok {
			out[position].WeekSessions = sessions
		}
	}
	return out, denominators.Err()
}

type mechanismCurve struct {
	Kind        string `json:"kind"`
	Mechanism   string `json:"mechanism"`
	MechanismEN string `json:"mechanism_en"`
	// KeywordsMentioned are the keywords of this mechanism that actually
	// appear in the rule's text — the factual link between rule and curve.
	KeywordsMentioned []string    `json:"keywords_mentioned"`
	Signatures        int         `json:"signatures"`
	Weeks             []weekPoint `json:"weeks"`
}

type assetAdherenceResponse struct {
	AssetID    string           `json:"asset_id"`
	Mechanisms []mechanismCurve `json:"mechanisms"`
	Note       string           `json:"note"`
	NoteEN     string           `json:"note_en"`
	Complete   bool             `json:"complete"`
}

func (s *Server) handleAssetAdherence(w http.ResponseWriter, r *http.Request) {
	assetID := r.PathValue("id")
	ctx := r.Context()
	var kind, scope, sourcePath string
	err := s.db.QueryRowContext(ctx,
		`SELECT kind, scope, COALESCE(source_path, '') FROM assets WHERE id = ?`, assetID).
		Scan(&kind, &scope, &sourcePath)
	if err != nil {
		http.Error(w, "asset not found", http.StatusNotFound)
		return
	}
	out := assetAdherenceResponse{AssetID: assetID, Mechanisms: []mechanismCurve{},
		Note: assetAdherenceNote, NoteEN: assetAdherenceNoteEN, Complete: true}
	// Only a rule-shaped asset states working methods a mechanism keyword
	// could appear in; a skill or hook gets an empty answer, not an error.
	if (kind != "rule" && kind != "agents_md") || sourcePath == "" {
		writeJSON(w, http.StatusOK, out)
		return
	}
	text, ok := readRuleText(sourcePath)
	if !ok {
		writeJSON(w, http.StatusOK, out)
		return
	}
	// A project-scope rule answers only for its own project, the same
	// applicability coverage uses.
	projectKey := ""
	if scope == "project" {
		projectKey = projectKeyOfRulePath(sourcePath)
	}
	signatures, err := s.distinctSignatures(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, rule := range friction.KeywordRules() {
		mentioned := make([]string, 0, len(rule.Keywords))
		for _, keyword := range rule.Keywords {
			if strings.Contains(text, strings.ToLower(keyword)) {
				mentioned = append(mentioned, keyword)
			}
		}
		if len(mentioned) == 0 {
			continue
		}
		matched := make([]string, 0, 4)
		for _, signature := range signatures {
			if rule.Matches(signature) {
				matched = append(matched, signature)
			}
		}
		curve := mechanismCurve{Kind: rule.Kind, Mechanism: rule.Mechanism,
			MechanismEN: rule.MechanismEN, KeywordsMentioned: mentioned,
			Signatures: len(matched), Weeks: []weekPoint{}}
		if len(matched) > 0 {
			weeks, err := s.weeklySeries(ctx, matched, projectKey)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			curve.Weeks = weeks
		}
		out.Mechanisms = append(out.Mechanisms, curve)
	}
	writeJSON(w, http.StatusOK, out)
}

// projectKeyOfRulePath walks up from a rule file to the directory the project
// keys use: the part above a .claude/… path, else the file's own directory.
func projectKeyOfRulePath(path string) string {
	if index := strings.Index(path, "/.claude/"); index > 0 {
		return path[:index]
	}
	if index := strings.LastIndex(path, "/"); index > 0 {
		return path[:index]
	}
	return path
}

func (s *Server) distinctSignatures(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT signature FROM friction_records WHERE signature IS NOT NULL AND signature <> ''`)
	if err != nil {
		return nil, fmt.Errorf("api: distinct signatures: %w", err)
	}
	defer rows.Close()
	out := make([]string, 0, 256)
	for rows.Next() {
		var signature string
		if err := rows.Scan(&signature); err != nil {
			return nil, err
		}
		out = append(out, signature)
	}
	return out, rows.Err()
}
