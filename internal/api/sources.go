package api

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"flatline/internal/adapters"
	"flatline/internal/eventstore"
)

// The sources endpoint is the data page's view of where sessions come from.
//
// | 谁做 | 做什么 | 结果 |
// | --- | --- | --- |
// | daemon 启动 | 把 flag 给的、或探测到的每个根写进 sources（已存在的行不覆盖 label） | 每个根一行，用户可以给它起名 |
// | 用户 PUT | 只改 label / machine_label / enabled | 名字与开关变了；根本身不动 |
// | 用户 POST | 新增一个 {kind, root, label} | 行建好，daemon 下一轮 refresh 才会真正去读它 |
//
// The registry is read-only over the source itself: adding a root tells the
// daemon where to read, and no code path here writes into a source directory.
//
// Easy misreading: POST does not import anything. It records the root; the
// next refresh pass is what reads it.

const sourceRootNote = "只读扫描：daemon 只从这些根读取，绝不写入源目录。新增的根在下一轮 refresh 生效。"

// sourceRootNoteEN is the same sentence for a reader in English.
const sourceRootNoteEN = "Read-only scan: the daemon only reads from these roots and never writes into a source directory. A newly added root takes effect on the next refresh."

func (s *Server) handleSources(w http.ResponseWriter, r *http.Request) {
	sources, err := eventstore.New(s.db).ListSources(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sources": sources, "note": sourceRootNote, "note_en": sourceRootNoteEN, "data_version": s.dataVersion(),
	})
}

type sourceUpdateRequest struct {
	ID int64 `json:"id"`
	eventstore.SourceUpdate
}

func (s *Server) handleUpdateSource(w http.ResponseWriter, r *http.Request) {
	var request sourceUpdateRequest
	if err := decodeJSONBody(r, &request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if request.ID <= 0 {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	updated, err := eventstore.New(s.db).UpdateSource(r.Context(), request.ID, request.SourceUpdate)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !updated {
		http.Error(w, "source not found", http.StatusNotFound)
		return
	}
	s.bumpDataVersion()
	s.handleSources(w, r)
}

type sourceCreateRequest struct {
	Kind  string `json:"kind"`
	Root  string `json:"root"`
	Label string `json:"label"`
}

func (s *Server) handleCreateSource(w http.ResponseWriter, r *http.Request) {
	var request sourceCreateRequest
	if err := decodeJSONBody(r, &request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	kind := strings.TrimSpace(request.Kind)
	if !adapters.Source(kind).Valid() {
		http.Error(w, "unknown source kind "+kind, http.StatusBadRequest)
		return
	}
	root := strings.TrimSpace(request.Root)
	if root == "" || !filepath.IsAbs(root) {
		http.Error(w, "root must be an absolute path", http.StatusBadRequest)
		return
	}
	// The root is checked read-only, so a typo is refused now instead of
	// becoming a source the daemon fails to open on every pass.
	if _, err := os.Stat(root); err != nil {
		http.Error(w, "root is not readable: "+err.Error(), http.StatusBadRequest)
		return
	}
	source, _, err := eventstore.New(s.db).AddSource(r.Context(), kind, filepath.Clean(root), request.Label)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.bumpDataVersion()
	writeJSON(w, http.StatusCreated, map[string]any{
		"source": source, "note": sourceRootNote, "note_en": sourceRootNoteEN,
		"message": "已登记；daemon 下一轮 refresh 才会读取这个根",
	})
}

// decodeJSONBody reads a bounded request body. The bound is what keeps a
// malformed client from making the daemon allocate without limit.
func decodeJSONBody(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}
