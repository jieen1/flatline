package adapters

import "testing"

func TestSourceValidityIsRegistryDriven(t *testing.T) {
	for _, source := range []Source{SourceClaudeCode, SourceCodex, SourceOpenCode, SourceDSH, SourceHermes} {
		if !source.Valid() {
			t.Fatalf("built-in source %q is not valid", source)
		}
	}
	// An unregistered source must be rejected, so a typo fails at ingest
	// instead of writing an unattributable session row.
	if Source("not-a-harness").Valid() {
		t.Fatal("an unregistered source must not be valid")
	}
	if Source("").Valid() {
		t.Fatal("the empty source must not be valid")
	}
}

func TestRegisterSourceOpensTheEnumeration(t *testing.T) {
	if err := RegisterSource("demo_harness", "Demo Harness"); err != nil {
		t.Fatalf("RegisterSource: %v", err)
	}
	if !Source("demo_harness").Valid() {
		t.Fatal("registering a source should make it valid")
	}
	if got := Source("demo_harness").DisplayName(); got != "Demo Harness" {
		t.Fatalf("display name = %q", got)
	}
	// Re-registering the same name is what a second adapter registration does;
	// it must not be an error.
	if err := RegisterSource("demo_harness", "Demo Harness"); err != nil {
		t.Fatalf("idempotent registration failed: %v", err)
	}
	if err := RegisterSource("demo_harness", "Something Else"); err == nil {
		t.Fatal("re-registering under a different display name should fail")
	}
	if err := RegisterSource("  ", "blank"); err == nil {
		t.Fatal("an empty source must be rejected")
	}
}

func TestDisplayNamesForKnownSources(t *testing.T) {
	want := map[Source]string{
		SourceClaudeCode: "Claude Code", SourceCodex: "Codex",
		SourceOpenCode: "opencode", SourceDSH: "DeepSeek Harness", SourceHermes: "Hermes",
	}
	for source, name := range want {
		if got := source.DisplayName(); got != name {
			t.Fatalf("%s display name = %q, want %q", source, got, name)
		}
	}
	// An unknown source falls back to its identifier rather than to "".
	if got := Source("mystery").DisplayName(); got != "mystery" {
		t.Fatalf("fallback display name = %q", got)
	}
	if len(KnownSources()) < 5 {
		t.Fatalf("known sources = %v", KnownSources())
	}
}

func TestRegistryAcceptsANewSourceWithoutCodeChange(t *testing.T) {
	registry := NewRegistry()
	adapter := fakeAdapter{source: "brand_new_harness"}
	if err := registry.Register(adapter); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !Source("brand_new_harness").Valid() {
		t.Fatal("registering an adapter must declare its source")
	}
	if _, ok := registry.Get("brand_new_harness"); !ok {
		t.Fatal("lookup failed for the newly registered source")
	}
}
