package api

import (
	"net/http"
	"sort"
	"time"
)

// The now view is the monitor's first screen: which sessions are being
// written to right now (P16-3). A transcript is live while its newest file
// was modified inside transcriptIdleBound; the mtimes come from the last
// scan pass, so a reading can lag reality by up to one scan interval. The
// answer is state, not history — it is served without an ETag and with
// no-store, because a revalidated 304 would keep a finished run on screen.

const nowNote = "进行中 = 该会话最新的转写文件在最近 10 分钟内被写入过；文件修改时间来自最近一次扫描，读数最多滞后一个扫描间隔（默认 5 分钟）。live_children 只数同样在写的子会话。"

const nowNoteEN = "In progress means the session's newest transcript file was written inside the last 10 minutes; the mtimes come from the last scan pass, so a reading can lag by up to one scan interval (default 5 minutes). live_children counts only children that are being written too."

type nowRow struct {
	*sessionResponse
	// LiveChildren is how many of this session's children are live as well; a
	// fleet still spending shows up here even while the parent itself pauses.
	LiveChildren int `json:"live_children"`
}

type nowPayload struct {
	Sessions    []nowRow `json:"sessions"`
	Count       int      `json:"count"`
	GeneratedAt string   `json:"generated_at"`
	Note        string   `json:"note"`
	NoteEN      string   `json:"note_en"`
	Complete    bool     `json:"complete"`
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
