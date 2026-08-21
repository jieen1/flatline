package runtime

import (
	"context"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/assets"
	"flatline/internal/storage"
	"flatline/internal/vital"
)

func TestScanFSSnapshotsDiscoveredFilesWithoutSourceMutation(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "scan.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()
	app := New(db, adapters.NewRegistry(), vital.DefaultConfig())
	when := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	files := fstest.MapFS{
		".claude/skills/fixture/SKILL.md": &fstest.MapFile{Data: []byte("synthetic scan v1\n")},
		".claude/rules/style.md":          &fstest.MapFile{Data: []byte("synthetic rule\n")},
		"README.md":                       &fstest.MapFile{Data: []byte("not an asset\n")},
	}
	report, err := app.scanFS(ctx, files, "/synthetic/root", assets.ScopeProject, when)
	if err != nil {
		t.Fatalf("scanFS: %v", err)
	}
	if report.Discovered != 2 || report.SnapshotsObserved != 2 || report.VersionsCreated != 2 {
		t.Fatalf("first report = %+v", report)
	}
	replay, err := app.scanFS(ctx, files, "/synthetic/root", assets.ScopeProject, when.Add(time.Hour))
	if err != nil {
		t.Fatalf("scanFS replay: %v", err)
	}
	if replay.Discovered != 2 || replay.SnapshotsObserved != 2 || replay.VersionsCreated != 0 {
		t.Fatalf("replay report = %+v", replay)
	}
	var versions int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM asset_versions`).Scan(&versions); err != nil {
		t.Fatalf("version count: %v", err)
	}
	if versions != 2 {
		t.Fatalf("versions = %d, want 2", versions)
	}
}
