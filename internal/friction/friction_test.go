package friction

import (
	"strings"
	"testing"
)

func TestClassifyCoversEveryCategoryAndItsBoundary(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		tool     string
		payload  map[string]any
		category string
		rule     string
	}{
		{
			name:     "user interrupt from message text",
			kind:     KindUserInterrupt,
			payload:  map[string]any{"text": "[Request interrupted by user]"},
			category: CategoryUserInterrupt,
			rule:     `消息文本以 "[Request interrupted by user" 开头`,
		},
		{
			name:     "user interrupt recognised from text alone",
			kind:     KindToolError,
			payload:  map[string]any{"text": "[Request interrupted by user for tool use]"},
			category: CategoryUserInterrupt,
		},
		{
			name:     "interrupt phrase in the middle is not an interrupt",
			kind:     KindToolError,
			payload:  map[string]any{"text": "the log said [Request interrupted by user]", "is_error": true, "tool_output": "no signal"},
			category: CategoryToolError,
		},
		{
			name:     "permission denied",
			kind:     KindToolError,
			tool:     "Bash",
			payload:  map[string]any{"tool_output": "mkdir: Permission denied", "exit_code": 1},
			category: CategoryPermissionDenied,
			rule:     `输出包含 "permission denied"`,
		},
		{
			name:     "command not found needs a shell command",
			kind:     KindToolError,
			tool:     "Bash",
			payload:  map[string]any{"tool_output": "/bin/bash: line 1: gofmt: command not found", "exit_code": 127},
			category: CategoryCommandNotFound,
			rule:     `输出包含 "command not found"`,
		},
		{
			name:     "a missing file under ls is a file_not_found, not a command_not_found",
			kind:     KindToolError,
			tool:     "Bash",
			payload:  map[string]any{"tool_input": `{"command":"ls /nope"}`, "tool_output": "ls: cannot access '/nope': No such file or directory\nExit code 2", "exit_code": 2},
			category: CategoryFileNotFound,
			rule:     `输出包含 "no such file"`,
		},
		{
			name:     "windows shell wording is a command_not_found",
			kind:     KindToolError,
			tool:     "Bash",
			payload:  map[string]any{"tool_output": "'gofmt' is not recognized as an internal or external command", "exit_code": 1},
			category: CategoryCommandNotFound,
			rule:     `输出包含 "not recognized as an internal"`,
		},
		{
			name:     "exit code 127 alone is a command_not_found even with no linked tool",
			kind:     KindToolError,
			payload:  map[string]any{"tool_output": "Chunk ID: 1a\nProcess exited with code 127\nOutput:\nbash: ripgrep: not found", "exit_code": 127},
			category: CategoryCommandNotFound,
			rule:     "明确记录 exit_code=127（shell 的“命令未找到”退出码）",
		},
		{
			name:     "same text without a shell tool falls through",
			kind:     KindToolError,
			tool:     "Read",
			payload:  map[string]any{"tool_output": "No such file or directory", "is_error": true},
			category: CategoryFileNotFound,
			rule:     `输出包含 "no such file"`,
		},
		{
			name:     "file not found",
			kind:     KindToolError,
			tool:     "Read",
			payload:  map[string]any{"tool_output": "File does not exist. Note: your current working directory is /tmp.", "is_error": true},
			category: CategoryFileNotFound,
		},
		{
			name:     "tool input invalid",
			kind:     KindToolError,
			tool:     "Edit",
			payload:  map[string]any{"tool_output": "<tool_use_error>String to replace not found in file.</tool_use_error>", "is_error": true},
			category: CategoryToolInputInvalid,
			rule:     `输出包含 "String to replace not found"`,
		},
		{
			name:     "timeout wins over the non-zero exit code",
			kind:     KindToolError,
			tool:     "Bash",
			payload:  map[string]any{"tool_output": "Command timed out after 2m 0s", "exit_code": 143},
			category: CategoryTimeout,
			rule:     `输出包含 "timed out"`,
		},
		{
			name:     "network error",
			kind:     KindToolError,
			tool:     "Bash",
			payload:  map[string]any{"tool_output": "curl: (7) Failed to connect: ECONNREFUSED", "exit_code": 7},
			category: CategoryNetworkError,
			rule:     `输出包含 "ECONNREFUSED"`,
		},
		{
			name:     "test failure needs a test command",
			kind:     KindToolError,
			tool:     "Bash",
			payload:  map[string]any{"tool_input": `{"command":"go test ./..."}`, "tool_output": "--- FAIL: TestThing (0.01s)", "exit_code": 1},
			category: CategoryTestFailure,
			rule:     `工具是测试命令且输出包含 "FAIL"`,
		},
		{
			name:     "the same output under a non-test command stays a non-zero exit",
			kind:     KindToolError,
			tool:     "Bash",
			payload:  map[string]any{"tool_input": `{"command":"cat failures.log"}`, "tool_output": "--- FAIL: TestThing (0.01s)", "exit_code": 1},
			category: CategoryNonzeroExit,
			rule:     "明确记录 exit_code=1 且未命中更具体规则",
		},
		{
			name:     "ripgrep exit 1 is no match, not a failure",
			kind:     KindToolError,
			tool:     "Bash",
			payload:  map[string]any{"tool_input": `{"command":"rg -n needle src/"}`, "tool_output": "", "exit_code": 1},
			category: CategoryExpectedExit,
			rule:     "搜索命令没有匹配到内容（退出码 1 是它的“无匹配”约定）",
		},
		{
			name:     "an exit code timeout reported is a timeout, whatever ran",
			kind:     KindToolError,
			tool:     "Bash",
			payload:  map[string]any{"tool_input": `{"command":"timeout 5 ./server"}`, "tool_output": "", "exit_code": 124},
			category: CategoryTimeout,
			rule:     "timeout 在给定时限内中止了这条命令（退出码 124 是 timeout 的超时约定）",
		},
		{
			name:     "build error needs a build command",
			kind:     KindToolError,
			tool:     "Bash",
			payload:  map[string]any{"tool_input": `{"command":"go build ./..."}`, "tool_output": "main.go:3:2: error: undefined x", "exit_code": 2},
			category: CategoryBuildError,
			rule:     `工具是构建命令且输出包含 "error:"`,
		},
		{
			name:     "the same output under a non-build command stays a non-zero exit",
			kind:     KindToolError,
			tool:     "Bash",
			payload:  map[string]any{"tool_input": `{"command":"cat build.log"}`, "tool_output": "main.go:3:2: error: undefined x", "exit_code": 2},
			category: CategoryNonzeroExit,
		},
		{
			name:     "non-zero exit fallback",
			kind:     KindToolError,
			tool:     "Bash",
			payload:  map[string]any{"tool_output": "Exit code 9", "exit_code": 9},
			category: CategoryNonzeroExit,
			rule:     "明确记录 exit_code=9 且未命中更具体规则",
		},
		{
			name:     "explicit is_error fallback",
			kind:     KindToolError,
			tool:     "Task",
			payload:  map[string]any{"tool_output": "<tool_use_error>No task found with ID: a600c17</tool_use_error>", "is_error": true},
			category: CategoryToolError,
			rule:     "明确记录 is_error=true 且未命中更具体规则",
		},
		{
			name:     "exit code zero is not friction",
			kind:     KindToolError,
			tool:     "Bash",
			payload:  map[string]any{"tool_output": "all good", "exit_code": 0},
			category: "",
			rule:     "",
		},
		{
			name:     "ordinary text mentioning error is not classified",
			kind:     KindToolError,
			tool:     "Bash",
			payload:  map[string]any{"tool_output": "the error handling looks fine"},
			category: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			category, rule := Classify(tc.kind, tc.tool, tc.payload)
			if category != tc.category {
				t.Fatalf("category = %q, want %q (rule %q)", category, tc.category, rule.Text)
			}
			if tc.rule != "" && rule.Text != tc.rule {
				t.Fatalf("rule = %q, want %q", rule.Text, tc.rule)
			}
			if category != "" && (rule.Text == "" || rule.EN == "") {
				t.Fatalf("category %q has no one-line rule in both languages: %q / %q", category, rule.Text, rule.EN)
			}
		})
	}
}

func TestEveryCategoryIsReachable(t *testing.T) {
	seen := map[string]bool{}
	for _, rule := range outputRules {
		seen[rule.category] = true
	}
	seen[CategoryUserInterrupt] = true
	seen[CategoryNonzeroExit] = true
	seen[CategoryToolError] = true
	// expected_exit comes from the exit-code semantics table rather than from a
	// literal in the output, so its rule set is the one in exitcodes.go.
	for _, rule := range programExitRules {
		if !rule.failure {
			seen[CategoryExpectedExit] = true
		}
	}
	for _, category := range Categories {
		if !seen[category] {
			t.Errorf("category %q has no rule that can produce it", category)
		}
	}
	if len(seen) != len(Categories) {
		t.Errorf("rules produce %d categories, closed set has %d", len(seen), len(Categories))
	}
}

func TestCommandFormReadsBoundedToolInput(t *testing.T) {
	if got := commandForm(map[string]any{"tool_input": `{"command":"go test ./...","description":"run"}`}); got != "go test ./..." {
		t.Errorf("commandForm = %q", got)
	}
	if got := commandForm(map[string]any{"tool_input": "go test ./..."}); got != "go test ./..." {
		t.Errorf("commandForm raw = %q", got)
	}
	if got := commandForm(map[string]any{}); got != "" {
		t.Errorf("commandForm missing = %q, want empty", got)
	}
}

func TestSignatureGroupsTheSameFrictionAcrossSessions(t *testing.T) {
	cases := []struct {
		name     string
		category string
		tool     string
		program  string
		payload  map[string]any
		want     string
	}{
		{
			name:     "absolute path and run number are normalized away",
			category: CategoryFileNotFound,
			tool:     "Read",
			payload:  map[string]any{"tool_output": "Exit code 2\nls: cannot access '/home/bot/project/run-17/notes.md': No such file or directory"},
			want:     "file_not_found|Read|ls: cannot access 'notes.md': no such file or directory",
		},
		{
			name:     "the same friction in another checkout has the same signature",
			category: CategoryFileNotFound,
			tool:     "Read",
			payload:  map[string]any{"tool_output": "ls: cannot access '/srv/other/run-4/notes.md': No such file or directory"},
			want:     "file_not_found|Read|ls: cannot access 'notes.md': no such file or directory",
		},
		{
			name:     "the evidence line is the one carrying the literal, not the first line",
			category: CategoryCommandNotFound,
			tool:     "Bash",
			payload:  map[string]any{"tool_output": "Chunk ID: 1a8ab2\nWall time: 0.1 seconds\n/bin/bash: line 1: gofmt: command not found"},
			want:     "command_not_found|Bash|bash: line #: gofmt: command not found",
		},
		{
			name:     "step 3: a category from a recorded field picks the line naming the failure",
			category: CategoryNonzeroExit,
			tool:     "Bash",
			payload:  map[string]any{"tool_output": "\n\nmake: *** [Makefile:31: build] Error 2", "exit_code": 2},
			want:     "nonzero_exit|Bash|make: *** [makefile:#: build] error #",
		},
		{
			name:     "step 2: a traceback signs on its last line, not on its header",
			category: CategoryNonzeroExit,
			tool:     "Bash",
			program:  "python",
			payload: map[string]any{
				"tool_output": "Traceback (most recent call last):\n  File \"/home/bot/x/run.py\", line 3, in <module>\n    import pydantic\nModuleNotFoundError: No module named 'pydantic'",
				"exit_code":   1,
			},
			want: "nonzero_exit|Bash|modulenotfounderror: no module named 'pydantic'",
		},
		{
			name:     "step 3: a harness refusal is evidence even without an error word",
			category: CategoryToolError,
			tool:     "exec_command",
			program:  "git",
			payload: map[string]any{
				"tool_output": "Script failed\nOutput:\n\nCommand blocked by PreToolUse hook: git commit is blocked.",
				"is_error":    true,
			},
			want: "tool_error|exec_command|command blocked by pretooluse hook: git commit is blocked.",
		},
		{
			name:     "step 4: an output with nothing to tell apart signs on the program and the exit code",
			category: CategoryNonzeroExit,
			tool:     "exec_command",
			program:  "pytest",
			payload: map[string]any{
				"tool_output": "Chunk ID: 1a8ab2\nWall time 0.4 seconds\nOutput:\n\n3 skipped in 0.4s\nTotal output lines: 1",
				"exit_code":   5,
			},
			want: "nonzero_exit|exec_command|pytest exit 5",
		},
		{
			name:     "step 4: two exit codes of the same program stay two signatures",
			category: CategoryNonzeroExit,
			tool:     "exec_command",
			program:  "pytest",
			payload:  map[string]any{"tool_output": "Chunk ID: 1a8ab2\nOutput:\n\n3 skipped in 0.4s", "exit_code": 1},
			want:     "nonzero_exit|exec_command|pytest exit 1",
		},
		{
			name:     "step 4: with no program recorded the tool name stands in",
			category: CategoryToolError,
			tool:     "exec_command",
			payload:  map[string]any{"tool_output": "Chunk ID: 1a8ab2\nOutput:\n\n3 skipped in 0.4s", "is_error": true},
			want:     "tool_error|exec_command|exec_command tool_error",
		},
		{
			name:     "the codex wrapper header is not the evidence",
			category: CategoryNonzeroExit,
			tool:     "exec_command",
			payload: map[string]any{
				"tool_output": "Chunk ID: 1a8ab2\nWall time: 0.4 seconds\nProcess exited with code 2\nOriginal token count: 97\nOutput:\nmake: *** [Makefile:31: build] Error 2",
				"exit_code":   2,
			},
			want: "nonzero_exit|exec_command|make: *** [makefile:#: build] error #",
		},
		{
			name:     "a blocked codex script signs on the reason, not on the frame",
			category: CategoryToolError,
			tool:     "",
			payload: map[string]any{
				"tool_output": "Script failed\nWall time 0.1 seconds\nOutput:\n\nScript error:\nCommand blocked by PreToolUse hook: git commit is blocked.",
				"is_error":    true,
			},
			want: "tool_error||command blocked by pretooluse hook: git commit is blocked.",
		},
		{
			name:     "framing alone with nothing else recorded still yields a signature",
			category: CategoryNonzeroExit,
			tool:     "",
			payload:  map[string]any{"tool_output": "Chunk ID: 1a8ab2\nProcess exited with code 2\nOutput:", "exit_code": 2},
			want:     "nonzero_exit||chunk id: #a#ab#",
		},
		{
			name:     "a codex turn_aborted signs on the recorded reason",
			category: CategoryUserInterrupt,
			tool:     "",
			payload:  map[string]any{"abort_reason": "interrupted"},
			want:     "user_interrupt||interrupted",
		},
		{
			name:     "an unclassified record has no signature",
			category: "",
			tool:     "Bash",
			payload:  map[string]any{"tool_output": "all good"},
			want:     "",
		},
		{
			name:     "an interrupt signs on its message text",
			category: CategoryUserInterrupt,
			tool:     "",
			payload:  map[string]any{"text": "[Request interrupted by user for tool use]"},
			want:     "user_interrupt||[request interrupted by user for tool use]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Signature(tc.category, tc.tool, tc.payload, tc.program); got != tc.want {
				t.Fatalf("Signature = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeLineBoundsAndCollapses(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"exit code prefix is dropped", "Exit code 2: boom", "boom"},
		{"a line that is only an exit code normalizes to empty", "exit code: 137", ""},
		{"windows path keeps its last segment", `C:\Users\bot\a.txt missing`, "a.txt missing"},
		{"a relative path is left alone", "cmd/flatline/main.go:12: bad", "cmd/flatline/main.go:#: bad"},
		{"whitespace runs collapse", "a \t  b", "a b"},
		{"bounded to 120 characters", strings.Repeat("x", 200), strings.Repeat("x", 120)},
		{"empty stays empty", "   ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeLine(tc.in); got != tc.want {
				t.Fatalf("NormalizeLine(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// pytestOutput is the shape a Codex exec result carries when pytest reports a
// failure: harness framing, a run summary, a "=== FAILURES ===" divider, the
// traceback, and the short summary that names the test.
const pytestOutput = `Chunk ID: 6dff8c
Wall time: 1.7004 seconds
Process exited with code 1
Output:
============================= test session starts ==============================
collected 44 items

tests/unit/application/sync/test_sync_coordinator.py F

=================================== FAILURES ===================================
____________ TestSyncCoordinator.test_sync_all_runs_all_four_syncs _____________

    def test_sync_all_runs_all_four_syncs() -> None:
>       assert set(results.keys()) == {"positions", "balance"}
E       AssertionError: assert {'balance'} == {'balance', 'positions'}

tests/unit/application/sync/test_sync_coordinator.py:79: AssertionError
=========================== short test summary info ============================
FAILED tests/unit/application/sync/test_sync_coordinator.py::TestSyncCoordinator::test_sync_all_runs_all_four_syncs
!!!!!!!!!!!!!!!!!!!!!!!!!! stopping after 1 failures !!!!!!!!!!!!!!!!!!!!!!!!!!!
========================= 1 failed, 15 passed in 0.79s =========================`

func TestSignatureSkipsSectionDividers(t *testing.T) {
	cases := []struct {
		name     string
		category string
		tool     string
		payload  map[string]any
		want     string
	}{
		{
			name:     "a pytest FAILURES divider is not the evidence",
			category: CategoryTestFailure,
			tool:     "exec_command",
			payload: map[string]any{"tool_input": `{"cmd":"pytest -q tests/"}`,
				"tool_output": pytestOutput, "exit_code": 1},
			want: "test_failure|exec_command|failed tests/unit/application/sync/test_sync_coordinator.py::testsynccoordinator::test_sync_all_runs_all_four_syncs",
		},
		{
			name:     "a captured-output divider is skipped for the fallback line",
			category: CategoryNonzeroExit,
			tool:     "exec_command",
			payload: map[string]any{"tool_input": `{"cmd":"./run.sh"}`, "exit_code": 3,
				"tool_output": "------ Captured stderr ------\nfatal: unable to open the socket\n-----------------------------"},
			want: "nonzero_exit|exec_command|fatal: unable to open the socket",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Signature(tc.category, tc.tool, tc.payload, "")
			if got != tc.want {
				t.Fatalf("Signature =\n  %q\nwant\n  %q", got, tc.want)
			}
		})
	}
}

func TestIsDecorationLine(t *testing.T) {
	decoration := []string{
		"=================================== FAILURES ===================================",
		"------ Captured stderr ------",
		"____________ TestSyncCoordinator.test_sync_all _____________",
		"!!!!!!!!!!!! stopping after 1 failures !!!!!!!!!!!!",
		"========================= 1 failed, 15 passed in 0.79s =========================",
		"----------",
		"### step ###",
	}
	content := []string{
		"--- a/internal/friction/friction.go",
		"FAILED tests/x.py::test_y - AssertionError",
		"E       AssertionError: assert 1 == 2",
		"fatal: unable to open the socket",
		"--",
		"",
	}
	for _, line := range decoration {
		if !isDecorationLine(line) {
			t.Errorf("isDecorationLine(%q) = false, want true", line)
		}
	}
	for _, line := range content {
		if isDecorationLine(line) {
			t.Errorf("isDecorationLine(%q) = true, want false", line)
		}
	}
}
