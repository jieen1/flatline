// Package assets implements the P3 Asset Registry: registration of assets
// and creation of asset versions from synthetic (caller-supplied) content
// observations.
//
// Design basis: system design v0.4 §6 (Asset Snapshotter → Asset Registry)
// and §7.1 (Asset / AssetVersion objects), roadmap P3.
//
// Evidence discipline (AGENTS.md §2):
//   - Content hashes are deterministic SHA-256 over the exact bytes supplied
//     by the caller; nothing is invented when the caller omits a field.
//   - `observation_level` is the closed canonical enum (design §3.1) and is
//     carried verbatim on every version row; `unknown` is never coerced to
//     another level or treated as zero.
//   - Missing metadata (source path, description, last-seen time, content
//     locator) is stored as SQL NULL — "not recorded", never a fabricated
//     value.
//
// This package performs no filesystem scanning and no writes to asset source
// files (AGENTS.md §3); the caller supplies the observed content bytes.
package assets

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"flatline/internal/canonical"
	"flatline/internal/storage"
)

// Kind is the asset kind, matching the assets.kind CHECK constraint.
type Kind string

const (
	KindSkill    Kind = "skill"
	KindAgentsMD Kind = "agents_md"
	KindRule     Kind = "rule"
	KindHook     Kind = "hook"
)

func (k Kind) Valid() bool {
	switch k {
	case KindSkill, KindAgentsMD, KindRule, KindHook:
		return true
	default:
		return false
	}
}

// Scope is the asset scope, matching the assets.scope CHECK constraint.
type Scope string

const (
	ScopeUser    Scope = "user"
	ScopeProject Scope = "project"
)

func (s Scope) Valid() bool { return s == ScopeUser || s == ScopeProject }

// Asset is a registered asset row. Pointer fields are nil when the source
// did not record the value (missing ≠ zero).
type Asset struct {
	ID          string
	Kind        Kind
	Name        string
	Scope       Scope
	SourcePath  *string
	Description *string
	FirstSeenAt time.Time
	LastSeenAt  *time.Time
	ArchivedAt  *time.Time
}

// AssetInput is one synthetic observation of an asset. SourcePath and
// Description may be empty (stored as NULL); LastSeenAt may be nil.
type AssetInput struct {
	Kind        Kind
	Scope       Scope
	Name        string
	SourcePath  string
	Description string
	FirstSeenAt time.Time
	LastSeenAt  *time.Time
}

// ID derives the stable asset id: kind:scope:name.
func (in AssetInput) ID() string {
	return string(in.Kind) + ":" + string(in.Scope) + ":" + in.Name
}

func (in AssetInput) Validate() error {
	if !in.Kind.Valid() {
		return fmt.Errorf("assets: invalid kind %q", in.Kind)
	}
	if !in.Scope.Valid() {
		return fmt.Errorf("assets: invalid scope %q", in.Scope)
	}
	if strings.TrimSpace(in.Name) == "" {
		return fmt.Errorf("assets: name is required")
	}
	if in.FirstSeenAt.IsZero() {
		return fmt.Errorf("assets: first_seen_at is required")
	}
	if in.FirstSeenAt.Location() != time.UTC {
		return fmt.Errorf("assets: first_seen_at must be UTC")
	}
	if in.LastSeenAt != nil && in.LastSeenAt.Location() != time.UTC {
		return fmt.Errorf("assets: last_seen_at must be UTC")
	}
	return nil
}

// AssetVersion is one content snapshot of an asset. ContentRef is nil when
// no locator to the stored snapshot content was recorded.
type AssetVersion struct {
	ID               int64
	AssetID          string
	Version          int
	ContentHash      string
	ContentRef       *string
	ObservationLevel canonical.ObservationLevel
	ObservedAt       time.Time
	// Created reports whether this call inserted a new row (true) or
	// returned an already-registered version with identical content
	// (false).
	Created bool
}

// VersionInput is one synthetic content observation for an asset.
type VersionInput struct {
	AssetID          string
	Content          []byte
	ObservationLevel canonical.ObservationLevel
	ObservedAt       time.Time
	ContentRef       string // optional locator to the stored snapshot content
}

// ContentHash returns the deterministic content hash: "sha256:<hex>" over
// the exact supplied bytes.
func (in VersionInput) ContentHash() string {
	sum := sha256.Sum256(in.Content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (in VersionInput) Validate() error {
	if in.AssetID == "" {
		return fmt.Errorf("assets: asset_id is required")
	}
	if in.Content == nil {
		return fmt.Errorf("assets: content is required (nil content means the source was not read)")
	}
	if !in.ObservationLevel.Valid() {
		return fmt.Errorf("assets: invalid observation level %q", in.ObservationLevel)
	}
	if in.ObservedAt.IsZero() {
		return fmt.Errorf("assets: observed_at is required")
	}
	if in.ObservedAt.Location() != time.UTC {
		return fmt.Errorf("assets: observed_at must be UTC")
	}
	return nil
}

// Registry persists assets and asset versions against the existing
// assets / asset_versions tables.
type Registry struct{ db *storage.DB }

func New(db *storage.DB) *Registry { return &Registry{db: db} }

// ObservationRank orders evidence strength for the snapshot upgrader. The
// order is explicit and closed; it is not a quality score and unknown never
// becomes a stronger fact without a new observation.
func ObservationRank(level canonical.ObservationLevel) int {
	switch level {
	case canonical.LevelInvoked:
		return 6
	case canonical.LevelObservedUse:
		return 5
	case canonical.LevelLoaded:
		return 4
	case canonical.LevelOffered:
		return 3
	case canonical.LevelInferred:
		return 2
	case canonical.LevelUnknown:
		return 1
	default:
		return 0
	}
}

// Register upserts the asset row, keyed by the stable id kind:scope:name.
// Re-registering the same asset is idempotent: the row is not duplicated,
// and fields the new observation leaves empty are left as previously
// recorded (missing metadata is never cleared or invented).
func (r *Registry) Register(ctx context.Context, in AssetInput) (string, error) {
	if err := in.Validate(); err != nil {
		return "", err
	}
	id := in.ID()
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO assets (id, kind, name, scope, source_path, description, first_seen_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			last_seen_at = COALESCE(excluded.last_seen_at, assets.last_seen_at),
			source_path  = COALESCE(excluded.source_path, assets.source_path),
			description  = COALESCE(excluded.description, assets.description)`,
		id, in.Kind, in.Name, in.Scope,
		nullableString(in.SourcePath), nullableString(in.Description),
		formatTime(in.FirstSeenAt), nullableTime(in.LastSeenAt))
	if err != nil {
		return "", fmt.Errorf("assets: register %s: %w", id, err)
	}
	return id, nil
}

// RecordVersion creates a new asset version for the observed content, or
// returns the existing version when the same content hash is already
// registered for the asset (idempotent: repeated observations of unchanged
// content do not create duplicate versions).
//
// The observation level is stored verbatim; a version observed at level
// `unknown` stays `unknown`.
func (r *Registry) RecordVersion(ctx context.Context, in VersionInput) (*AssetVersion, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	hash := in.ContentHash()

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("assets: begin version transaction: %w", err)
	}
	fail := func(err error) (*AssetVersion, error) {
		_ = tx.Rollback()
		return nil, err
	}

	// Idempotency and version numbering must be decided in one transaction.
	// The unique (asset_id, content_hash) index is the database-level guard for
	// concurrent replays; the conflict branch below returns its existing row.
	existing, err := scanVersion(tx.QueryRowContext(ctx, `
		SELECT id, asset_id, version, content_hash, content_ref, observation_level, observed_at
		FROM asset_versions WHERE asset_id = ? AND content_hash = ?`, in.AssetID, hash))
	if err == nil {
		if err := updateLastSeen(ctx, tx, in.AssetID, in.ObservedAt); err != nil {
			return fail(err)
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("assets: commit repeated version for %s: %w", in.AssetID, err)
		}
		existing.Created = false
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fail(fmt.Errorf("assets: find version for %s: %w", in.AssetID, err))
	}

	var maxVersion int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM asset_versions WHERE asset_id = ?`,
		in.AssetID).Scan(&maxVersion); err != nil {
		return fail(fmt.Errorf("assets: next version for %s: %w", in.AssetID, err))
	}

	result, err := tx.ExecContext(ctx, `
		INSERT INTO asset_versions (asset_id, version, content_hash, content_ref, observation_level, observed_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (asset_id, content_hash) DO NOTHING`,
		in.AssetID, maxVersion+1, hash, nullableString(in.ContentRef),
		string(in.ObservationLevel), formatTime(in.ObservedAt))
	if err != nil {
		return fail(fmt.Errorf("assets: record version for %s: %w", in.AssetID, err))
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fail(fmt.Errorf("assets: count version for %s: %w", in.AssetID, err))
	}
	if rows == 0 {
		existing, err = scanVersion(tx.QueryRowContext(ctx, `
			SELECT id, asset_id, version, content_hash, content_ref, observation_level, observed_at
			FROM asset_versions WHERE asset_id = ? AND content_hash = ?`, in.AssetID, hash))
		if err != nil {
			return fail(fmt.Errorf("assets: load conflicted version for %s: %w", in.AssetID, err))
		}
		if err := updateLastSeen(ctx, tx, in.AssetID, in.ObservedAt); err != nil {
			return fail(err)
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("assets: commit conflicted version for %s: %w", in.AssetID, err)
		}
		existing.Created = false
		return existing, nil
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fail(fmt.Errorf("assets: version id for %s: %w", in.AssetID, err))
	}
	if err := updateLastSeen(ctx, tx, in.AssetID, in.ObservedAt); err != nil {
		return fail(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("assets: commit version for %s: %w", in.AssetID, err)
	}

	return &AssetVersion{
		ID:               id,
		AssetID:          in.AssetID,
		Version:          maxVersion + 1,
		ContentHash:      hash,
		ContentRef:       nullableStringPtr(in.ContentRef),
		ObservationLevel: in.ObservationLevel,
		ObservedAt:       in.ObservedAt,
		Created:          true,
	}, nil
}

// UpgradeObservation records stronger evidence for an existing immutable
// content snapshot. It never changes content_hash, version, or observed_at.
// An optional content reference only fills a previously missing locator.
func (r *Registry) UpgradeObservation(ctx context.Context, versionID int64, level canonical.ObservationLevel, contentRef string) error {
	if versionID <= 0 {
		return fmt.Errorf("assets: version id must be positive")
	}
	if !level.Valid() {
		return fmt.Errorf("assets: invalid observation level %q", level)
	}
	var current string
	var currentRef sql.NullString
	if err := r.db.QueryRowContext(ctx, `
		SELECT observation_level, content_ref FROM asset_versions WHERE id = ?`, versionID).
		Scan(&current, &currentRef); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sql.ErrNoRows
		}
		return fmt.Errorf("assets: load version %d for observation upgrade: %w", versionID, err)
	}
	if ObservationRank(level) <= ObservationRank(canonical.ObservationLevel(current)) && currentRef.Valid {
		return nil
	}
	newLevel := current
	if ObservationRank(level) > ObservationRank(canonical.ObservationLevel(current)) {
		newLevel = string(level)
	}
	var ref any
	if !currentRef.Valid && strings.TrimSpace(contentRef) != "" {
		ref = contentRef
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE asset_versions
		SET observation_level = ?, content_ref = COALESCE(content_ref, ?)
		WHERE id = ?`, newLevel, ref, versionID)
	if err != nil {
		return fmt.Errorf("assets: upgrade observation for version %d: %w", versionID, err)
	}
	return nil
}

// Get returns the asset with the given id, or sql.ErrNoRows.
func (r *Registry) Get(ctx context.Context, id string) (*Asset, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, kind, name, scope, source_path, description, first_seen_at, last_seen_at, archived_at
		FROM assets WHERE id = ?`, id)
	asset, err := scanAsset(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("assets: get %s: %w", id, err)
	}
	return asset, nil
}

// List returns all registered assets ordered by id.
func (r *Registry) List(ctx context.Context) ([]Asset, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, kind, name, scope, source_path, description, first_seen_at, last_seen_at, archived_at
		FROM assets ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("assets: list: %w", err)
	}
	defer rows.Close()
	var out []Asset
	for rows.Next() {
		asset, err := scanAsset(rows)
		if err != nil {
			return nil, fmt.Errorf("assets: scan asset: %w", err)
		}
		out = append(out, *asset)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("assets: iterate assets: %w", err)
	}
	return out, nil
}

// Versions returns all versions of an asset in version order.
func (r *Registry) Versions(ctx context.Context, assetID string) ([]AssetVersion, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, asset_id, version, content_hash, content_ref, observation_level, observed_at
		FROM asset_versions WHERE asset_id = ? ORDER BY version`, assetID)
	if err != nil {
		return nil, fmt.Errorf("assets: versions for %s: %w", assetID, err)
	}
	defer rows.Close()
	var out []AssetVersion
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, fmt.Errorf("assets: scan version: %w", err)
		}
		out = append(out, *v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("assets: iterate versions: %w", err)
	}
	return out, nil
}

// VersionByHash returns the version of an asset with the given content hash,
// or sql.ErrNoRows when that content has not been observed for the asset.
func (r *Registry) VersionByHash(ctx context.Context, assetID, contentHash string) (*AssetVersion, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, asset_id, version, content_hash, content_ref, observation_level, observed_at
		FROM asset_versions WHERE asset_id = ? AND content_hash = ?`, assetID, contentHash)
	v, err := scanVersion(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("assets: version by hash for %s: %w", assetID, err)
	}
	return v, nil
}

// LatestVersion returns the highest version of an asset, or sql.ErrNoRows
// when the asset has no versions yet.
func (r *Registry) LatestVersion(ctx context.Context, assetID string) (*AssetVersion, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, asset_id, version, content_hash, content_ref, observation_level, observed_at
		FROM asset_versions WHERE asset_id = ? ORDER BY version DESC LIMIT 1`, assetID)
	v, err := scanVersion(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("assets: latest version for %s: %w", assetID, err)
	}
	return v, nil
}

type scanner interface{ Scan(...any) error }

func scanAsset(row scanner) (*Asset, error) {
	var (
		a           Asset
		kind, name  string
		scope       string
		sourcePath  sql.NullString
		description sql.NullString
		firstSeen   string
		lastSeen    sql.NullString
		archivedAt  sql.NullString
	)
	if err := row.Scan(&a.ID, &kind, &name, &scope, &sourcePath, &description, &firstSeen, &lastSeen, &archivedAt); err != nil {
		return nil, err
	}
	a.Kind = Kind(kind)
	a.Name = name
	a.Scope = Scope(scope)
	if sourcePath.Valid {
		a.SourcePath = &sourcePath.String
	}
	if description.Valid {
		a.Description = &description.String
	}
	var err error
	if a.FirstSeenAt, err = parseTime(firstSeen); err != nil {
		return nil, fmt.Errorf("decode first_seen_at: %w", err)
	}
	if lastSeen.Valid {
		value, err := parseTime(lastSeen.String)
		if err != nil {
			return nil, fmt.Errorf("decode last_seen_at: %w", err)
		}
		a.LastSeenAt = &value
	}
	if archivedAt.Valid {
		value, err := parseTime(archivedAt.String)
		if err != nil {
			return nil, fmt.Errorf("decode archived_at: %w", err)
		}
		a.ArchivedAt = &value
	}
	return &a, nil
}

func scanVersion(row scanner) (*AssetVersion, error) {
	var (
		v          AssetVersion
		level      string
		observedAt string
		contentRef sql.NullString
	)
	if err := row.Scan(&v.ID, &v.AssetID, &v.Version, &v.ContentHash, &contentRef, &level, &observedAt); err != nil {
		return nil, err
	}
	v.ObservationLevel = canonical.ObservationLevel(level)
	if contentRef.Valid {
		v.ContentRef = &contentRef.String
	}
	var err error
	if v.ObservedAt, err = parseTime(observedAt); err != nil {
		return nil, fmt.Errorf("decode observed_at: %w", err)
	}
	return &v, nil
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nullableStringPtr(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}

func updateLastSeen(ctx context.Context, tx *sql.Tx, assetID string, observedAt time.Time) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE assets
		SET last_seen_at = ?
		WHERE id = ? AND (last_seen_at IS NULL OR last_seen_at < ?)`,
		formatTime(observedAt), assetID, formatTime(observedAt)); err != nil {
		return fmt.Errorf("assets: advance last_seen_at for %s: %w", assetID, err)
	}
	return nil
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseTime(value string) (time.Time, error) { return time.Parse(time.RFC3339Nano, value) }
