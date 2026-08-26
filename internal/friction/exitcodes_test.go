package friction

import (
	"reflect"
	"testing"
)

func TestLookupExitReadsTheProgramsOwnConvention(t *testing.T) {
	cases := []struct {
		name      string
		command   string
		code      int
		found     bool
		isFailure bool
		meaning   string
	}{
		{name: "ripgrep exit 1 is no match", command: "rg -n needle src/", code: 1, found: true},
		{name: "ripgrep exit 2 is a real error", command: "rg -n needle src/", code: 2},
		{name: "grep exit 1 is no match", command: "grep -rn needle .", code: 1, found: true},
		{name: "diff exit 1 is a difference", command: "diff a.txt b.txt", code: 1, found: true},
		{name: "pgrep exit 1 is no process", command: "pgrep -f server", code: 1, found: true},
		{name: "test exit 1 is a false condition", command: "test -f /nope", code: 1, found: true},
		{name: "bracket test exit 1 is a false condition", command: "[ -f /nope ]", code: 1, found: true},
		{name: "pytest exit 5 collected nothing", command: "pytest -q tests/", code: 5, found: true},
		{name: "pytest exit 1 is a real failure", command: "pytest -q tests/", code: 1},
		{name: "python -m pytest is pytest", command: "python -m pytest -q tests/x.py", code: 5, found: true},
		{name: "an absolute interpreter is still pytest", command: "/tmp/ci/bin/python -m pytest -q tests/", code: 5, found: true},
		{name: "poetry run pytest is pytest", command: "poetry run pytest -q tests/", code: 5, found: true},
		{name: "a cd prefix does not own the status", command: "cd /repo && rg -n needle src/", code: 1, found: true},
		{name: "a pipeline reports its last command", command: "rg --files src | rg needle", code: 1, found: true},
		{name: "an unrelated program before && blocks the rule", command: "make build && rg -n needle src/", code: 1},
		{name: "a trailing program that is not in the table blocks the rule", command: "test -f x && nl -ba x", code: 1},
		{name: "the last statement is the one that reports", command: "rg -n a src\nls /nope", code: 1},
		{name: "a timeout wrapper hands the status through", command: "timeout 30 rg -n needle src/", code: 1, found: true},
		{name: "timeout's own code holds whatever ran", command: "./server", code: 124, found: true, isFailure: true,
			meaning: "timeout 在给定时限内中止了这条命令（退出码 124 是 timeout 的超时约定）"},
		{name: "sigterm holds whatever ran", command: "pkill -f server || true", code: 143, found: true, isFailure: true},
		{name: "sigint holds whatever ran", command: "./server", code: 130, found: true, isFailure: true},
		{name: "sigkill holds whatever ran", command: "./server", code: 137, found: true, isFailure: true},
		{name: "no command means no rule", command: "", code: 1},
		{name: "an unlisted program has no rule", command: "poetry install", code: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome, ok := LookupExit(tc.command, tc.code)
			if ok != tc.found {
				t.Fatalf("LookupExit(%q, %d) found = %t, want %t (%+v)", tc.command, tc.code, ok, tc.found, outcome)
			}
			if !ok {
				return
			}
			if outcome.IsFailure != tc.isFailure {
				t.Fatalf("is_failure = %t, want %t (%q)", outcome.IsFailure, tc.isFailure, outcome.Meaning)
			}
			if outcome.Meaning == "" {
				t.Fatal("an exit rule with no one-line meaning")
			}
			if tc.meaning != "" && outcome.Meaning != tc.meaning {
				t.Fatalf("meaning = %q, want %q", outcome.Meaning, tc.meaning)
			}
			if expected := ExpectedExit(tc.command, tc.code); expected == tc.isFailure {
				t.Fatalf("ExpectedExit = %t while is_failure = %t", expected, tc.isFailure)
			}
		})
	}
}

func TestExpectedExitIsNeverTrueForZero(t *testing.T) {
	if ExpectedExit("rg -n needle .", 0) {
		t.Error("a zero exit is a success, not an expected nonzero exit")
	}
}

func TestExitCandidatesNamesEveryProgramThatCouldOwnTheStatus(t *testing.T) {
	cases := []struct {
		command string
		want    []string
	}{
		{"rg -n needle src/", []string{"rg"}},
		{"cd /repo && rg -n needle src/", []string{"rg"}},
		{"make build && rg -n needle src/", []string{"make", "rg"}},
		{"rg --files src | rg needle | head -3", []string{"head"}},
		{"echo hi; rg -n needle src/", []string{"rg"}},
		{"pkill -f server || true", []string{"pkill"}},
		{"FOO=1 rg -n needle src/", []string{"rg"}},
		{"sudo rg -n needle src/", []string{"rg"}},
		{"", nil},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			got := ExitCandidates(tc.command)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ExitCandidates(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}

func TestProgramStillNamesTheFirstProgram(t *testing.T) {
	cases := map[string]string{
		"rg -n needle src/":              "rg",
		"cd /repo && go test ./...":      "go",
		"FOO=1 sudo /usr/bin/make build": "make",
		"python -m pytest -q tests/x.py": "python",
		"rg --files src | rg needle":     "rg",
		"":                               "",
		"./scripts/run.sh --flag":        "run.sh",
	}
	for command, want := range cases {
		if got := Program(command); got != want {
			t.Errorf("Program(%q) = %q, want %q", command, got, want)
		}
	}
}

func TestEveryExitRuleIsWrittenInBothLanguages(t *testing.T) {
	for _, rule := range programExitRules {
		if rule.meaning == "" || rule.meaningEN == "" {
			t.Errorf("exit rule %v/%d has meaning %q / %q; both are required",
				rule.programs, rule.code, rule.meaning, rule.meaningEN)
		}
	}
	for _, rule := range genericExitRules {
		if rule.meaning == "" || rule.meaningEN == "" {
			t.Errorf("exit rule %d has meaning %q / %q; both are required",
				rule.code, rule.meaning, rule.meaningEN)
		}
	}
}
