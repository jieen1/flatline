package baseline

import (
	"context"
	"testing"
	"time"

	"flatline/internal/storage"
)

// All test data is synthetic (AGENTS.md §2.1, §7): no real user session data,
// no real asset paths. Rows are inserted directly into the migrated schema so
// the resolver and query are exercised against the real tables.

func testDB(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.Open(context.Background(), t.TempDir()+"/baseline.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func ts(s string) time.Time {
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return v
}

func mapEqual(a, b map[string]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// seedAsset inserts an asset row.
func seedAsset(t *testing.T, db *storage.DB, id, kind, name string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO assets (id, kind, name, first_seen_at) VALUES (?, ?, ?, ?)`,
		id, kind, name, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
}

// seedVersion inserts an asset version row and returns its id.
func seedVersion(t *testing.T, db *storage.DB, assetID string, version int, hash, observedAt string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO asset_versions (asset_id, version, content_hash, observation_level, observed_at) VALUES (?, ?, ?, ?, ?)`,
		assetID, version, hash, "unknown", observedAt)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// seedSession inserts a session row. startedAt may be "" for a NULL value.
func seedSession(t *testing.T, db *storage.DB, id, source, startedAt string) {
	t.Helper()
	var started any
	if startedAt != "" {
		started = startedAt
	}
	if _, err := db.Exec(`INSERT INTO sessions (id, source, source_session_id, started_at) VALUES (?, ?, ?, ?)`,
		id, source, id, started); err != nil {
		t.Fatal(err)
	}
}

// seedOpportunity inserts an opportunity row and returns its id.
func seedOpportunity(t *testing.T, db *storage.DB, sessionID, shapeClass, assetID, detectedAt string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO opportunities (session_id, shape_class, shape_rule_version, asset_id, detector_version, detected_at) VALUES (?, ?, ?, ?, ?, ?)`,
		sessionID, shapeClass, "shape/1", assetID, "tracker/1", detectedAt)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// seedParticipation inserts a participation row.
func seedParticipation(t *testing.T, db *storage.DB, versionID int64, sessionID, signal, level, occurredAt string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO participations (asset_version_id, session_id, participation_signal, observation_level, occurred_at) VALUES (?, ?, ?, ?, ?)`,
		versionID, sessionID, signal, level, occurredAt); err != nil {
		t.Fatal(err)
	}
}
