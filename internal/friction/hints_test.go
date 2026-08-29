package friction

import "testing"

func TestLookupHintNamesTheMechanism(t *testing.T) {
	cases := []struct {
		name      string
		signature string
		kind      string
	}{
		{"a missing binary is an environment fact", "command_not_found|Bash|bash: line #: ruff: command not found", HintEnvironment},
		{"a missing python package is an environment fact", "nonzero_exit|Bash|modulenotfounderror: no module named 'pydantic'", HintEnvironment},
		{"the read-before-edit rule belongs to the harness", "tool_error|Edit|<tool_use_error>file has not been read yet. read it first…", HintHarnessRule},
		{"a stale old_string is tool misuse", "tool_input_invalid|Edit|string to replace not found in file.", HintToolMisuse},
		{"a pretooluse block is the user's own hook", "tool_error|exec_command|command blocked by pretooluse hook: git commit is blocked.", HintUserHook},
		{"an OS refusal is a permission fact", "permission_denied|Bash|permissionerror: [errno #] operation not permitted: 'x'", HintPermission},
		{"a harness timeout is a timeout", "timeout|Bash|command timed out after #m #s", HintTimeout},
		{"a missing path is tool misuse", "file_not_found|Bash|sed: can't read readme.md: no such file or directory", HintToolMisuse},
		{"a test category needs no literal", "test_failure|Bash|--- fail: testthing (#.#s)", HintTest},
		{"a build category needs no literal", "build_error|Bash|./main.go:#:#: undefined: x", HintBuild},
		{"an auto-classifier refusal is a permission fact", "tool_error|Bash|permission for this action was denied by the claude code auto mode classifier. reason:", HintPermission},
		{"a missing exec cell is a stale reference", "tool_error|wait|exec cell # not found", HintToolMisuse},
		{"an unknown process id is a stale reference", "tool_error|exec|write_stdin failed: unknown process id #", HintToolMisuse},
		{"apply_patch context mismatch is tool misuse", "tool_error|exec|apply_patch verification failed: failed to find expected lines in app.js:", HintToolMisuse},
		{"the agent thread cap is a harness rule", "tool_error|exec|collab spawn failed: agent thread limit reached", HintHarnessRule},
		{"exit 124 is the timeout convention", "nonzero_exit|write_stdin|write_stdin exit 124", HintTimeout},
		{"exit 144 is a signal death", "nonzero_exit|Bash|bash exit 144", HintEnvironment},
		{"exit 143 is a signal death", "nonzero_exit|exec_command|pkill exit 143", HintEnvironment},
		{"rg exit 2 is an rg or shell error", "nonzero_exit|exec_command|rg exit 2", HintToolMisuse},
		{"git exit 2 is a git error", "nonzero_exit|exec_command|git exit 2", HintToolMisuse},
		{"ls exit 2 is a listing failure", "nonzero_exit|exec_command|ls exit 2", HintToolMisuse},
		{"a linter's found-N-errors line is a self-reported build failure", "nonzero_exit|exec_command|found # errors.", HintBuild},
		{"a python syntax error is a parse failure", "nonzero_exit|exec_command|syntaxerror: unterminated string literal (detected at line #)", HintBuild},
		// The rules below were added on 2026-08-29 from the signatures that
		// recur most on real local history while carrying no mechanism. Each
		// literal is the recorded sample line of a signature seen here.
		{"an explicit user rejection is a permission fact",
			"tool_error|Bash|the user doesn't want to proceed with this tool use. the tool use was rejected", HintPermission},
		{"a rejection of a question tool is the same fact",
			"tool_error|AskUserQuestion|the user doesn't want to proceed with this tool use. the tool use was rejected", HintPermission},
		{"an unreachable auto-mode model is a permission fact",
			"tool_error|Bash|claude-sonnet-#[#m] is temporarily unavailable (connection failed), so auto mode cannot determine the safety of bash right now.", HintPermission},
		{"the same fact when the classifier call timed out",
			"timeout|Bash|claude-sonnet-#[#m] is temporarily unavailable (timed out), so auto mode cannot determine the safety of bash right now.", HintPermission},
		{"a branch held by another worktree is tool misuse",
			"nonzero_exit|Bash|fatal: 'feature/issue-#' is already used by worktree at 'cog-#-dev'", HintToolMisuse},
		{"grep exit 2 is a grep error",
			"nonzero_exit|Bash|grep exit 2", HintToolMisuse},
		{"pgrep exit 2 is the same convention",
			"nonzero_exit|Bash|pgrep exit 2", HintToolMisuse},
		{"a dropped MCP connection is an environment fact",
			"tool_error|mcp__playwright__browser_run_code|mcp error -#: connection closed", HintEnvironment},
		{"a dotted checklist reporting failed is a self-reported check failure",
			"nonzero_exit|Bash|backend · ruff (format + lint, auto-fix).................................failed", HintBuild},
		// The category the classifier already decided outranks any rule that
		// reads the evidence line. A failing test whose runner happens to print
		// a dot-aligned line is still a test, not a build.
		{"a test category outranks the dotted-checklist line",
			"test_failure|Bash|tests/test_api.py::test_login.....................................failed", HintTest},
		{"a build category outranks the dotted-checklist line",
			"build_error|Bash|compile · cargo........................................................failed", HintBuild},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hint := LookupHint(tc.signature)
			if hint == nil {
				t.Fatalf("LookupHint(%q) = nil, want kind %q", tc.signature, tc.kind)
			}
			if hint.Kind != tc.kind {
				t.Fatalf("LookupHint(%q).Kind = %q, want %q", tc.signature, hint.Kind, tc.kind)
			}
			if hint.Mechanism == "" {
				t.Fatalf("LookupHint(%q) has no mechanism sentence", tc.signature)
			}
		})
	}
}

func TestLookupHintIsClosedAndSaysNothingWhenItDoesNotKnow(t *testing.T) {
	for _, signature := range []string{"", "nonzero_exit|Bash|make exit #", "tool_error|Read|something else entirely"} {
		if hint := LookupHint(signature); hint != nil {
			t.Fatalf("LookupHint(%q) = %+v, want nil for a signature no rule covers", signature, hint)
		}
	}
	known := make(map[string]bool, len(HintKinds))
	for _, kind := range HintKinds {
		known[kind] = true
	}
	for _, rule := range hintRules {
		if !known[rule.kind] {
			t.Fatalf("rule %q uses kind %q, which is outside the closed set", rule.match.String(), rule.kind)
		}
	}
}

// A rule added in one language only would leave the English pages showing the
// Chinese sentence, which is the thing this dictionary exists to avoid.
func TestEveryHintRuleIsWrittenInBothLanguages(t *testing.T) {
	for _, rule := range hintRules {
		if rule.mechanism == "" || rule.mechanismEN == "" {
			t.Errorf("hint rule %q has mechanism %q / %q; both are required",
				rule.match.String(), rule.mechanism, rule.mechanismEN)
		}
	}
}

// KeywordRules is the adherence curve's source of truth (P17-1): the harness
// mechanisms a user rule could restate. Every entry must carry keywords and
// recognize its own signatures, or a curve could be drawn for a mechanism
// that has nothing to match.
func TestKeywordRulesExposeHarnessMechanisms(t *testing.T) {
	rules := KeywordRules()
	if len(rules) == 0 {
		t.Fatal("KeywordRules() is empty; the adherence curve has no mechanisms to draw")
	}
	foundReadBeforeEdit := false
	for _, rule := range rules {
		if len(rule.Keywords) == 0 {
			t.Errorf("rule %q carries no keywords; it cannot be mentioned by a user rule", rule.Mechanism)
		}
		if rule.Mechanism == "" || rule.MechanismEN == "" {
			t.Errorf("rule %+v is missing a mechanism sentence", rule)
		}
		if rule.Matches("tool_error|Edit|<tool_use_error>file has not been read yet. read it first…") {
			foundReadBeforeEdit = true
			if rule.Kind != HintHarnessRule {
				t.Errorf("read-before-edit rule kind = %q, want harness_rule", rule.Kind)
			}
		}
	}
	if !foundReadBeforeEdit {
		t.Error("no keyword rule matches the read-before-edit signature")
	}
}
