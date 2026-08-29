package api

import (
	"context"
	"database/sql"
	"net/http"
	"sort"
	"strings"
	"time"
)

// The now view is the monitor's first screen: which sessions are being
// written to right now (P16-3). A transcript is live while its newest file
// was modified inside transcriptIdleBound; the mtimes come from the last
// scan pass, so a reading can lag reality by up to one scan interval. The
// answer is state, not history — it is served without an ETag and with
// no-store, because a revalidated 304 would keep a finished run on screen.

const nowNote = "进行中 = 该会话最新的转写文件在最近 10 分钟内被写入过；文件修改时间来自最近一次扫描，读数最多滞后一个扫描间隔（默认 5 分钟）。live_children 只数同样在写的子会话。loop = 同一摩擦签名最近 60 分钟内在该会话出现 ≥5 次（与洞察层卡死循环同一阈值），只陈述次数与时间界，不判定原因。"

const nowNoteEN = "In progress means the session's newest transcript file was written inside the last 10 minutes; the mtimes come from the last scan pass, so a reading can lag by up to one scan interval (default 5 minutes). live_children counts only children that are being written too. loop means one friction signature recurred 5+ times in this session inside the last 60 minutes — the same threshold the stuck-loop insight uses; it states the count and the bounds, never a cause."

// nowLoopWindow bounds how far back a repeat has to be to still be "now".
const nowLoopWindow = time.Hour

type nowRow struct {
	*sessionResponse
	// LiveChildren is how many of this session's children are live as well; a
	// fleet still spending shows up here even while the parent itself pauses.
	LiveChildren int `json:"live_children"`
	// FrictionLastAt is the newest friction record's time, so the reader sees
	// whether the run is failing right now or ran clean for the last while.
	FrictionLastAt *string `json:"friction_last_at"`
	// Loop is the strongest fact a live view can state: one signature
	// recurring inside nowLoopWindow, at the stuck-loop threshold.
	Loop *nowLoop `json:"loop"`
}

type nowLoop struct {
	Signature  string `json:"signature"`
	SampleLine string `json:"sample_line"`
	Count      int    `json:"count"`
	FirstAt    string `json:"first_at"`
	LastAt     string `json:"last_at"`
}

type nowPayload struct {
	Sessions    []nowRow `json:"sessions"`
	Count       int      `json:"count"`
	GeneratedAt string   `json:"generated_at"`
	Note        string   `json:"note"`
	NoteEN      string   `json:"note_en"`
	Complete    bool     `json:"complete"`
}

// attachNowFriction fills each live row's newest friction time, and names the
// signature that recurred at the stuck-loop threshold inside nowLoopWindow.
// stuckLoopThreshold is the insight layer's own bound, so "looping now" and
// "looped in this window" are the same sentence at two time scales.
func (s *Server) attachNowFriction(ctx context.Context, live []nowRow) error {
	if len(live) == 0 {
		return nil
	}
	placeholders := make([]string, len(live))
	args := make([]any, 0, len(live)+1)
	index := make(map[string]int, len(live))
	for position, row := range live {
		placeholders[position] = "?"
		args = append(args, row.ID)
		index[row.ID] = position
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT session_id, MAX(occurred_at) FROM friction_records
		WHERE session_id IN (`+strings.Join(placeholders, ",")+`)
		GROUP BY session_id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var sessionID string
		var lastAt sql.NullString
		if err := rows.Scan(&sessionID, &lastAt); err != nil {
			return err
		}
		if position, ok := index[sessionID]; ok && lastAt.Valid {
			value := lastAt.String
			live[position].FrictionLastAt = &value
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	cutoff := time.Now().UTC().Add(-nowLoopWindow).Format(time.RFC3339Nano)
	loopArgs := append([]any{}, args...)
	loopArgs = append(loopArgs, cutoff, stuckLoopThreshold)
	loopRows, err := s.db.QueryContext(ctx, `
		SELECT session_id, signature, COUNT(*), MIN(occurred_at), MAX(occurred_at)
		FROM friction_records
		WHERE session_id IN (`+strings.Join(placeholders, ",")+`)
		  AND signature IS NOT NULL AND occurred_at >= ?
		GROUP BY session_id, signature
		HAVING COUNT(*) >= ?
		ORDER BY COUNT(*) DESC`, loopArgs...)
	if err != nil {
		return err
	}
	defer loopRows.Close()
	for loopRows.Next() {
		var sessionID, signature, firstAt, lastAt string
		var count int
		if err := loopRows.Scan(&sessionID, &signature, &count, &firstAt, &lastAt); err != nil {
			return err
		}
		position, ok := index[sessionID]
		// The list is count-descending, so the first loop a session gets is
		// its heaviest; later rows for the same session are kept out.
		if !ok || live[position].Loop != nil {
			continue
		}
		live[position].Loop = &nowLoop{Signature: signature,
			SampleLine: frictionSignatureLine(signature), Count: count,
			FirstAt: firstAt, LastAt: lastAt}
	}
	return loopRows.Err()
}

func (s *Server) handleNow(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cutoff := time.Now().Add(-transcriptIdleBound).UnixNano()
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+sessionColumns+sessionFrom+` WHERE nf.mtime_ns > ?`, cutoff)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	live := make([]nowRow, 0, 8)
	liveIDs := make(map[string]struct{}, 8)
	childrenOf := make(map[string]int, 4)
	for rows.Next() {
		item, err := scanSession(rows)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// The bound is decided by scanSession's own clock too; a row read at
		// the edge of the window can have crossed it between SQL and scan.
		if !item.InProgress {
			continue
		}
		live = append(live, nowRow{sessionResponse: item})
		liveIDs[item.ID] = struct{}{}
		if item.ParentSessionID != nil {
			childrenOf[*item.ParentSessionID]++
		}
	}
	if err := rows.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for index := range live {
		live[index].LiveChildren = childrenOf[live[index].ID]
	}
	if err := s.attachNowFriction(ctx, live); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Main sessions first, fleets before loners, the freshest first inside a
	// group, so the screen reads command post down to workers.
	sort.SliceStable(live, func(i, j int) bool {
		leftMain := live[i].ThreadKind != nil && *live[i].ThreadKind == "main"
		rightMain := live[j].ThreadKind != nil && *live[j].ThreadKind == "main"
		if leftMain != rightMain {
			return leftMain
		}
		if live[i].LiveChildren != live[j].LiveChildren {
			return live[i].LiveChildren > live[j].LiveChildren
		}
		return live[i].ID < live[j].ID
	})
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, nowPayload{
		Sessions: live, Count: len(live),
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Note:        nowNote, NoteEN: nowNoteEN, Complete: true,
	})
}
