package assets

import (
	"context"
	"testing"
	"testing/fstest"

	"flatline/internal/canonical"
)

func TestSnapshotterRecordsFileAndUpgradesObservation(t *testing.T) {
	ctx := context.Background()
	reg := testRegistry(t)
	snapshotter := NewSnapshotter(reg)
	input := AssetInput{
		Kind:        KindSkill,
		Scope:       ScopeProject,
		Name:        "synthetic-skill",
		SourcePath:  "skills/synthetic/SKILL.md",
		Description: "synthetic fixture asset",
		FirstSeenAt: testTime(1),
	}
	files := fstest.MapFS{
		"skills/synthetic/SKILL.md": &fstest.MapFile{Data: []byte("synthetic snapshot\n")},
	}

	first, err := snapshotter.SnapshotFile(ctx, files, input, FileSnapshotInput{
		Path:             "skills/synthetic/SKILL.md",
		ObservationLevel: canonical.LevelUnknown,
		ObservedAt:       testTime(2),
		ContentRef:       "synthetic://snapshot/1",
	})
	if err != nil {
		t.Fatalf("first SnapshotFile: %v", err)
	}
	if !first.Created || first.ObservationLevel != canonical.LevelUnknown {
		t.Fatalf("first version = %+v, want created unknown", first)
	}

	second, err := snapshotter.SnapshotFile(ctx, files, input, FileSnapshotInput{
		Path:             "skills/synthetic/SKILL.md",
		ObservationLevel: canonical.LevelInvoked,
		ObservedAt:       testTime(3),
	})
	if err != nil {
		t.Fatalf("second SnapshotFile: %v", err)
	}
	if second.Created || second.ID != first.ID || second.Version != first.Version {
		t.Fatalf("second version = %+v, want same existing version", second)
	}
	if second.ObservationLevel != canonical.LevelInvoked {
		t.Fatalf("upgraded level = %q, want invoked", second.ObservationLevel)
	}

	asset, err := reg.Get(ctx, input.ID())
	if err != nil {
		t.Fatalf("Get asset: %v", err)
	}
	if asset.LastSeenAt == nil || !asset.LastSeenAt.Equal(testTime(3)) {
		t.Fatalf("last seen = %v, want %v", asset.LastSeenAt, testTime(3))
	}
	version, err := reg.VersionByHash(ctx, input.ID(), first.ContentHash)
	if err != nil {
		t.Fatalf("VersionByHash: %v", err)
	}
	if version.ContentRef == nil || *version.ContentRef != "synthetic://snapshot/1" {
		t.Fatalf("content ref = %v, want first recorded locator", version.ContentRef)
	}
}

func TestSnapshotterRejectsInvalidFilePath(t *testing.T) {
	reg := testRegistry(t)
	snapshotter := NewSnapshotter(reg)
	input := AssetInput{Kind: KindRule, Scope: ScopeProject, Name: "rule", FirstSeenAt: testTime(1)}
	files := fstest.MapFS{"rule.txt": &fstest.MapFile{Data: []byte("fixture\n")}}
	for _, path := range []string{"", "/rule.txt", "../rule.txt"} {
		if _, err := snapshotter.SnapshotFile(context.Background(), files, input, FileSnapshotInput{
			Path: path, ObservationLevel: canonical.LevelUnknown, ObservedAt: testTime(1),
		}); err == nil {
			t.Errorf("SnapshotFile path %q: expected error", path)
		}
	}
}
