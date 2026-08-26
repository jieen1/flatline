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
