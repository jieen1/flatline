package detectors

import (
	"strings"
	"testing"
	"time"
)

func TestExtractReferencesRequiresExplicitSyntax(t *testing.T) {
	refs := ExtractReferences([]byte("reference: command synthetic-cli\nprose /not-a-reference\nreference: path ./synthetic/file\nreference: tool synthetic-tool\nreference: unknown ignored\n"))
	if len(refs) != 3 {
		t.Fatalf("references = %#v, want three explicit references", refs)
	}
	if refs[0].Kind != ReferenceCommand || refs[1].Kind != ReferencePath || refs[2].Kind != ReferenceTool || refs[1].Line != 3 {
		t.Fatalf("references = %#v, want kind and line evidence", refs)
	}
}

func TestCheckReferencesKeepsUnknownSeparateFromMissing(t *testing.T) {
	missing := false
	verdict, err := CheckReferences("skill:project:fixture", 1, time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC), []ExtractedReference{
		{Kind: ReferenceCommand, Value: "missing", Line: 1, LocatorRef: "fixture:line:1"},
		{Kind: ReferenceTool, Value: "not-checked", Line: 2, LocatorRef: "fixture:line:2"},
	}, ReferenceChecker{CommandExists: func(string) bool { return missing }, ToolExists: nil})
	if err != nil {
		t.Fatalf("CheckReferences: %v", err)
	}
	if !verdict.Triggered || verdict.Failed != 1 || verdict.Unknown != 1 || !strings.Contains(verdict.Summary, "1 of 1") {
		t.Fatalf("verdict = %+v, want one failed and one unknown", verdict)
	}
}
