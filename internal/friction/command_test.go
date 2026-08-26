package friction

import "testing"

func TestProgramSkipsShellSyntaxAndBuiltins(t *testing.T) {
	cases := map[string]string{
		"set -euo pipefail\nversion=go1.27.0\nmkdir -p /tmp/x":      "mkdir",
		"export PATH=/opt/bin:$PATH\ngofmt -l .":                    "gofmt",
		"which sqlite3 && sqlite3 db.sqlite 'select 1'":             "sqlite3",
		"VAR=1 \\\n  nohup /home/bot/.venvs/vllm/bin/python run.py": "python",
		"# 制造 CPU 竞争\ncp a b":                                       "cp",
		"cd /repo\n: > log\ntimeout 60 pytest -q":                   "timeout",
		"true":              "",
		"set -euo pipefail": "",
		"source .venv/bin/activate\npython -m pytest":                       "python",
		"for f in a b c; do printf '%s' \"$f\"; done":                       "printf",
		"while true; do sleep 5; done":                                      "sleep",
		"if [ -f x ]; then cat x; fi":                                       "cat",
		"command -v go || echo missing":                                     "go",
		"SNAP=$(ls -d /home/bot/models/*/ | head -1) && du -sh \"$SNAP\"":   "ls",
		"type grep; which grep; \\grep -n '^class ' /home/bot/vllm/core.py": "grep",
	}
	for command, want := range cases {
		if got := Program(command); got != want {
			t.Errorf("Program(%q) = %q, want %q", command, got, want)
		}
	}
}

// echo and env name the program when the line is nothing but them, and step
// aside when the line goes on to run something.
func TestProgramKeepsEchoAndEnvOnlyWhenTheyAreTheWholeLine(t *testing.T) {
	cases := map[string]string{
		"echo \"waiting for forks\"":                  "echo",
		"echo \"=== runtime ===\" && find runtime":    "find",
		"echo y | pip install ruff":                   "pip",
		"env":                                         "env",
		"env | grep -i pythonpath":                    "grep",
		"env QSR_TEST=1 /home/bot/.venvs/py bench.py": "py",
	}
	for command, want := range cases {
		if got := Program(command); got != want {
			t.Errorf("Program(%q) = %q, want %q", command, got, want)
		}
	}
}

func TestProbedCommandReadsOnlySingleStatementWhichProbes(t *testing.T) {
	cases := []struct {
		command string
		want    string
		ok      bool
	}{
		{"which psql", "psql", true},
		{"which  redis-server ", "redis-server", true},
		{"/usr/bin/which initdb", "initdb", true},
		// Two names: a nonzero exit does not say which of them was missing.
		{"which nsys ncu", "", false},
		// A second statement owns the recorded exit code, not the probe.
		{"which gh && gh auth status", "", false},
		{"which nvcc 2>/dev/null || echo none", "", false},
		{"rg -n needle src/", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := ProbedCommand(tc.command)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ProbedCommand(%q) = (%q, %v), want (%q, %v)", tc.command, got, ok, tc.want, tc.ok)
		}
	}
}
