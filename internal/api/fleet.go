package api

import (
	"context"
	"net/http"
	"sort"
	"strings"
)

// The fleet view answers what a session's subagent tree did as one unit
// (ADR-25). On this machine 69% of sessions are subagents and the largest
// parent commanded 108 of them; the facts were all stored, but reading them
// meant aggregating /sessions?parent=… by hand. Everything here is a
// read-time aggregation of rows that already exist — no new tables.

type fleetRollupResponse struct {
	// Sessions is the whole tree: the parent plus every child.
	Sessions int `json:"sessions"`
	// TokenSessions is how many of them have a usage row at all. The sums
	// below cover only those; a tree with none reports null, not zero.
	TokenSessions     int    `json:"token_sessions"`
	InputTokens       *int64 `json:"input_tokens"`
	CachedInputTokens *int64 `json:"cached_input_tokens"`
	CacheWriteTokens  *int64 `json:"cache_write_tokens"`
	OutputTokens      *int64 `json:"output_tokens"`
	TotalTokens       *int64 `json:"total_tokens"`
	// WorkTokens = input + output + cache write: every token that is not a
	// cache read. On this machine cache reads are 98% of the total, so the
	// total alone overstates what a run cost by around fifty-fold.
	WorkTokens    *int64 `json:"work_tokens"`
	FrictionCount int    `json:"friction_count"`
	ToolCallCount int    `json:"tool_call_count"`
	LinesAdded    *int64 `json:"lines_added"`
	LinesRemoved  *int64 `json:"lines_removed"`
	FilesChanged  *int64 `json:"files_changed"`
}

// fleetOutcomeResponse states the recorded git evidence inside the tree and
// stops there. claude_code records no exit code for 98% of commands, so
// "no recorded failure" is the strongest sentence the facts support — it is
// never tightened into "succeeded" (ADR-8).
type fleetOutcomeResponse struct {
	CommitsRecorded  int    `json:"commits_recorded"`
	CommitsNoFailure int    `json:"commits_no_failure"`
	PushesRecorded   int    `json:"pushes_recorded"`
	PushesNoFailure  int    `json:"pushes_no_failure"`
	MergesRecorded   int    `json:"merges_recorded"`
	MergesNoFailure  int    `json:"merges_no_failure"`
	Note             string `json:"note"`
	NoteEN           string `json:"note_en"`
}

const fleetOutcomeNote = "结局证据只陈述树内记录到的 git commit / push / merge 命令数，以及其中未记录到失败（无错误标记、无非零退出码）的条数。claude_code 的命令大多没有退出码，所以“未见失败”不等于“成功”。"

const fleetOutcomeNoteEN = "Outcome evidence states how many git commit / push / merge commands the tree recorded, and how many of those carry no recorded failure (no error flag, no nonzero exit). Most claude_code commands record no exit code, so no recorded failure is not success."

type fleetResponsePayload struct {
	SessionID string               `json:"session_id"`
	Children  []*sessionResponse   `json:"children"`
	Rollup    fleetRollupResponse  `json:"rollup"`
	Outcome   fleetOutcomeResponse `json:"outcome"`
	Complete  bool                 `json:"complete"`
}

func (s *Server) handleSessionFleet(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	ctx := r.Context()
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+sessionColumns+sessionFrom+` WHERE s.id = ? OR s.parent_session_id = ?`,
		sessionID, sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var parent *sessionResponse
	children := make([]*sessionResponse, 0, 16)
	for rows.Next() {
		item, err := scanSession(rows)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if item.ID == sessionID {
			parent = item
		} else {
			children = append(children, item)
		}
	}
	if err := rows.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if parent == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	// Largest spender first; the children whose cost was never recorded sit
	// at the end rather than sorting as if they were free.
	sort.SliceStable(children, func(i, j int) bool {
		left, right := children[i].Usage.TotalTokens, children[j].Usage.TotalTokens
		if (left == nil) != (right == nil) {
			return right == nil
		}
		if left != nil && *left != *right {
			return *left > *right
		}
		return children[i].ID < children[j].ID
	})

	tree := append([]*sessionResponse{parent}, children...)
	rollup := rollUpFleet(tree)
	outcome, err := s.fleetOutcome(ctx, tree)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, fleetResponsePayload{
		SessionID: sessionID, Children: children, Rollup: rollup, Outcome: outcome, Complete: true,
	})
}

func rollUpFleet(tree []*sessionResponse) fleetRollupResponse {
	out := fleetRollupResponse{Sessions: len(tree)}
	var input, cached, write, output, total, added, removed, files int64
	for _, item := range tree {
		out.FrictionCount += item.FrictionCount
		out.ToolCallCount += item.ToolCallCount
		usage := item.Usage
		if usage.TotalTokens == nil && usage.InputTokens == nil && usage.OutputTokens == nil {
			continue
		}
		out.TokenSessions++
		input += orZero(usage.InputTokens)
		cached += orZero(usage.CachedInputTokens)
		write += orZero(usage.CacheWriteTokens)
		output += orZero(usage.OutputTokens)
		total += orZero(usage.TotalTokens)
		added += orZero(usage.LinesAdded)
		removed += orZero(usage.LinesRemoved)
		files += orZero(usage.FilesChanged)
	}
	if out.TokenSessions == 0 {
		return out
	}
	work := input + output + write
	out.InputTokens, out.CachedInputTokens, out.CacheWriteTokens = &input, &cached, &write
	out.OutputTokens, out.TotalTokens, out.WorkTokens = &output, &total, &work
	out.LinesAdded, out.LinesRemoved, out.FilesChanged = &added, &removed, &files
	return out
}

func orZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func (s *Server) fleetOutcome(ctx context.Context, tree []*sessionResponse) (fleetOutcomeResponse, error) {
	out := fleetOutcomeResponse{Note: fleetOutcomeNote, NoteEN: fleetOutcomeNoteEN}
	placeholders := make([]string, len(tree))
	args := make([]any, 0, len(tree))
	for index, item := range tree {
		placeholders[index] = "?"
		args = append(args, item.ID)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT CASE WHEN command LIKE 'git commit%' THEN 'commit'
		            WHEN command LIKE 'git push%' THEN 'push'
		            ELSE 'merge' END AS verb,
		       COUNT(*),
		       SUM(CASE WHEN COALESCE(is_error, 0) = 0 AND COALESCE(exit_code, 0) = 0 THEN 1 ELSE 0 END)
		FROM session_commands
		WHERE program = 'git'
		  AND (command LIKE 'git commit%' OR command LIKE 'git push%' OR command LIKE 'git merge%')
		  AND session_id IN (`+strings.Join(placeholders, ",")+`)
		GROUP BY verb`, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var verb string
		var recorded, noFailure int
		if err := rows.Scan(&verb, &recorded, &noFailure); err != nil {
			return out, err
		}
		switch verb {
		case "commit":
			out.CommitsRecorded, out.CommitsNoFailure = recorded, noFailure
		case "push":
			out.PushesRecorded, out.PushesNoFailure = recorded, noFailure
		case "merge":
			out.MergesRecorded, out.MergesNoFailure = recorded, noFailure
		}
	}
	return out, rows.Err()
}
