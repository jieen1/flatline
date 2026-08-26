package api

import (
	"net/http"

	"flatline/internal/runtime"
)

// handleIngestStatus is the one endpoint that is never cached: it is what the
// UI polls to watch an import that is still running.
func (s *Server) handleIngestStatus(w http.ResponseWriter, r *http.Request) {
	var assetsCount, sessionsCount, eventsCount int
	if err := s.db.QueryRowContext(r.Context(), `
		SELECT (SELECT COUNT(*) FROM assets), (SELECT COUNT(*) FROM sessions), (SELECT COUNT(*) FROM events)`).
		Scan(&assetsCount, &sessionsCount, &eventsCount); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	progress := runtime.ImportProgress{Phase: runtime.PhaseIdle}
	if s.status != nil {
		progress = s.status.Progress()
	}
	status := "ready"
	if progress.Phase != runtime.PhaseIdle {
		status = "importing"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": status, "source": "daemon_owned_sqlite", "data_version": s.dataVersion(),
		"import": progress,
		"assets": assetsCount, "sessions": sessionsCount, "events": eventsCount,
		"message": "counts reflect persisted local facts; no external upload is performed",
	})
}
