package detectors

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ExtractedReference is an explicitly declared asset reference. The parser
// accepts only the auditable line form:
// `reference: command <name>`, `reference: path <path>`, or
// `reference: tool <name>`. It deliberately does not guess from arbitrary
// prose or code blocks.
type ExtractedReference struct {
	Kind       ReferenceKind
	Value      string
	Line       int
	LocatorRef string
}

func ExtractReferences(content []byte) []ExtractedReference {
	lines := strings.Split(string(content), "\n")
	out := make([]ExtractedReference, 0)
	for index, raw := range lines {
		line := strings.TrimSpace(raw)
		if len(line) < len("reference:") || !strings.EqualFold(line[:len("reference:")], "reference:") {
			continue
		}
		fields := strings.Fields(strings.TrimSpace(line[len("reference:"):]))
		if len(fields) < 2 {
			continue
		}
		kind := ReferenceKind(strings.ToLower(fields[0]))
		if !kind.Valid() {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line[len("reference:"):]), fields[0]))
		if value == "" {
			continue
		}
		lineNumber := index + 1
		out = append(out, ExtractedReference{Kind: kind, Value: value, Line: lineNumber, LocatorRef: fmt.Sprintf("asset-content:line:%d", lineNumber)})
	}
	return out
}

// ReferenceChecker is injectable so tests never inspect or execute a user's
// environment. Callbacks return only existence, not a semantic health score.
type ReferenceChecker struct {
	CommandExists func(string) bool
	PathExists    func(string) bool
	ToolExists    func(string) bool
}

func NewLocalReferenceChecker(root string) ReferenceChecker {
	return ReferenceChecker{
		CommandExists: func(value string) bool { _, err := exec.LookPath(value); return err == nil },
		PathExists: func(value string) bool {
			if !filepath.IsAbs(value) && root != "" {
				value = filepath.Join(root, value)
			}
			_, err := os.Stat(value)
			return err == nil
		},
		ToolExists: func(value string) bool { _, err := exec.LookPath(value); return err == nil },
	}
}

// CheckReferences evaluates extracted references with injected existence
// functions and returns the same explainable verdict used by the state
// machine. A nil callback makes that reference unknown; it does not fail it.
func CheckReferences(assetID string, versionID int64, checkedAt time.Time, refs []ExtractedReference, checker ReferenceChecker) (ReferenceVerdict, error) {
	items, err := ObserveReferences(refs, checker)
	if err != nil {
		return ReferenceVerdict{}, err
	}
	return EvaluateReferenceHealth(ReferenceInput{AssetID: assetID, AssetVersionID: versionID, CheckedAt: checkedAt, Items: items})
}

// ObserveReferences returns the individual rows that may be persisted in
// reference_check_items. A nil Exists pointer is intentionally retained as
// SQL NULL by the caller (ADR-13).
func ObserveReferences(refs []ExtractedReference, checker ReferenceChecker) ([]ReferenceObservation, error) {
	items := make([]ReferenceObservation, 0, len(refs))
	for _, ref := range refs {
		if !ref.Kind.Valid() || strings.TrimSpace(ref.Value) == "" || ref.Line <= 0 {
			return nil, fmt.Errorf("detectors: invalid extracted reference")
		}
		item := ReferenceObservation{Kind: ref.Kind, Value: ref.Value, LocatorRef: ref.LocatorRef}
		var exists func(string) bool
		switch ref.Kind {
		case ReferenceCommand:
			exists = checker.CommandExists
		case ReferencePath:
			exists = checker.PathExists
		case ReferenceTool:
			exists = checker.ToolExists
		}
		if exists != nil {
			value := exists(ref.Value)
			item.Exists = &value
			item.Known = true
		}
		items = append(items, item)
	}
	return items, nil
}
