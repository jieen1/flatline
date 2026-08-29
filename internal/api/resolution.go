package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"flatline/internal/friction"
)

// Resolution mining (P18-1) is the corpus put to work: for a friction
// signature, what happened right after it stopped inside each session where
// work went on. The answer is a sequence-and-alignment fact — "this ran next,
// and the signature did not come back" — never a causal claim; corroboration
// across sessions is what lets the reader weigh it, so every action carries
// the number of distinct sessions it appeared in.

const resolutionWindow = 5

const resolutionNote = "终结 = 该签名在一个会话内最后一次出现之后，会话仍有事件记录；随后动作 = 终结点之后按时间取前 5 条命令与文件操作；跨会话按规范化后的动作聚合，sessions 为出现该动作的会话数（佐证数）。裸文件动作（读/写某个文件）只在失败工具本身是文件工具时参与排序——“接着读了个文件”在任何失败之后都常见，对命令类失败是噪音；样例序列里保留全部动作。只陈述先后与对齐，不判定因果——随后发生的未必是解法。"

const resolutionNoteEN = "Ended means the session still recorded events after the signature's last occurrence; the aftermath is the first 5 commands and file actions after that point, in time order. Actions are aggregated across sessions by their normalized form, and sessions is how many distinct sessions carried the action (the corroboration). Bare file actions rank only when the failing tool is itself a file tool — reading some file next is common after any failure and is noise for command failures; the sample sequence keeps every action. This states sequence and alignment, never cause — what followed is not necessarily what fixed it."

type resolutionAction struct {
	// Label is the normalized form actions are corroborated under: the
	// normalized command line, or action:tool for a file operation.
	Label    string `json:"label"`
	Kind     string `json:"kind"`
	Sessions int    `json:"sessions"`
	Count    int    `json:"count"`
}

type resolutionSampleAction struct {
	Kind       string  `json:"kind"`
	Detail     string  `json:"detail"`
	EventID    int64   `json:"event_id"`
	OccurredAt string  `json:"occurred_at"`
	ExitCode   *int64  `json:"exit_code"`
	IsError    *bool   `json:"is_error"`
	Program    *string `json:"program"`
}

type resolutionSample struct {
	SessionID    string                   `json:"session_id"`
	DisplayTitle *string                  `json:"display_title"`
	LastHitAt    string                   `json:"last_hit_at"`
	Actions      []resolutionSampleAction `json:"actions"`
}

type resolutionResponse struct {
	Signature     string             `json:"signature"`
	SampleLine    string             `json:"sample_line"`
	TotalSessions int                `json:"total_sessions"`
	EndedSessions int                `json:"ended_sessions"`
	Actions       []resolutionAction `json:"actions"`
	Sample        *resolutionSample  `json:"sample"`
	Note          string             `json:"note"`
	NoteEN        string             `json:"note_en"`
	Complete      bool               `json:"complete"`
}

func (s *Server) handleFrictionResolution(w http.ResponseWriter, r *http.Request) {
	signature := strings.TrimSpace(r.URL.Query().Get("signature"))
	if signature == "" {
		http.Error(w, "signature is required", http.StatusBadRequest)
		return
	}
	projectKey := strings.TrimSpace(r.URL.Query().Get("project"))
	out, err := s.mineResolution(r.Context(), signature, projectKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) mineResolution(ctx context.Context, signature, projectKey string) (resolutionResponse, error) {
	out := resolutionResponse{Signature: signature, SampleLine: frictionSignatureLine(signature),
		Actions: []resolutionAction{}, Note: resolutionNote, NoteEN: resolutionNoteEN, Complete: true}

	args := []any{signature}
	projectFilter := ""
	if projectKey != "" {
		projectFilter = " AND (SELECT s.project_key FROM sessions s WHERE s.id = f.session_id) = ?"
		args = append(args, projectKey)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.session_id, MAX(f.occurred_at),
		       (SELECT MAX(e.occurred_at) FROM events e WHERE e.session_id = f.session_id)
		FROM friction_records f WHERE f.signature = ?`+projectFilter+` GROUP BY f.session_id`, args...)
	if err != nil {
		return out, fmt.Errorf("api: resolution sessions: %w", err)
	}
	defer rows.Close()
	type ended struct {
		sessionID, lastHit string
	}
	endedSessions := make([]ended, 0, 16)
	for rows.Next() {
		var sessionID, lastHit string
		var sessionEnd sql.NullString
		if err := rows.Scan(&sessionID, &lastHit, &sessionEnd); err != nil {
			return out, err
		}
		out.TotalSessions++
		if sessionEnd.Valid && sessionEnd.String > lastHit {
			endedSessions = append(endedSessions, ended{sessionID, lastHit})
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.EndedSessions = len(endedSessions)
	if len(endedSessions) == 0 {
		return out, nil
	}

	// Bare file actions rank only for file-tool signatures: measured on the
	// real corpus, "read:Read" topped 9 of 12 command-failure signatures as
	// filler. The sample sequences keep every action either way.
	rankFiles := fileToolSignature(signature)
	type key struct{ label, kind string }
	totals := make(map[key]*resolutionAction)
	sessionsOf := make(map[key]map[string]struct{})
	// positionOf breaks ties: at equal corroboration, the action that sits
	// closer to the ending point ranks first.
	positionOf := make(map[key]int)
	perSession := make(map[string][]resolutionSampleAction, len(endedSessions))
	labelsOf := make(map[string]map[key]struct{}, len(endedSessions))
	for _, item := range endedSessions {
		actions, err := s.aftermath(ctx, item.sessionID, item.lastHit)
		if err != nil {
			return out, err
		}
		perSession[item.sessionID] = actions
		labelsOf[item.sessionID] = make(map[key]struct{}, len(actions))
		for position, action := range actions {
			if action.Kind == "file" && !rankFiles {
				continue
			}
			k := key{label: aftermathLabel(action), kind: action.Kind}
			entry, ok := totals[k]
			if !ok {
				entry = &resolutionAction{Label: k.label, Kind: k.kind}
				totals[k] = entry
				sessionsOf[k] = make(map[string]struct{})
			}
			entry.Count++
			sessionsOf[k][item.sessionID] = struct{}{}
			labelsOf[item.sessionID][k] = struct{}{}
			positionOf[k] += position
		}
	}
	for k, entry := range totals {
		entry.Sessions = len(sessionsOf[k])
		out.Actions = append(out.Actions, *entry)
	}
	sort.Slice(out.Actions, func(i, j int) bool {
		left, right := out.Actions[i], out.Actions[j]
		if left.Sessions != right.Sessions {
			return left.Sessions > right.Sessions
		}
		if left.Count != right.Count {
			return left.Count > right.Count
		}
		leftPos := positionOf[key{label: left.Label, kind: left.Kind}]
		rightPos := positionOf[key{label: right.Label, kind: right.Kind}]
		if leftPos != rightPos {
			return leftPos < rightPos
		}
		return left.Label < right.Label
	})
	if len(out.Actions) > 8 {
		out.Actions = out.Actions[:8]
	}

	if len(out.Actions) == 0 {
		return out, nil
	}
	// The sample is the most recent ended session whose aftermath carries the
	// top corroborated action, so the sequence shown is the one the counts
	// point at.
	top := key{label: out.Actions[0].Label, kind: out.Actions[0].Kind}
	var sample *resolutionSample
	for _, item := range endedSessions {
		if _, carries := labelsOf[item.sessionID][top]; !carries {
			continue
		}
		if sample == nil || item.lastHit > sample.LastHitAt {
			sample = &resolutionSample{SessionID: item.sessionID, LastHitAt: item.lastHit,
				Actions: perSession[item.sessionID]}
		}
	}
	if sample != nil {
		var title sql.NullString
		if err := s.db.QueryRowContext(ctx,
			`SELECT COALESCE(title, task_text) FROM sessions WHERE id = ?`, sample.SessionID).Scan(&title); err == nil && title.Valid {
			value := boundRunes(strings.TrimSpace(title.String), maxSessionTitleRunes)
			if value != "" {
				sample.DisplayTitle = &value
			}
		}
	}
	out.Sample = sample
	return out, nil
}

// aftermath is the first resolutionWindow commands and file actions after a
// point in time, merged in time order.
func (s *Server) aftermath(ctx context.Context, sessionID, after string) ([]resolutionSampleAction, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT 'command', COALESCE(program, ''), command, exit_code, is_error, event_id, COALESCE(occurred_at, '')
		FROM session_commands WHERE session_id = ? AND occurred_at > ?
		UNION ALL
		SELECT 'file', tool_name, action || ' ' || path, NULL, NULL, event_id, COALESCE(occurred_at, '')
		FROM session_files WHERE session_id = ? AND occurred_at > ?
		ORDER BY 7 LIMIT ?`, sessionID, after, sessionID, after, resolutionWindow)
	if err != nil {
		return nil, fmt.Errorf("api: resolution aftermath: %w", err)
	}
	defer rows.Close()
	out := make([]resolutionSampleAction, 0, resolutionWindow)
	for rows.Next() {
		var action resolutionSampleAction
		var program, detail string
		var exitCode sql.NullInt64
		var isError sql.NullInt64
		if err := rows.Scan(&action.Kind, &program, &detail, &exitCode, &isError, &action.EventID, &action.OccurredAt); err != nil {
			return nil, err
		}
		action.Detail = boundRunes(detail, 160)
		if program != "" {
			value := program
			action.Program = &value
		}
		if exitCode.Valid {
			value := exitCode.Int64
			action.ExitCode = &value
		}
		if isError.Valid {
			value := isError.Int64 != 0
			action.IsError = &value
		}
		out = append(out, action)
	}
	return out, rows.Err()
}

// fileToolSignature reads the tool out of a signature's middle part and says
// whether it is itself a file tool — the case where a following file action is
// the story rather than filler.
func fileToolSignature(signature string) bool {
	parts := strings.SplitN(signature, "|", 3)
	if len(parts) < 2 {
		return false
	}
	switch parts[1] {
	case "Edit", "Write", "Read", "NotebookEdit", "MultiEdit", "apply_patch":
		return true
	}
	return false
}

// aftermathLabel is the form an action is corroborated under across sessions:
// the normalized command line, or action:tool for a file operation — the file
// path varies per case and would break the count, so it stays in the sample.
func aftermathLabel(action resolutionSampleAction) string {
	if action.Kind == "file" {
		verb, _, _ := strings.Cut(action.Detail, " ")
		tool := ""
		if action.Program != nil {
			tool = *action.Program
		}
		return verb + ":" + tool
	}
	return friction.NormalizeLine(action.Detail)
}
