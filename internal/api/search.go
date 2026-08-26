package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
)

type searchAsset struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Kind  string  `json:"kind"`
	State *string `json:"state"`
}

type searchProgram struct {
	Program  string `json:"program"`
	Calls    int    `json:"calls"`
	Sessions int    `json:"sessions"`
}

// handleSearch answers the global search box. Each list is a shortcut into the
// page that owns it; nothing here is a ranking or a score.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	text := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := queryLimit(r, 10)
	if limit > 25 {
		limit = 25
	}
	response := map[string]any{
		"q": text, "sessions": []sessionResponse{}, "projects": []projectResponse{},
		"assets": []searchAsset{}, "programs": []searchProgram{},
		"friction_categories": []frictionCategoryCount{}, "data_version": s.dataVersion(),
	}
	if text == "" {
		writeJSON(w, http.StatusOK, response)
		return
	}
	ctx := r.Context()
	query, err := parseSessionQuery(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	query.limit, query.offset, query.deep = limit, 0, false
	sessions, _, err := s.querySessions(ctx, query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response["sessions"] = sessions

	like := "%" + strings.ToLower(text) + "%"
	projects, err := s.searchProjects(ctx, like, 5)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response["projects"] = projects

	assets, err := s.searchAssets(ctx, like, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response["assets"] = assets

	programs, err := s.searchPrograms(ctx, like, 5)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response["programs"] = programs

	categories, err := s.searchFrictionCategories(ctx, like)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response["friction_categories"] = categories
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) searchProjects(ctx context.Context, like string, limit int) ([]projectResponse, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(s.project_key, '`+unrecordedKey+`') AS project_key, COUNT(*),
		       MAX(NULLIF(s.started_at, ''))
		FROM sessions s
		WHERE LOWER(COALESCE(s.cwd, '')) LIKE ?
		GROUP BY project_key
		ORDER BY 2 DESC, 1
		LIMIT ?`, like, limit)
	if err != nil {
		return nil, fmt.Errorf("api: search projects: %w", err)
	}
	defer rows.Close()
	out := make([]projectResponse, 0, limit)
	for rows.Next() {
		var item projectResponse
		var last sql.NullString
		if err := rows.Scan(&item.Key, &item.Sessions, &last); err != nil {
			return nil, fmt.Errorf("api: scan search project: %w", err)
		}
		item.Label = projectLabelOf(item.Key)
		if item.Key != unrecordedKey {
			item.CWD = item.Key
		}
		item.LastStartedAt = later(nil, last)
		item.Harnesses = map[string]int{}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Server) searchAssets(ctx context.Context, like string, limit int) ([]searchAsset, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id, a.name, a.kind,
		       (SELECT v.state FROM vital_states v
		        WHERE v.asset_id = a.id AND v.ended_at IS NULL
		        ORDER BY v.started_at DESC LIMIT 1)
		FROM assets a
		WHERE a.archived_at IS NULL AND (LOWER(a.name) LIKE ? OR LOWER(a.id) LIKE ?)
		ORDER BY a.name
		LIMIT ?`, like, like, limit)
	if err != nil {
		return nil, fmt.Errorf("api: search assets: %w", err)
	}
	defer rows.Close()
	out := make([]searchAsset, 0, limit)
	for rows.Next() {
		var item searchAsset
		var state sql.NullString
		if err := rows.Scan(&item.ID, &item.Name, &item.Kind, &state); err != nil {
			return nil, fmt.Errorf("api: scan search asset: %w", err)
		}
		if state.Valid {
			value := state.String
			item.State = &value
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Server) searchPrograms(ctx context.Context, like string, limit int) ([]searchProgram, error) {
	out := make([]searchProgram, 0, limit)
	has, err := s.hasTable(ctx, "session_commands")
	if err != nil || !has {
		return out, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT program, COUNT(*), COUNT(DISTINCT session_id)
		FROM session_commands
		WHERE program IS NOT NULL AND program <> '' AND LOWER(program) LIKE ?
		GROUP BY program
		ORDER BY 2 DESC, 1
		LIMIT ?`, like, limit)
	if err != nil {
		return nil, fmt.Errorf("api: search programs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item searchProgram
		if err := rows.Scan(&item.Program, &item.Calls, &item.Sessions); err != nil {
			return nil, fmt.Errorf("api: scan search program: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Server) searchFrictionCategories(ctx context.Context, like string) ([]frictionCategoryCount, error) {
	out := make([]frictionCategoryCount, 0)
	has, err := s.hasColumn(ctx, "friction_records", "category")
	if err != nil || !has {
		return out, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT category, COUNT(*)
		FROM friction_records
		WHERE category IS NOT NULL AND category <> '' AND LOWER(category) LIKE ?
		GROUP BY category
		ORDER BY 2 DESC, 1
		LIMIT 10`, like)
	if err != nil {
		return nil, fmt.Errorf("api: search friction categories: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item frictionCategoryCount
		if err := rows.Scan(&item.Category, &item.Count); err != nil {
			return nil, fmt.Errorf("api: scan search friction category: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
