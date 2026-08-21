package tracking

import (
	"fmt"
	"sort"
	"strings"
)

// ShapeRuleVersion identifies the deterministic task-shape rule in force.
// The version is stored on every opportunity row (shape_rule_version) so a
// classification result can always be explained as "rule vX said: <basis>".
// Bumping the version is a deliberate, documented change (ADR-10: derived
// rows carry the rule version so the whole derived layer can be recomputed).
const ShapeRuleVersion = "shape/1"

// ShapeRule documents the rule (roadmap P3: 形状归类规则文档化).
//
// Rule shape/1 — what counts as a "same-kind task" (同类任务):
//
//   - A session's task shape is the sorted, de-duplicated set of its
//     normalized task tags. Tags are lowercase ASCII; every other rune is
//     replaced with '-'; runs of '-' collapse to one; leading/trailing '-'
//     are trimmed. Empty tags are dropped.
//   - Two sessions are the same shape class iff their tag sets are equal.
//     Order and multiplicity do not matter: a session is a bag of task
//     facets, not a sequence.
//   - The shape class is the canonical string form of the tag set:
//     "shape/1:" + tags joined by "|".
//   - A session with no tags has no shape class: it produces no
//     opportunities. "No shape" is not the empty shape — absence of a
//     classification is never recorded as a class (缺失 ≠ 零).
//
// The rule is deliberately deterministic and dependency-free: the same
// input always yields the same class, and the class string itself is the
// drill-down basis (no hidden model, ADR-8/ADR-9).
const ShapeRule = `shape/1: shape class = sorted unique normalized task tags joined by "|";
tags are lowercased with non-alphanumeric runs collapsed to "-";
a session with no tags has no shape class and yields no opportunities`

// NormalizeTag is the deterministic tag normalization of rule shape/1.
func NormalizeTag(tag string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(tag) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// ClassifyShape applies rule shape/1 to a session's raw task tags.
// It returns the shape class and the human-readable basis, or an error for
// invalid input. An empty tag list yields ("", "", nil): no shape class.
func ClassifyShape(tags []string) (class, basis string, err error) {
	seen := make(map[string]bool, len(tags))
	for _, tag := range tags {
		normalized := NormalizeTag(tag)
		if normalized == "" {
			continue
		}
		seen[normalized] = true
	}
	if len(seen) == 0 {
		return "", "", nil
	}
	normalized := make([]string, 0, len(seen))
	for tag := range seen {
		normalized = append(normalized, tag)
	}
	sort.Strings(normalized)
	class = ShapeRuleVersion + ":" + strings.Join(normalized, "|")
	basis = fmt.Sprintf("%s: tags %s", ShapeRuleVersion, strings.Join(normalized, ", "))
	return class, basis, nil
}
