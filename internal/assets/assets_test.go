package assets

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"flatline/internal/canonical"
	"flatline/internal/storage"
)

// All inputs in this file are fabricated synthetic fixtures (AGENTS.md §2):
// no real asset paths, no real user content, no network access.

const (
	fixtureContentV1 = "synthetic skill content v1\n"
	fixtureContentV2 = "synthetic skill content v2\n"
)

// Precomputed SHA-256 of the fixture contents (deterministic).
var (
	hashV1 = "sha256:" + sha256Hex([]byte(fixtureContentV1))
	hashV2 = "sha256:" + sha256Hex([]byte(fixtureContentV2))
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func testRegistry(t *testing.T) *Registry {
	t.Helper()
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db)
}

func testTime(day int) time.Time {
	return time.Date(2026, 8, day, 12, 0, 0, 0, time.UTC)
}

func skillInput(name string) AssetInput {
	return AssetInput{
		Kind:        KindSkill,
		Scope:       ScopeUser,
		Name:        name,
		SourcePath:  "/synthetic/fixtures/" + name + "/SKILL.md",
		Description: "synthetic fixture asset",
		FirstSeenAt: testTime(1),
	}
}

func TestRegisterCreatesStableID(t *testing.T) {
	ctx := context.Background()
	reg := testRegistry(t)

	id, err := reg.Register(ctx, skillInput("sql-migrations"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if want := "skill:user:sql-migrations"; id != want {
		t.Fatalf("id = %q, want %q", id, want)
	}

	asset, err := reg.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if asset.Kind != KindSkill || asset.Scope != ScopeUser || asset.Name != "sql-migrations" {
		t.Errorf("asset = %+v, want kind=skill scope=user name=sql-migrations", asset)
	}
	if asset.SourcePath == nil || *asset.SourcePath != "/synthetic/fixtures/sql-migrations/SKILL.md" {
		t.Errorf("SourcePath = %v, want synthetic fixture path", asset.SourcePath)
	}
	if asset.LastSeenAt != nil {
		t.Errorf("LastSeenAt = %v, want nil (not recorded)", asset.LastSeenAt)
	}
	if asset.ArchivedAt != nil {
		t.Errorf("ArchivedAt = %v, want nil (not recorded)", asset.ArchivedAt)
	}
}

func TestRegisterIsIdempotent(t *testing.T) {
	ctx := context.Background()
	reg := testRegistry(t)

	in := skillInput("sql-migrations")
	if _, err := reg.Register(ctx, in); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	// Re-registering the same asset must not duplicate the row.
	if _, err := reg.Register(ctx, in); err != nil {
		t.Fatalf("second Register: %v", err)
	}

	assets, err := reg.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(assets) != 1 {
		t.Fatalf("List = %d assets, want 1 (idempotent)", len(assets))
	}
}

func TestRegisterMissingMetadataStaysNull(t *testing.T) {
	ctx := context.Background()
	reg := testRegistry(t)

	// No source path, no description, no last-seen: all must remain
	// "not recorded" (NULL), never a fabricated value.
	in := AssetInput{Kind: KindRule, Scope: ScopeProject, Name: "no-meta", FirstSeenAt: testTime(1)}
	id, err := reg.Register(ctx, in)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	asset, err := reg.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if asset.SourcePath != nil {
		t.Errorf("SourcePath = %q, want nil (missing metadata must stay unrecorded)", *asset.SourcePath)
	}
	if asset.Description != nil {
		t.Errorf("Description = %q, want nil (missing metadata must stay unrecorded)", *asset.Description)
	}
	if asset.LastSeenAt != nil {
		t.Errorf("LastSeenAt = %v, want nil (missing metadata must stay unrecorded)", *asset.LastSeenAt)
	}
}

func TestRegisterRejectsInvalidInput(t *testing.T) {
	ctx := context.Background()
	reg := testRegistry(t)

	cases := []struct {
		name string
		in   AssetInput
	}{
		{"invalid kind", AssetInput{Kind: "widget", Scope: ScopeUser, Name: "x", FirstSeenAt: testTime(1)}},
		{"invalid scope", AssetInput{Kind: KindSkill, Scope: "org", Name: "x", FirstSeenAt: testTime(1)}},
		{"empty name", AssetInput{Kind: KindSkill, Scope: ScopeUser, Name: "  ", FirstSeenAt: testTime(1)}},
		{"zero first seen", AssetInput{Kind: KindSkill, Scope: ScopeUser, Name: "x"}},
		{"non-utc first seen", AssetInput{Kind: KindSkill, Scope: ScopeUser, Name: "x", FirstSeenAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.Local)}},
	}
	for _, tc := range cases {
		if _, err := reg.Register(ctx, tc.in); err == nil {
			t.Errorf("Register(%s): expected error, got nil", tc.name)
		}
	}
}

func TestRecordVersionCreatesSequentialVersions(t *testing.T) {
	ctx := context.Background()
	reg := testRegistry(t)
	id, err := reg.Register(ctx, skillInput("sql-migrations"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	v1, err := reg.RecordVersion(ctx, VersionInput{
		AssetID:          id,
		Content:          []byte(fixtureContentV1),
		ObservationLevel: canonical.LevelInvoked,
		ObservedAt:       testTime(2),
	})
	if err != nil {
		t.Fatalf("RecordVersion v1: %v", err)
	}
	if !v1.Created {
		t.Error("v1.Created = false, want true")
	}
	if v1.Version != 1 {
		t.Errorf("v1.Version = %d, want 1", v1.Version)
	}
	if v1.ContentHash != hashV1 {
		t.Errorf("v1.ContentHash = %q, want %q", v1.ContentHash, hashV1)
	}
	if v1.ObservationLevel != canonical.LevelInvoked {
		t.Errorf("v1.ObservationLevel = %q, want invoked", v1.ObservationLevel)
	}

	v2, err := reg.RecordVersion(ctx, VersionInput{
		AssetID:          id,
		Content:          []byte(fixtureContentV2),
		ObservationLevel: canonical.LevelObservedUse,
		ObservedAt:       testTime(3),
	})
	if err != nil {
		t.Fatalf("RecordVersion v2: %v", err)
	}
	if !v2.Created || v2.Version != 2 {
		t.Fatalf("v2 = {Created:%v Version:%d}, want {true 2}", v2.Created, v2.Version)
	}
	if v2.ContentHash != hashV2 {
		t.Errorf("v2.ContentHash = %q, want %q", v2.ContentHash, hashV2)
	}

	versions, err := reg.Versions(ctx, id)
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("Versions = %d rows, want 2", len(versions))
	}
	latest, err := reg.LatestVersion(ctx, id)
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if latest.Version != 2 || latest.ContentHash != hashV2 {
		t.Errorf("latest = {Version:%d Hash:%q}, want {2 %q}", latest.Version, latest.ContentHash, hashV2)
	}
}

func TestRecordVersionIsIdempotentForSameContent(t *testing.T) {
	ctx := context.Background()
	reg := testRegistry(t)
	id, err := reg.Register(ctx, skillInput("sql-migrations"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	in := VersionInput{
		AssetID:          id,
		Content:          []byte(fixtureContentV1),
		ObservationLevel: canonical.LevelInvoked,
		ObservedAt:       testTime(2),
	}
	first, err := reg.RecordVersion(ctx, in)
	if err != nil {
		t.Fatalf("first RecordVersion: %v", err)
	}
	// Re-observing identical content must not create a new version.
	second, err := reg.RecordVersion(ctx, in)
	if err != nil {
		t.Fatalf("second RecordVersion: %v", err)
	}
	if second.Created {
		t.Error("second.Created = true, want false (idempotent)")
	}
	if second.ID != first.ID || second.Version != first.Version {
		t.Errorf("second = {ID:%d Version:%d}, want {ID:%d Version:%d}",
			second.ID, second.Version, first.ID, first.Version)
	}

	versions, err := reg.Versions(ctx, id)
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("Versions = %d rows, want 1 (idempotent)", len(versions))
	}
}

func TestRecordVersionPreservesUnknownObservationLevel(t *testing.T) {
	ctx := context.Background()
	reg := testRegistry(t)
	id, err := reg.Register(ctx, skillInput("sql-migrations"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// The source did not record how it knows about this content: the level
	// must stay `unknown`, never coerced to a stronger level.
	v, err := reg.RecordVersion(ctx, VersionInput{
		AssetID:          id,
		Content:          []byte(fixtureContentV1),
		ObservationLevel: canonical.LevelUnknown,
		ObservedAt:       testTime(2),
	})
	if err != nil {
		t.Fatalf("RecordVersion: %v", err)
	}
	if v.ObservationLevel != canonical.LevelUnknown {
		t.Errorf("ObservationLevel = %q, want unknown (preserved verbatim)", v.ObservationLevel)
	}
	stored, err := reg.VersionByHash(ctx, id, hashV1)
	if err != nil {
		t.Fatalf("VersionByHash: %v", err)
	}
	if stored.ObservationLevel != canonical.LevelUnknown {
		t.Errorf("stored ObservationLevel = %q, want unknown", stored.ObservationLevel)
	}
}

func TestRecordVersionRejectsInvalidInput(t *testing.T) {
	ctx := context.Background()
	reg := testRegistry(t)
	id, err := reg.Register(ctx, skillInput("sql-migrations"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	cases := []struct {
		name string
		in   VersionInput
	}{
		{"empty asset id", VersionInput{Content: []byte("x"), ObservationLevel: canonical.LevelInvoked, ObservedAt: testTime(1)}},
		{"empty content", VersionInput{AssetID: id, ObservationLevel: canonical.LevelInvoked, ObservedAt: testTime(1)}},
		{"invalid level", VersionInput{AssetID: id, Content: []byte("x"), ObservationLevel: canonical.ObservationLevel("exact"), ObservedAt: testTime(1)}},
		{"zero observed at", VersionInput{AssetID: id, Content: []byte("x"), ObservationLevel: canonical.LevelInvoked}},
		{"non-utc observed at", VersionInput{AssetID: id, Content: []byte("x"), ObservationLevel: canonical.LevelInvoked, ObservedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.Local)}},
	}
	for _, tc := range cases {
		if _, err := reg.RecordVersion(ctx, tc.in); err == nil {
			t.Errorf("RecordVersion(%s): expected error, got nil", tc.name)
		}
	}
}

func TestRecordVersionRequiresRegisteredAsset(t *testing.T) {
	ctx := context.Background()
	reg := testRegistry(t)
	// Foreign-key enforcement: a version for an unregistered asset must fail.
	_, err := reg.RecordVersion(ctx, VersionInput{
		AssetID:          "skill:user:ghost",
		Content:          []byte(fixtureContentV1),
		ObservationLevel: canonical.LevelInvoked,
		ObservedAt:       testTime(2),
	})
	if err == nil {
		t.Fatal("RecordVersion for unregistered asset: expected foreign-key error, got nil")
	}
}

func TestRecordVersionStoresContentRefWhenProvided(t *testing.T) {
	ctx := context.Background()
	reg := testRegistry(t)
	id, err := reg.Register(ctx, skillInput("sql-migrations"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	ref := "synthetic://snapshots/v1"
	v, err := reg.RecordVersion(ctx, VersionInput{
		AssetID:          id,
		Content:          []byte(fixtureContentV1),
		ObservationLevel: canonical.LevelInvoked,
		ObservedAt:       testTime(2),
		ContentRef:       ref,
	})
	if err != nil {
		t.Fatalf("RecordVersion: %v", err)
	}
	if v.ContentRef == nil || *v.ContentRef != ref {
		t.Errorf("ContentRef = %v, want %q", v.ContentRef, ref)
	}

	// Without a ref the locator stays unrecorded (nil), not an empty string.
	id2, err := reg.Register(ctx, skillInput("no-ref"))
	if err != nil {
		t.Fatalf("Register no-ref: %v", err)
	}
	v2, err := reg.RecordVersion(ctx, VersionInput{
		AssetID:          id2,
		Content:          []byte(fixtureContentV1),
		ObservationLevel: canonical.LevelUnknown,
		ObservedAt:       testTime(2),
	})
	if err != nil {
		t.Fatalf("RecordVersion no-ref: %v", err)
	}
	if v2.ContentRef != nil {
		t.Errorf("ContentRef = %q, want nil (not recorded)", *v2.ContentRef)
	}
}

func TestRecordVersionAdvancesLastSeen(t *testing.T) {
	ctx := context.Background()
	reg := testRegistry(t)
	id, err := reg.Register(ctx, skillInput("sql-migrations"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if _, err := reg.RecordVersion(ctx, VersionInput{
		AssetID:          id,
		Content:          []byte(fixtureContentV1),
		ObservationLevel: canonical.LevelInvoked,
		ObservedAt:       testTime(5),
	}); err != nil {
		t.Fatalf("RecordVersion: %v", err)
	}
	asset, err := reg.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if asset.LastSeenAt == nil || !asset.LastSeenAt.Equal(testTime(5)) {
		t.Errorf("LastSeenAt = %v, want %v", asset.LastSeenAt, testTime(5))
	}

	// An older observation must not regress last_seen_at.
	if _, err := reg.RecordVersion(ctx, VersionInput{
		AssetID:          id,
		Content:          []byte(fixtureContentV2),
		ObservationLevel: canonical.LevelInvoked,
		ObservedAt:       testTime(3),
	}); err != nil {
		t.Fatalf("RecordVersion older: %v", err)
	}
	asset, err = reg.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after older observation: %v", err)
	}
	if asset.LastSeenAt == nil || !asset.LastSeenAt.Equal(testTime(5)) {
		t.Errorf("LastSeenAt = %v, want %v (must not regress)", asset.LastSeenAt, testTime(5))
	}
}

func TestRepeatedVersionObservationAdvancesLastSeen(t *testing.T) {
	ctx := context.Background()
	reg := testRegistry(t)
	id, err := reg.Register(ctx, skillInput("sql-migrations"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	in := VersionInput{
		AssetID: id, Content: []byte(fixtureContentV1),
		ObservationLevel: canonical.LevelInvoked, ObservedAt: testTime(2),
	}
	if _, err := reg.RecordVersion(ctx, in); err != nil {
		t.Fatalf("first RecordVersion: %v", err)
	}
	in.ObservedAt = testTime(6)
	version, err := reg.RecordVersion(ctx, in)
	if err != nil {
		t.Fatalf("repeated RecordVersion: %v", err)
	}
	if version.Created {
		t.Fatal("repeated content created a second version")
	}
	asset, err := reg.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if asset.LastSeenAt == nil || !asset.LastSeenAt.Equal(testTime(6)) {
		t.Fatalf("LastSeenAt = %v, want %v", asset.LastSeenAt, testTime(6))
	}
}

func TestGetAndVersionByHashReturnNoRows(t *testing.T) {
	ctx := context.Background()
	reg := testRegistry(t)

	if _, err := reg.Get(ctx, "skill:user:ghost"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("Get(unknown) err = %v, want sql.ErrNoRows", err)
	}
	if _, err := reg.VersionByHash(ctx, "skill:user:ghost", hashV1); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("VersionByHash(unknown) err = %v, want sql.ErrNoRows", err)
	}
	if _, err := reg.LatestVersion(ctx, "skill:user:ghost"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("LatestVersion(unknown) err = %v, want sql.ErrNoRows", err)
	}
}

func TestListOrdersByID(t *testing.T) {
	ctx := context.Background()
	reg := testRegistry(t)
	for _, name := range []string{"zeta", "alpha"} {
		if _, err := reg.Register(ctx, skillInput(name)); err != nil {
			t.Fatalf("Register %s: %v", name, err)
		}
	}
	assets, err := reg.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(assets) != 2 {
		t.Fatalf("List = %d, want 2", len(assets))
	}
	if assets[0].Name != "alpha" || assets[1].Name != "zeta" {
		t.Errorf("List order = [%s %s], want [alpha zeta]", assets[0].Name, assets[1].Name)
	}
}
