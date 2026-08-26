package eventstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// A source is a root the daemon reads sessions out of.
//
// Until now the roots were flags and built-in probes, and a stored session
// recorded only which harness wrote it. That is enough for one machine and not
// enough for two: a ~/.codex/sessions directory rsynced from another machine
// holds sessions that are, in the database, indistinguishable from the local
// ones. Registering each root as a row is what lets a session say which one it
// came from, and lets the user name it ("笔记本", "工作站").
//
// The registry never gives the daemon a reason to write into a source: the
// only thing stored here is where to read and what to call it.

// Source is one configured root.
type Source struct {
	ID           int64   `json:"id"`
	Kind         string  `json:"kind"`
	Root         string  `json:"root"`
	Label        *string `json:"label"`
	MachineLabel *string `json:"machine_label"`
	Enabled      bool    `json:"enabled"`
	CreatedAt    string  `json:"created_at"`
	// Sessions and LastSessionAt are what is actually stored from this root.
	Sessions      int     `json:"sessions"`
	LastSessionAt *string `json:"last_session_at"`
}

// RegisterSource records a root the daemon was given or probed. An existing
// row keeps the name the user gave it: the daemon supplies a default label
// only the first time it sees a root.
func (s *Store) RegisterSource(ctx context.Context, kind, root, label string) error {
	kind, root = strings.TrimSpace(kind), strings.TrimSpace(root)
	if kind == "" || root == "" {
		return fmt.Errorf("eventstore: a source needs a kind and a root")
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO sources (kind, root, label) VALUES (?, ?, ?)
		ON CONFLICT (kind, root) DO NOTHING`, kind, root, nullableString(label)); err != nil {
		return fmt.Errorf("eventstore: register source %s %s: %w", kind, root, err)
	}
	return nil
}

// ListSources reports every configured root with what is stored from it.
func (s *Store) ListSources(ctx context.Context) ([]Source, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT src.id, src.kind, src.root, src.label, src.machine_label, src.enabled, src.created_at,
		       COUNT(ses.id), MAX(ses.started_at)
		FROM sources src
		LEFT JOIN sessions ses ON ses.source_id = src.id
		GROUP BY src.id
		ORDER BY src.kind, src.root`)
	if err != nil {
		return nil, fmt.Errorf("eventstore: list sources: %w", err)
	}
	defer rows.Close()
	out := make([]Source, 0)
	for rows.Next() {
		var item Source
		var label, machineLabel, lastSeen sql.NullString
		var enabled int
		if err := rows.Scan(&item.ID, &item.Kind, &item.Root, &label, &machineLabel, &enabled,
			&item.CreatedAt, &item.Sessions, &lastSeen); err != nil {
			return nil, fmt.Errorf("eventstore: scan source: %w", err)
		}
		item.Enabled = enabled != 0
		if label.Valid {
			item.Label = &label.String
		}
		if machineLabel.Valid {
			item.MachineLabel = &machineLabel.String
		}
		if lastSeen.Valid {
			item.LastSessionAt = &lastSeen.String
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// SourceUpdate is what a user may change about a registered root: what it is
// called and whether it is read. The root itself is not editable — a different
// root is a different source, and renaming one in place would silently refile
// every session read from it.
type SourceUpdate struct {
	Label        *string `json:"label"`
	MachineLabel *string `json:"machine_label"`
	Enabled      *bool   `json:"enabled"`
}

func (s *Store) UpdateSource(ctx context.Context, id int64, update SourceUpdate) (bool, error) {
	assignments := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if update.Label != nil {
		assignments = append(assignments, "label = ?")
		args = append(args, nullableString(*update.Label))
	}
	if update.MachineLabel != nil {
		assignments = append(assignments, "machine_label = ?")
		args = append(args, nullableString(*update.MachineLabel))
	}
	if update.Enabled != nil {
		assignments = append(assignments, "enabled = ?")
		args = append(args, boolInt(*update.Enabled))
	}
	if len(assignments) == 0 {
		return false, fmt.Errorf("eventstore: nothing to update")
	}
	args = append(args, id)
	result, err := s.db.ExecContext(ctx,
		`UPDATE sources SET `+strings.Join(assignments, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return false, fmt.Errorf("eventstore: update source %d: %w", id, err)
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}

// AddSource registers a root the user typed. It returns the row, whether it
// was new or already there, so the caller can say which happened.
func (s *Store) AddSource(ctx context.Context, kind, root, label string) (Source, bool, error) {
	kind, root = strings.TrimSpace(kind), strings.TrimSpace(root)
	if err := s.RegisterSource(ctx, kind, root, label); err != nil {
		return Source{}, false, err
	}
	sources, err := s.ListSources(ctx)
	if err != nil {
		return Source{}, false, err
	}
	for _, item := range sources {
		if item.Kind == kind && item.Root == root {
			return item, true, nil
		}
	}
	return Source{}, false, fmt.Errorf("eventstore: source %s %s was not registered", kind, root)
}

// AttachSessionSources files every session under the configured root its
// transcript was read from. It is derived, not recorded at ingest: the
// registry can gain a root after the sessions under it were already imported,
// and this is what makes those sessions say where they came from.
//
// The longest matching root wins, so a root nested inside another one — a
// single project's history under the whole history directory — takes the
// session.
func (s *Store) AttachSessionSources(ctx context.Context) (int, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE sessions SET source_id = (
			SELECT src.id FROM sources src
			JOIN native_files nf ON nf.session_id = sessions.id
			WHERE src.kind = sessions.source
			  AND (nf.path = src.root
			       OR nf.path LIKE src.root || '/%'
			       OR nf.path LIKE src.root || '#%')
			ORDER BY LENGTH(src.root) DESC
			LIMIT 1)
		WHERE EXISTS (
			SELECT 1 FROM sources src
			JOIN native_files nf ON nf.session_id = sessions.id
			WHERE src.kind = sessions.source
			  AND (nf.path = src.root
			       OR nf.path LIKE src.root || '/%'
			       OR nf.path LIKE src.root || '#%'))`)
	if err != nil {
		return 0, fmt.Errorf("eventstore: attach session sources: %w", err)
	}
	affected, err := result.RowsAffected()
	return int(affected), err
}
