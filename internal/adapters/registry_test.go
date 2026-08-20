package adapters

import (
	"testing"

	"flatline/internal/canonical"
)

type fakeAdapter struct{ source Source }

func (f fakeAdapter) Source() Source                                { return f.source }
func (f fakeAdapter) Version() string                               { return "test-1" }
func (f fakeAdapter) DetectVersion(RawSession) (VersionInfo, error) { return VersionInfo{}, nil }
func (f fakeAdapter) Parse(RawSession) (SessionMeta, []canonical.Event, error) {
	return SessionMeta{}, nil, nil
}
func (f fakeAdapter) FieldMatrix() FieldMatrix { return FieldMatrix{Supported: []string{"session_id"}} }

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	cc := fakeAdapter{source: SourceClaudeCode}
	codex := fakeAdapter{source: SourceCodex}
	if err := r.Register(cc); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(codex); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(cc); err == nil {
		t.Fatal("duplicate registration should fail")
	}
	if got, ok := r.Get(SourceClaudeCode); !ok || got.Source() != SourceClaudeCode {
		t.Fatal("lookup failed")
	}
	got := r.Sources()
	if len(got) != 2 || got[0] != SourceClaudeCode || got[1] != SourceCodex {
		t.Fatalf("sources = %#v", got)
	}
}

func TestFieldMatrixRejectsOverlap(t *testing.T) {
	if err := (FieldMatrix{Supported: []string{"model"}, Unrecorded: []string{"model"}}).Validate(); err == nil {
		t.Fatal("overlapping field matrix should fail")
	}
}
