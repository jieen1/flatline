package assets

import (
	"testing"
	"testing/fstest"
	"time"

	"flatline/internal/canonical"
)

func TestDiscoverRecognizesOnlyDeclaredAssetKinds(t *testing.T) {
	files := fstest.MapFS{
		"project/.claude/skills/migrations/SKILL.md": &fstest.MapFile{Data: []byte("synthetic skill\n")},
		"project/AGENTS.md":                          &fstest.MapFile{Data: []byte("synthetic agents\n")},
		"project/.claude/rules/style.md":             &fstest.MapFile{Data: []byte("synthetic rule\n")},
		"project/.claude/hooks/check.sh":             &fstest.MapFile{Data: []byte("synthetic hook\n")},
		"project/README.md":                          &fstest.MapFile{Data: []byte("not an asset\n")},
		"project/.git/config":                        &fstest.MapFile{Data: []byte("ignored\n")},
	}
	when := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	discovered, err := Discover(files, DiscoveryOptions{Scope: ScopeProject, RootLabel: "/synthetic/project", FirstSeenAt: when, ObservedAt: when, ObservationLevel: canonical.LevelLoaded, ContentRefPrefix: "fixture:asset"})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(discovered) != 4 {
		t.Fatalf("discovered = %d, want 4: %#v", len(discovered), discovered)
	}
	seen := make(map[Kind]string)
	for _, item := range discovered {
		seen[item.Asset.Kind] = item.Asset.Name
		if item.Asset.SourcePath == "" || item.ContentRef == "" {
			t.Fatalf("locators missing for %#v", item)
		}
	}
	for _, kind := range []Kind{KindSkill, KindAgentsMD, KindRule, KindHook} {
		if seen[kind] == "" {
			t.Fatalf("kind %q missing from %#v", kind, seen)
		}
	}
}
