package friction

import (
	"path/filepath"
	"strings"
)

// commandWrappers run another program without changing which program the
// command line is understood to run.
var commandWrappers = map[string]struct{}{"sudo": {}, "time": {}, "nohup": {}}

// A command line runs shell syntax as well as programs, and only the programs
// belong in a failure-rate list. Three groups are read as "not a program":
//
//   - statement enders: everything after them on that statement is their own
//     argument, so scanning moves on to the next statement. `cd`, `export`,
//     `set` and friends only change the shell's own state; `true`, `false` and
//     `:` do nothing at all; `which`, `type` and `hash` ask whether a program
//     exists instead of running one; `for`, `select` and `case` are followed by
//     a word list, not by a command.
//   - field skips: prefixes that run the program named after them, and the
//     keywords that introduce a statement (`if`, `do`, …).
//   - soft names: `echo` and `env` are programs when a command line is nothing
//     but them, and prefixes when it is not — `echo y | pip install x` runs
//     pip, `env FOO=1 python x` runs python. They stand only when nothing else
//     on the line names a program. `env` is a wrapper, so reading continues
//     inside its own statement; `echo` prints, so its arguments are not
//     programs and reading continues at the next statement.
var (
	statementEnders = map[string]struct{}{
		"cd": {}, "pushd": {}, "popd": {},
		"export": {}, "set": {}, "unset": {}, "alias": {}, "unalias": {},
		"declare": {}, "typeset": {}, "readonly": {}, "local": {}, "shift": {},
		"source": {}, ".": {}, "trap": {}, "ulimit": {}, "umask": {},
		"true": {}, "false": {}, ":": {},
		"test": {}, "[": {}, "[[": {},
		"which": {}, "type": {}, "hash": {},
		"for": {}, "select": {}, "case": {},
	}
	fieldSkips = map[string]struct{}{
		"command": {}, "builtin": {}, "exec": {}, `\`: {}, "!": {}, "{": {}, "}": {},
		"if": {}, "elif": {}, "then": {}, "else": {}, "fi": {},
		"while": {}, "until": {}, "do": {}, "done": {}, "esac": {},
	}
	softWrappers = map[string]struct{}{"env": {}}
	softEnders   = map[string]struct{}{"echo": {}}
)

// Program names the first program a command line runs. Leading environment
// assignments and the sudo/time/nohup wrappers are stepped over, shell syntax
// and the builtins listed above run no program, and only the last path segment
// is kept. It returns "" when the line names no program at all — a script that
// is nothing but `set -euo pipefail` has no program, which is not the same as
// an unnamed one.
//
// It answers "what did this command line run", which is what the command
// projection records. Whose exit status the shell reported is a different
// question; ExitCandidates answers that one.
func Program(command string) string {
	soft := ""
	for _, segment := range commandSegments(command) {
		name, gentle := segmentProgram(segment)
		switch {
		case name == "":
		case gentle:
			if soft == "" {
				soft = name
			}
		default:
			return name
		}
	}
	return soft
}

// segmentProgram reads one statement and reports the program it runs and
// whether that name is only a soft one — a name that stands for this statement
// but yields to any program a later statement names.
func segmentProgram(segment string) (string, bool) {
	soft := ""
	for _, field := range strings.Fields(segment) {
		if strings.HasPrefix(field, "#") {
			// The rest of the statement is a comment.
			break
		}
		if substituted, ok := substitutedCommand(field); ok {
			field = substituted
		} else if isEnvAssignment(field) {
			continue
		}
		field = strings.Trim(field, `"'`)
		field = strings.TrimLeft(field, "({$")
		// A leading backslash escapes an alias; the program is the rest.
		if len(field) > 1 && field[0] == '\\' {
			field = field[1:]
		}
		if field == "" || strings.HasPrefix(field, "-") {
			continue
		}
		name := filepath.Base(field)
		if _, wrapped := commandWrappers[name]; wrapped {
			continue
		}
		if _, skipped := fieldSkips[name]; skipped {
			continue
		}
		if _, gentle := softWrappers[name]; gentle {
			if soft == "" {
				soft = name
			}
			continue
		}
		if _, gentle := softEnders[name]; gentle {
			if soft == "" {
				soft = name
			}
			break
		}
		if _, ends := statementEnders[name]; ends {
			break
		}
		if name == "/" {
			break
		}
		return name, false
	}
	return soft, soft != ""
}

// substitutedCommand returns the command inside `NAME=$(cmd …)`. The
// assignment itself runs nothing; the substitution is what runs.
func substitutedCommand(field string) (string, bool) {
	if field == "" || !isIdentByte(field[0]) {
		return "", false
	}
	for _, marker := range []string{"=$(", "=`"} {
		index := strings.Index(field, marker)
		if index <= 0 {
			continue
		}
		value := field[index+len(marker):]
		if cut := strings.IndexAny(value, ")`"); cut >= 0 {
			value = value[:cut]
		}
		if value = strings.TrimSpace(value); value != "" {
			return value, true
		}
	}
	return "", false
}

// ProbedCommand returns the single program name a command line asked the shell
// to look up, and reports whether it is such a line at all.
//
// The rule is one sentence: a command line whose only statement is
// `which <name>` asks whether <name> is on PATH, so a nonzero exit code
// recorded for it says that name was not found. Anything else — a second
// statement, a second name, a redirect target — returns false: the recorded
// exit code then belongs to the last statement, and would not be an answer
// about this name.
func ProbedCommand(command string) (string, bool) {
	statements := commandSegments(command)
	probe := ""
	for _, statement := range statements {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		if probe != "" {
			return "", false
		}
		probe = statement
	}
	fields := strings.Fields(probe)
	if len(fields) != 2 || filepath.Base(strings.Trim(fields[0], `"'`)) != "which" {
		return "", false
	}
	name := strings.Trim(fields[1], `"'`)
	if name == "" || strings.HasPrefix(name, "-") || strings.ContainsAny(name, "<>$*?") {
		return "", false
	}
	return name, true
}

func isEnvAssignment(field string) bool {
	index := strings.Index(field, "=")
	return index > 0 && !strings.HasPrefix(field, "-") &&
		isIdentByte(field[0]) && !strings.ContainsAny(field[:index], "/.")
}

func isIdentByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func isQuote(b byte) bool { return b == '"' || b == '\'' || b == '`' }

// commandSegments splits a command line on the separators that start a new
// program, ignoring separators inside quotes.
func commandSegments(command string) []string {
	return splitUnquoted(command, []string{"&&", "||", "|", "&", ";", "\n"})
}

// splitUnquoted splits on the given separators, longest first, skipping any
// separator that appears inside single, double or back quotes. Empty segments
// are kept: a caller that cares about position needs them.
func splitUnquoted(command string, separators []string) []string {
	out := make([]string, 0, 2)
	var current strings.Builder
	var quote byte
	for i := 0; i < len(command); {
		c := command[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			current.WriteByte(c)
			i++
			continue
		}
		if isQuote(c) {
			quote = c
			current.WriteByte(c)
			i++
			continue
		}
		if width := matchSeparator(command[i:], separators); width > 0 {
			out = append(out, current.String())
			current.Reset()
			i += width
			continue
		}
		current.WriteByte(c)
		i++
	}
	return append(out, current.String())
}

func matchSeparator(rest string, separators []string) int {
	width := 0
	for _, separator := range separators {
		if len(separator) > width && strings.HasPrefix(rest, separator) {
			width = len(separator)
		}
	}
	return width
}
