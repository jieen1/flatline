package friction

import (
	"path/filepath"
	"strings"
)

// A nonzero exit code is not by itself a failure. Several programs use one as
// an answer: ripgrep says "nothing matched", diff says "the two files differ",
// pgrep says "no process matched". Recording those as friction is wrong data,
// so this closed table names what {program, exit_code} means and whether it is
// a failure at all.
//
// The table is keyed on the program whose exit status the shell reported, not
// on the first program on the line; ExitCandidates works that out. Every entry
// is a documented convention of the program itself, never an inference from
// how the command happened to be used.

// ExitOutcome is what one recorded exit code means.
type ExitOutcome struct {
	// Program is the program the rule is about, or "" for a rule that holds
	// whatever ran (a signal).
	Program string
	Code    int
	// Meaning is the one sentence that explains the code, in the classifier's
	// own words. MeaningEN is the same sentence for a reader in English.
	Meaning   string
	MeaningEN string
	// IsFailure says whether the command did not do what it was asked. A false
	// value means the code is an answer, not a failure.
	IsFailure bool
}

type programExitRule struct {
	programs  []string
	code      int
	meaning   string
	meaningEN string
	failure   bool
}

// programExitRules are the conventions a specific program documents for a
// specific code.
var programExitRules = []programExitRule{
	{[]string{"rg", "ripgrep", "grep", "egrep", "fgrep", "ag", "ack"}, 1,
		"搜索命令没有匹配到内容（退出码 1 是它的“无匹配”约定）",
		"The search command matched nothing (exit code 1 is its \"no match\" convention)", false},
	{[]string{"diff", "cmp", "colordiff"}, 1,
		"比较命令发现两边有差异（退出码 1 是它的“有差异”约定）",
		"The comparison command found a difference (exit code 1 is its \"they differ\" convention)", false},
	{[]string{"pkill", "pgrep"}, 1,
		"没有匹配到任何进程（退出码 1 是它的“无匹配进程”约定）",
		"No process matched (exit code 1 is its \"no matching process\" convention)", false},
	{[]string{"test", "["}, 1,
		"条件判断为假（退出码 1 是 test/[ 的“条件不成立”约定）",
		"The condition is false (exit code 1 is the \"condition not met\" convention of test/[)", false},
	{[]string{"pytest", "py.test"}, 5,
		"pytest 没有收集到任何测试用例（退出码 5 是它的 NO_TESTS_COLLECTED）",
		"pytest collected no test cases (exit code 5 is its NO_TESTS_COLLECTED)", false},
}

type genericExitRule struct {
	code      int
	meaning   string
	meaningEN string
	failure   bool
}

// genericExitRules hold whatever program ran: the shell reports a process
// killed by signal N as 128+N, and timeout(1) reports its own timeout as 124.
// All of them are failures; they are listed so the reason is named instead of
// being shown as a bare number.
var genericExitRules = []genericExitRule{
	{124, "timeout 在给定时限内中止了这条命令（退出码 124 是 timeout 的超时约定）",
		"timeout stopped this command at the limit it was given (exit code 124 is timeout's own convention)", true},
	{130, "进程被 SIGINT 中止（退出码 130 = 128+2，通常是 Ctrl-C）",
		"The process was stopped by SIGINT (exit code 130 = 128+2, usually Ctrl-C)", true},
	{137, "进程被 SIGKILL 中止（退出码 137 = 128+9）",
		"The process was killed by SIGKILL (exit code 137 = 128+9)", true},
	{143, "进程被 SIGTERM 中止（退出码 143 = 128+15）",
		"The process was terminated by SIGTERM (exit code 143 = 128+15)", true},
}

// exitStatusTransparent programs cannot own the nonzero status of a command
// line: `true` and `:` always succeed, and the rest only move the shell
// around. They are dropped from the candidate set. A `cd` that fails prints
// "No such file or directory", which the output rules match before this table
// is ever consulted.
var exitStatusTransparent = map[string]struct{}{
	"cd": {}, "pushd": {}, "popd": {}, "export": {}, "set": {}, "unset": {},
	"source": {}, ".": {}, "true": {}, ":": {},
}

// moduleRunners run a module or another program named on their own command
// line, so the exit code belongs to what they ran, not to them.
var moduleRunners = map[string]struct{}{"python": {}, "python3": {}, "python2": {}, "py": {}}

var subcommandRunners = map[string]struct{}{"poetry": {}, "uv": {}, "pdm": {}, "rye": {}, "hatch": {}}

// LookupExit reports what a recorded exit code means for the command that
// produced it, and whether that is a failure.
//
// Two steps, in order:
//
//  1. A signal or timeout code holds whatever ran, so it is answered first.
//  2. Otherwise every program that could own the status has to agree. The
//     shell reports the status of the last statement, and inside it either the
//     last command of the chain or the one that short-circuited it, so all of
//     them are candidates. If any candidate has no entry for this code, the
//     table does not apply: the code stays an unexplained nonzero exit rather
//     than being explained by the wrong program.
func LookupExit(command string, code int) (ExitOutcome, bool) {
	for _, rule := range genericExitRules {
		if rule.code == code {
			return ExitOutcome{Code: code, Meaning: rule.meaning, MeaningEN: rule.meaningEN, IsFailure: rule.failure}, true
		}
	}
	candidates := ExitCandidates(command)
	if len(candidates) == 0 {
		return ExitOutcome{}, false
	}
	var agreed *programExitRule
	for _, candidate := range candidates {
		rule, ok := programExitRuleFor(candidate, code)
		if !ok {
			return ExitOutcome{}, false
		}
		if agreed != nil && (agreed.meaning != rule.meaning || agreed.failure != rule.failure) {
			return ExitOutcome{}, false
		}
		agreed = rule
	}
	return ExitOutcome{Program: candidates[len(candidates)-1], Code: code,
		Meaning: agreed.meaning, MeaningEN: agreed.meaningEN, IsFailure: agreed.failure}, true
}

// ExpectedExit reports whether this nonzero exit code is an answer rather than
// a failure. It is what the command and tool projections read to keep an
// expected code out of their failure counts.
func ExpectedExit(command string, code int) bool {
	if code == 0 {
		return false
	}
	outcome, ok := LookupExit(command, code)
	return ok && !outcome.IsFailure
}

func programExitRuleFor(program string, code int) (*programExitRule, bool) {
	for index := range programExitRules {
		rule := &programExitRules[index]
		if rule.code != code {
			continue
		}
		for _, name := range rule.programs {
			if name == program {
				return rule, true
			}
		}
	}
	return nil, false
}

// ExitCandidates lists the programs that could own the exit status of a
// command line: the shell reports the status of the last statement, inside it
// every `&&` / `||` link can be the one that stopped the chain, and inside a
// pipeline only the last command's status is reported.
func ExitCandidates(command string) []string {
	statements := splitUnquoted(command, []string{";", "\n"})
	last := ""
	for index := len(statements) - 1; index >= 0; index-- {
		if strings.TrimSpace(statements[index]) != "" {
			last = statements[index]
			break
		}
	}
	if last == "" {
		return nil
	}
	out := make([]string, 0, 2)
	seen := make(map[string]struct{}, 2)
	for _, link := range splitUnquoted(last, []string{"&&", "||"}) {
		pipeline := splitUnquoted(link, []string{"|"})
		program := ""
		for index := len(pipeline) - 1; index >= 0 && program == ""; index-- {
			program = exitProgramOf(pipeline[index])
		}
		if program == "" {
			continue
		}
		if _, transparent := exitStatusTransparent[program]; transparent {
			continue
		}
		if _, repeated := seen[program]; repeated {
			continue
		}
		seen[program] = struct{}{}
		out = append(out, program)
	}
	return out
}

// exitProgramOf names the program one command runs, stepping past the wrappers
// and runners that hand their exit status straight through.
func exitProgramOf(segment string) string {
	fields := strings.Fields(segment)
	for index := 0; index < len(fields); index++ {
		field := strings.TrimLeft(strings.Trim(fields[index], `"'`), "({$")
		if field == "" || isEnvAssignment(field) {
			continue
		}
		name := filepath.Base(field)
		if _, wrapped := commandWrappers[name]; wrapped {
			continue
		}
		if name == "env" {
			continue
		}
		if name == "timeout" {
			// timeout takes a duration before the command it runs.
			index += skipTimeoutArguments(fields[index+1:])
			continue
		}
		if _, ok := moduleRunners[name]; ok {
			if module, found := moduleArgument(fields[index+1:]); found {
				return module
			}
			return name
		}
		if _, ok := subcommandRunners[name]; ok {
			if index+1 < len(fields) && strings.Trim(fields[index+1], `"'`) == "run" {
				index++
				continue
			}
			return name
		}
		// An option can only appear before the program when a wrapper or a
		// runner has already been stepped over; it is never the program.
		if strings.HasPrefix(field, "-") {
			continue
		}
		if name == "." || name == "/" {
			return ""
		}
		return name
	}
	return ""
}

// skipTimeoutArguments counts the leading options and the duration timeout(1)
// consumes before the command it runs.
func skipTimeoutArguments(rest []string) int {
	skipped := 0
	for _, field := range rest {
		field = strings.Trim(field, `"'`)
		if field == "" {
			skipped++
			continue
		}
		if strings.HasPrefix(field, "-") {
			skipped++
			continue
		}
		// The first non-option argument is the duration.
		return skipped + 1
	}
	return skipped
}

// moduleArgument returns the module named by `-m`, if it comes before the
// first non-option argument.
func moduleArgument(rest []string) (string, bool) {
	for index, field := range rest {
		field = strings.Trim(field, `"'`)
		if field == "-m" {
			if index+1 < len(rest) {
				return strings.Trim(rest[index+1], `"'`), true
			}
			return "", false
		}
		if !strings.HasPrefix(field, "-") {
			return "", false
		}
	}
	return "", false
}
