package baseline

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"flatline/internal/storage"
)

// BundleEntry is one asset's effective version at the bundle's as-of time.
type BundleEntry struct {
	AssetID        string
	AssetVersionID int64
	Version        int
	ContentHash    string
	ObservedAt     time.Time
}

// Bundle is the effective asset version vector in force for a session at a
// given as-of time. Entries is sorted by AssetID for deterministic output.
type Bundle struct {
	SessionID       string
	AsOf            time.Time
	ResolverVersion string
	ResolvedAt      time.Time
	Entries         []BundleEntry
}

// VersionMap returns the {asset_id: asset_version_id} vector — the form stored
// in effective_bundles.bundle_json.
func (b *Bundle) VersionMap() map[string]int64 {
	out := make(map[string]int64, len(b.Entries))
	for _, e := range b.Entries {
		out[e.AssetID] = e.AssetVersionID
	}
	return out
}

// EntryFor returns the effective version entry for an asset, or ok=false when
// the asset had no version in force at the as-of time (not recorded).
func (b *Bundle) EntryFor(assetID string) (BundleEntry, bool) {
	for _, e := range b.Entries {
		if e.AssetID == assetID {
			return e, true
		}
	}
	return BundleEntry{}, false
}

// Resolver resolves the effective asset version vector for a session.
type Resolver struct{ db *storage.DB }

// NewResolver builds a Resolver over the given database.
func NewResolver(db *storage.DB) *Resolver { return &Resolver{db: db} }

// Resolve computes the effective bundle for a session using the session's
// recorded started_at as the as-of time, and persists it to effective_bundles
// (idempotent upsert keyed by session_id). It returns the bundle.
//
// A session with no recorded started_at cannot be time-anchored; Resolve
// returns an error rather than fabricate a time (缺失 ≠ 零). Use ResolveAsOf to
// supply an explicit as-of time.
func (r *Resolver) Resolve(ctx context.Context, sessionID string) (*Bundle, error) {
	var startedAt sql.NullString
	if err := r.db.QueryRowContext(ctx, `SELECT started_at FROM sessions WHERE id = ?`, sessionID).Scan(&startedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("baseline: session %s does not exist", sessionID)
		}
		return nil, fmt.Errorf("baseline: load session %s: %w", sessionID, err)
	}
	if !startedAt.Valid {
		return nil, fmt.Errorf("baseline: session %s has no recorded started_at; use ResolveAsOf to supply an explicit time", sessionID)
	}
	asOf, err := time.Parse(time.RFC3339Nano, startedAt.String)
	if err != nil {
		return nil, fmt.Errorf("baseline: parse started_at for %s: %w", sessionID, err)
	}
	bundle, err := r.compute(ctx, sessionID, asOf)
	if err != nil {
		return nil, err
	}
	if err := r.persist(ctx, bundle); err != nil {
		return nil, err
	}
	return bundle, nil
}

// ResolveAsOf computes the effective bundle for a session as of an explicit
// time. It does not persist (the persisted bundle is the one at the session's
// own started_at, written by Resolve). For each asset, the effective version
// is the one with the greatest observed_at that is <= asOf (ties broken by the
// higher version number). Assets with no version observed at or before asOf are
// omitted: their version at that time is "not recorded", never fabricated.
func (r *Resolver) ResolveAsOf(ctx context.Context, sessionID string, asOf time.Time) (*Bundle, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("baseline: session id is required")
	}
	if asOf.IsZero() {
		return nil, fmt.Errorf("baseline: as-of time is required")
	}
	if err := r.sessionExists(ctx, sessionID); err != nil {
		return nil, err
	}
	return r.compute(ctx, sessionID, asOf.UTC())
}

// compute resolves the version vector for a session at asOf without persisting.
func (r *Resolver) compute(ctx context.Context, sessionID string, asOf time.Time) (*Bundle, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT av.asset_id, av.id, av.version, av.content_hash, av.observed_at
		FROM asset_versions av
		WHERE julianday(av.observed_at) <= julianday(?)
		ORDER BY av.asset_id ASC, julianday(av.observed_at) DESC, av.version DESC`,
		formatTime(asOf))
	if err != nil {
		return nil, fmt.Errorf("baseline: query effective versions: %w", err)
	}
	defer rows.Close()

	// The first row per asset_id is the effective version (max observed_at,
	// then max version).
	entries := make([]BundleEntry, 0)
	seen := make(map[string]bool)
	for rows.Next() {
		var (
			e          BundleEntry
			observedAt string
		)
		if err := rows.Scan(&e.AssetID, &e.AssetVersionID, &e.Version, &e.ContentHash, &observedAt); err != nil {
			return nil, fmt.Errorf("baseline: scan effective version: %w", err)
		}
		if seen[e.AssetID] {
			continue
		}
		seen[e.AssetID] = true
		e.ObservedAt, err = time.Parse(time.RFC3339Nano, observedAt)
		if err != nil {
			return nil, fmt.Errorf("baseline: parse observed_at: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("baseline: iterate effective versions: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].AssetID < entries[j].AssetID })

	return &Bundle{
		SessionID:       sessionID,
		AsOf:            asOf,
		ResolverVersion: ResolverVersion,
		ResolvedAt:      time.Now().UTC(),
		Entries:         entries,
	}, nil
}

// persist upserts the bundle into effective_bundles, keyed by session_id.
// Re-resolving is idempotent: the stored bundle_json is a pure function of the
// inputs, so a replay writes the same vector (ADR-10).
func (r *Resolver) persist(ctx context.Context, b *Bundle) error {
	encoded, err := json.Marshal(b.VersionMap())
	if err != nil {
		return fmt.Errorf("baseline: marshal bundle: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO effective_bundles (session_id, bundle_json, resolver_version, resolved_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (session_id) DO UPDATE SET
			bundle_json      = excluded.bundle_json,
			resolver_version = excluded.resolver_version,
			resolved_at      = excluded.resolved_at`,
		b.SessionID, string(encoded), b.ResolverVersion, formatTime(b.ResolvedAt))
	if err != nil {
		return fmt.Errorf("baseline: persist bundle for %s: %w", b.SessionID, err)
	}
	return nil
}

// Load returns the persisted effective bundle for a session, or sql.ErrNoRows
// when no bundle has been resolved for it.
func (r *Resolver) Load(ctx context.Context, sessionID string) (*Bundle, error) {
	var (
		bundleJSON      string
		resolverVersion string
		resolvedAt      string
	)
	if err := r.db.QueryRowContext(ctx, `
		SELECT bundle_json, resolver_version, resolved_at
		FROM effective_bundles WHERE session_id = ?`, sessionID).Scan(&bundleJSON, &resolverVersion, &resolvedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("baseline: load bundle for %s: %w", sessionID, err)
	}
	var versionMap map[string]int64
	if err := json.Unmarshal([]byte(bundleJSON), &versionMap); err != nil {
		return nil, fmt.Errorf("baseline: decode bundle_json for %s: %w", sessionID, err)
	}
	resolvedAtTime, err := time.Parse(time.RFC3339Nano, resolvedAt)
	if err != nil {
		return nil, fmt.Errorf("baseline: parse resolved_at for %s: %w", sessionID, err)
	}
	bundle := &Bundle{
		SessionID:       sessionID,
		ResolverVersion: resolverVersion,
		ResolvedAt:      resolvedAtTime,
	}
	for assetID, versionID := range versionMap {
		var (
			e          BundleEntry
			observedAt string
		)
		err := r.db.QueryRowContext(ctx, `
			SELECT asset_id, id, version, content_hash, observed_at
			FROM asset_versions WHERE id = ?`, versionID).Scan(
			&e.AssetID, &e.AssetVersionID, &e.Version, &e.ContentHash, &observedAt)
		if err != nil {
			if err == sql.ErrNoRows {
				// The version row was removed; keep the id so the stored vector
				// is not silently altered.
				e.AssetID = assetID
				e.AssetVersionID = versionID
				bundle.Entries = append(bundle.Entries, e)
				continue
			}
			return nil, fmt.Errorf("baseline: load version %d: %w", versionID, err)
		}
		e.ObservedAt, err = time.Parse(time.RFC3339Nano, observedAt)
		if err != nil {
			return nil, fmt.Errorf("baseline: parse observed_at for version %d: %w", versionID, err)
		}
		bundle.Entries = append(bundle.Entries, e)
	}
	sort.Slice(bundle.Entries, func(i, j int) bool { return bundle.Entries[i].AssetID < bundle.Entries[j].AssetID })
	return bundle, nil
}

func (r *Resolver) sessionExists(ctx context.Context, sessionID string) error {
	var found int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE id = ?`, sessionID).Scan(&found); err != nil {
		return fmt.Errorf("baseline: check session %s: %w", sessionID, err)
	}
	if found == 0 {
		return fmt.Errorf("baseline: session %s does not exist", sessionID)
	}
	return nil
}
