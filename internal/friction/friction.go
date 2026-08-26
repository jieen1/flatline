// Package friction turns a bounded friction payload into one of a closed set
// of categories plus the single sentence that explains the match (ADR-8).
// Rules read only the bounded payload that is already stored in
// friction_records.payload_json; they never re-open the source history.
package friction

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ClassifierVersion changes whenever a rule, a literal or the category set
// changes. Rows carrying a different version are recomputed on daemon start.
const ClassifierVersion = "friction/5"

const (
	KindToolError     = "tool_error"
	KindUserInterrupt = "user_interrupt"
)

const (
	CategoryUserInterrupt    = "user_interrupt"
	CategoryPermissionDenied = "permission_denied"
	CategoryCommandNotFound  = "command_not_found"
	CategoryFileNotFound     = "file_not_found"
	CategoryToolInputInvalid = "tool_input_invalid"
	CategoryTimeout          = "timeout"
	CategoryNetworkError     = "network_error"
	CategoryTestFailure      = "test_failure"
	CategoryBuildError       = "build_error"
	CategoryExpectedExit     = "expected_exit"
	CategoryNonzeroExit      = "nonzero_exit"
	CategoryToolError        = "tool_error"
)

// Categories is the closed set, in rule priority order.
var Categories = []string{
	CategoryUserInterrupt,
	CategoryPermissionDenied,
	CategoryCommandNotFound,
	CategoryFileNotFound,
	CategoryToolInputInvalid,
	CategoryTimeout,
	CategoryNetworkError,
	CategoryTestFailure,
	CategoryBuildError,
	CategoryExpectedExit,
	CategoryNonzeroExit,
	CategoryToolError,
}

// IsExpectedExit reports whether a category records an exit code the program
// uses as an answer rather than as a failure. Every friction count, signature,
// lifecycle and failure rate leaves these out by default: they are facts, but
// they are not friction.
func IsExpectedExit(category string) bool { return category == CategoryExpectedExit }

const interruptPrefix = "[Request interrupted by user"

// abortInterrupted is what Codex writes into its turn_aborted record when the
// user stopped the turn. Claude Code records the same fact as message text;
// these are the only two explicit interrupt records either source keeps.
const abortInterrupted = "interrupted"

// timeoutExitCode is the code timeout(1) reports when it stopped the command.
const timeoutExitCode = 124

// literalRule matches a bounded output against a list of literals. The first
// literal that appears becomes the one-line explanation. exitCodes lets a rule
// also match on an exit code the harness recorded explicitly, with exitReason
// as its one-line explanation; an exit code only exists for an executed
// command, so that branch does not repeat the shell check.
type literalRule struct {
	category     string
	literals     []string
	exitCodes    []int
	exitReason   string
	exitReasonEN string
	needsCmd     bool
	needsTest    bool
	needsBuild   bool
}

// Rule is the one sentence that explains why a record landed in its category,
// written once in each language the pages are read in. Both come out of the
// same match; neither is translated after the fact.
type Rule struct {
	Text string
	EN   string
}

var outputRules = []literalRule{
	{category: CategoryPermissionDenied, literals: []string{"permission denied", "EACCES", "Operation not permitted", "requires approval"}},
	{category: CategoryCommandNotFound, literals: []string{"command not found", "not recognized as an internal", "is not recognized"},
		exitCodes: []int{127}, exitReason: "明确记录 exit_code=127（shell 的“命令未找到”退出码）",
		exitReasonEN: "exit_code=127 was recorded explicitly (the shell's \"command not found\" code)", needsCmd: true},
	{category: CategoryFileNotFound, literals: []string{"ENOENT", "no such file", "does not exist", "File does not exist"}},
	{category: CategoryToolInputInvalid, literals: []string{"InputValidationError", "String to replace not found", "must be absolute", "old_string"}},
	{category: CategoryTimeout, literals: []string{"timed out", "timeout", "ETIMEDOUT"}},
	{category: CategoryNetworkError, literals: []string{"ECONNREFUSED", "ENOTFOUND", "Could not resolve host", "TLS handshake", "502 Bad Gateway", "503 Service Unavailable", "504 Gateway Time"}},
	{category: CategoryTestFailure, literals: []string{"FAIL", "failed"}, needsTest: true},
	{category: CategoryBuildError, literals: []string{"error:"}, needsBuild: true},
}

var testCommands = []string{"go test", "pytest", "npm test", "npm run test", "yarn test", "pnpm test", "jest", "cargo test"}

var buildCommands = []string{"go build", "go vet", "tsc", "cargo build", "make", "gradle build", "mvn package"}

var shellTools = []string{"bash", "shell", "sh", "exec", "run_terminal_cmd", "local_shell", "terminal"}

// Classify returns the category and the one-line rule that produced it.
// An empty category means no rule matched; the caller stores NULL rather than
// inventing a category.
func Classify(kind, toolName string, payload map[string]any) (string, Rule) {
	category, explained, _ := classify(kind, toolName, payload)
	return category, explained
}

// classify additionally returns the literal that matched, which is what the
// signature reads to pick the evidence line. An empty literal means the
// category came from a recorded field rather than from output text.
func classify(kind, toolName string, payload map[string]any) (string, Rule, string) {
	if reason := strings.TrimSpace(payloadString(payload, "abort_reason")); reason == abortInterrupted {
		return CategoryUserInterrupt, rule(fmt.Sprintf("Codex 记录了 turn_aborted 且 reason=%q", abortInterrupted),
			fmt.Sprintf("Codex recorded turn_aborted with reason=%q", abortInterrupted)), ""
	}
	if kind == KindUserInterrupt || strings.HasPrefix(strings.TrimSpace(payloadString(payload, "text")), interruptPrefix) {
		return CategoryUserInterrupt, rule(fmt.Sprintf("消息文本以 %q 开头", interruptPrefix),
			fmt.Sprintf("The message text begins with %q", interruptPrefix)), interruptPrefix
	}
	output := payloadString(payload, "tool_output")
	command := commandForm(payload)
	lowered := strings.ToLower(output)
	exitCode, hasExitCode := payloadInt(payload, "exit_code")
	for _, candidate := range outputRules {
		if literal, ok := candidate.matchLiteral(output, lowered, command, toolName); ok {
			switch {
			case candidate.needsTest:
				return candidate.category, rule(fmt.Sprintf("工具是测试命令且输出包含 %q", literal),
					fmt.Sprintf("The tool is a test command and its output contains %q", literal)), literal
			case candidate.needsBuild:
				return candidate.category, rule(fmt.Sprintf("工具是构建命令且输出包含 %q", literal),
					fmt.Sprintf("The tool is a build command and its output contains %q", literal)), literal
			default:
				return candidate.category, rule(fmt.Sprintf("输出包含 %q", literal),
					fmt.Sprintf("The output contains %q", literal)), literal
			}
		}
		if hasExitCode && containsInt(candidate.exitCodes, exitCode) {
			return candidate.category, rule(candidate.exitReason, candidate.exitReasonEN), ""
		}
	}
	if hasExitCode && exitCode != 0 {
		if outcome, ok := LookupExit(command, exitCode); ok {
			explained := rule(outcome.Meaning, outcome.MeaningEN)
			if !outcome.IsFailure {
				return CategoryExpectedExit, explained, ""
			}
			if outcome.Code == timeoutExitCode {
				return CategoryTimeout, explained, ""
			}
			return CategoryNonzeroExit, explained, ""
		}
		return CategoryNonzeroExit, rule(fmt.Sprintf("明确记录 exit_code=%d 且未命中更具体规则", exitCode),
			fmt.Sprintf("exit_code=%d was recorded explicitly and no more specific rule matched", exitCode)), ""
	}
	if isError, ok := payloadBool(payload, "is_error"); ok && isError {
		return CategoryToolError, rule("明确记录 is_error=true 且未命中更具体规则",
			"is_error=true was recorded explicitly and no more specific rule matched"), ""
	}
	return "", Rule{}, ""
}

func rule(text, english string) Rule { return Rule{Text: text, EN: english} }

func (r literalRule) matchLiteral(output, lowered, command, toolName string) (string, bool) {
	if r.needsCmd && !isShellCommand(toolName, command) {
		return "", false
	}
	if r.needsTest && !matchesAny(strings.ToLower(command), testCommands) {
		return "", false
	}
	if r.needsBuild && !matchesAny(strings.ToLower(command), buildCommands) {
		return "", false
	}
	return firstLiteral(output, lowered, r.literals)
}

func containsInt(values []int, want int) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// firstLiteral matches an all-lowercase literal case-insensitively (it is a
// phrase) and a literal carrying uppercase case-sensitively (it is a symbol,
// such as ENOENT or FAIL, whose case is part of the evidence).
func firstLiteral(output, lowered string, literals []string) (string, bool) {
	for _, literal := range literals {
		if literal == strings.ToLower(literal) {
			if strings.Contains(lowered, literal) {
				return literal, true
			}
			continue
		}
		if strings.Contains(output, literal) {
			return literal, true
		}
	}
	return "", false
}

// Signature is the recurrence key: the same category, the same tool and the
// same normalized evidence line are the same friction, whichever session it
// happened in. An unclassified record has no signature.
//
// program is the program the paired tool call ran, as the command projection
// reads it, and is used only by the last fallback below. Pass "" when the
// source did not record a command.
//
// The evidence line is picked in four steps, first match wins:
//
//  1. the first line carrying the literal the category rule matched; a section
//     divider ("=== FAILURES ===") is never that line — see
//     evidenceAfterDecoration;
//  2. for a Python traceback, its last non-empty line — the exception type and
//     message, which is the part that tells two tracebacks apart;
//  3. the first content line naming a failure (error / fail / fatal / cannot /
//     denied / not found / exception / panic / refused), skipping the lines the
//     harness wraps around the program's own output;
//  4. no such line: "<program> exit <N>", so a failure whose output says
//     nothing distinguishing groups by what ran rather than by a chunk id.
func Signature(category, toolName string, payload map[string]any, program string) string {
	if category == "" {
		return ""
	}
	kind := KindToolError
	if category == CategoryUserInterrupt {
		kind = KindUserInterrupt
	}
	_, _, literal := classify(kind, toolName, payload)
	if reason := strings.TrimSpace(payloadString(payload, "abort_reason")); reason != "" {
		return category + "|" + toolName + "|" + NormalizeLine(reason)
	}
	text := payloadString(payload, "tool_output")
	if strings.TrimSpace(text) == "" {
		text = payloadString(payload, "text")
	}
	if line, ok := evidenceLine(text, literal); ok {
		return category + "|" + toolName + "|" + NormalizeLine(line)
	}
	return category + "|" + toolName + "|" + outcomeLine(toolName, program, payload, text)
}

// outcomeLine is the fallback evidence: what ran and how it ended. The exit
// code is appended after normalization so two different exit codes of the same
// program stay two different signatures.
func outcomeLine(toolName, program string, payload map[string]any, text string) string {
	subject := strings.ToLower(strings.TrimSpace(program))
	if exitCode, ok := payloadInt(payload, "exit_code"); ok && exitCode != 0 {
		// When the exit-code table explained this code, it explained it for one
		// program — the one whose status the shell reported. Naming any other
		// program here would put "rg 没有匹配" next to "python exit 1".
		if outcome, matched := LookupExit(commandForm(payload), exitCode); matched && outcome.Program != "" {
			subject = outcome.Program
		}
	}
	if subject == "" {
		subject = strings.ToLower(strings.TrimSpace(toolName))
	}
	if subject == "" {
		// Nothing names what ran, so the first recorded line is still better
		// evidence than an empty signature.
		return NormalizeLine(firstContentLine(text))
	}
	if exitCode, ok := payloadInt(payload, "exit_code"); ok && exitCode != 0 {
		return subject + " exit " + strconv.Itoa(exitCode)
	}
	if isError, ok := payloadBool(payload, "is_error"); ok && isError {
		return subject + " tool_error"
	}
	return NormalizeLine(firstContentLine(text))
}

// wrapperPrefixes are the lines a harness prints around the program's own
// output. Codex's exec tool frames every result with a chunk id, a wall time
// and a status line; signing on those would group unrelated failures together
// under a run identifier. They are skipped when picking the fallback line, and
// only then — a line that carries the matched literal is always the evidence.
var wrapperPrefixes = []string{
	"chunk id:", "wall time", "process exited with code",
	"original token count:", "output:", "script failed", "script error:",
	"script completed", "total output lines:", "warning: truncated output",
}

// tracebackHeader is the line Python prints before a traceback. Its own text is
// identical in every traceback, so the distinguishing line is the last one.
const tracebackHeader = "Traceback (most recent call last)"

// failureWordPattern names the words a failing program or a harness uses to
// say the call did not go through. It is what picks the evidence line when the
// category came from a recorded exit code rather than from a literal in the
// output.
var failureWordPattern = regexp.MustCompile(`(?i)error|fail|fatal|cannot|denied|not found|exception|panic|refused|blocked|rejected|unable to`)

// evidenceLine returns the line the signature is built from and whether the
// output holds one at all. Steps 1-3 of the rule documented on Signature live
// here; ok=false means step 4 has to name the outcome instead.
func evidenceLine(text, literal string) (string, bool) {
	lines := contentLines(text)
	if literal != "" {
		if index, ok := indexContaining(lines, literal); ok {
			if line, ok := evidenceAfterDecoration(lines, index, literal); ok {
				return line, true
			}
		}
	}
	if strings.Contains(text, tracebackHeader) {
		if line := lastContentLine(text); line != "" {
			return line, true
		}
	}
	for _, line := range lines {
		if hasWrapperPrefix(line) || isDecorationLine(line) {
			continue
		}
		if failureWordPattern.MatchString(line) {
			return line, true
		}
	}
	return "", false
}

// contentLines are the output's non-empty lines, trimmed.
func contentLines(text string) []string {
	raw := strings.Split(text, "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// indexContaining matches an all-lowercase literal case-insensitively (it is a
// phrase) and a literal carrying uppercase case-sensitively (it is a symbol
// whose case is part of the evidence), the same way firstLiteral does.
func indexContaining(lines []string, literal string) (int, bool) {
	lowered := strings.ToLower(literal)
	caseInsensitive := literal == lowered
	for index, line := range lines {
		if caseInsensitive {
			if strings.Contains(strings.ToLower(line), lowered) {
				return index, true
			}
			continue
		}
		if strings.Contains(line, literal) {
			return index, true
		}
	}
	return 0, false
}

// evidenceAfterDecoration keeps the signature off a section divider. A line
// made of repeated =, -, _, *, # and friends is a heading rule, not evidence:
// pytest's "=== FAILURES ===" and "--- Captured stderr ---" are identical in
// every run, so signing on them collapses unrelated failures into one group.
//
// When the matched literal sits on such a line, the same literal further down
// is preferred (pytest repeats it as "FAILED tests/x.py::test_y - ..."), and
// only failing that does the first following content line stand.
func evidenceAfterDecoration(lines []string, index int, literal string) (string, bool) {
	if !isDecorationLine(lines[index]) {
		return lines[index], true
	}
	rest := lines[index+1:]
	if next, ok := indexContaining(rest, literal); ok && !isDecorationLine(rest[next]) {
		return rest[next], true
	}
	for _, line := range rest {
		if !isDecorationLine(line) && !hasWrapperPrefix(line) {
			return line, true
		}
	}
	return "", false
}

// decorationRunes are what a program draws a divider out of.
const decorationRunes = "=-_*~#!+"

// decorationRun is the minimum repeat count that makes a run a divider rather
// than punctuation: "--- a/file" keeps its meaning, "------" does not.
const decorationRun = 3

// isDecorationLine reports whether a line is a divider: either made only of
// divider characters and spaces, or wrapped in a divider run on both ends.
func isDecorationLine(line string) bool {
	line = strings.TrimSpace(line)
	if len([]rune(line)) < decorationRun {
		return false
	}
	if strings.IndexFunc(line, func(r rune) bool {
		return r != ' ' && !strings.ContainsRune(decorationRunes, r)
	}) < 0 {
		return true
	}
	return edgeRun(line, false) >= decorationRun && edgeRun(line, true) >= decorationRun
}

// edgeRun counts the repeated divider character at one end of the line.
func edgeRun(line string, fromEnd bool) int {
	runes := []rune(line)
	at := func(index int) rune {
		if fromEnd {
			return runes[len(runes)-1-index]
		}
		return runes[index]
	}
	first := at(0)
	if !strings.ContainsRune(decorationRunes, first) {
		return 0
	}
	count := 0
	for count < len(runes) && at(count) == first {
		count++
	}
	return count
}

// lastContentLine is the last non-empty line that is neither harness framing
// nor a divider.
func lastContentLine(text string) string {
	lines := contentLines(text)
	for index := len(lines) - 1; index >= 0; index-- {
		if !hasWrapperPrefix(lines[index]) && !isDecorationLine(lines[index]) {
			return lines[index]
		}
	}
	return ""
}

// firstContentLine is the first line of the program's own output; if the
// output is nothing but harness framing and dividers, the very first line
// stands so the signature is never empty.
func firstContentLine(text string) string {
	lines := contentLines(text)
	if len(lines) == 0 {
		return ""
	}
	for _, line := range lines {
		if !hasWrapperPrefix(line) && !isDecorationLine(line) {
			return line
		}
	}
	return lines[0]
}

func hasWrapperPrefix(line string) bool {
	lowered := strings.ToLower(line)
	for _, prefix := range wrapperPrefixes {
		if strings.HasPrefix(lowered, prefix) {
			return true
		}
	}
	return false
}

var (
	exitPrefixPattern = regexp.MustCompile(`^exit code:?\s*-?\d+\s*[:.\-]?\s*`)
	digitRunPattern   = regexp.MustCompile(`\d+`)
	spaceRunPattern   = regexp.MustCompile(`\s+`)
)

const signatureLineBound = 120

// NormalizeLine turns one recorded output line into the stable half of a
// signature: the "Exit code N" prefix goes, the text is lowercased, absolute
// paths keep only their last segment, digit runs become '#', and the result is
// bounded to 120 characters.
func NormalizeLine(line string) string {
	line = strings.ToLower(strings.TrimSpace(line))
	line = exitPrefixPattern.ReplaceAllString(line, "")
	line = spaceRunPattern.ReplaceAllString(line, " ")
	line = shortenAbsolutePaths(line)
	line = digitRunPattern.ReplaceAllString(line, "#")
	return boundRunes(strings.TrimSpace(line), signatureLineBound)
}

func shortenAbsolutePaths(line string) string {
	fields := strings.Split(line, " ")
	for index, field := range fields {
		token := strings.Trim(field, `'"`+"`"+`,;()[]{}<>`)
		if !isAbsolutePath(token) {
			continue
		}
		cut := strings.LastIndexAny(strings.TrimRight(token, `/\`), `/\`)
		if cut < 0 || cut+1 >= len(token) {
			continue
		}
		fields[index] = strings.Replace(field, token, token[cut+1:], 1)
	}
	return strings.Join(fields, " ")
}

func isAbsolutePath(token string) bool {
	if len(token) < 2 {
		return false
	}
	if token[0] == '/' || token[0] == '\\' {
		return true
	}
	return len(token) > 2 && token[1] == ':' && (token[2] == '/' || token[2] == '\\')
}

func boundRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func matchesAny(value string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func isShellCommand(toolName, command string) bool {
	if command != "" {
		return true
	}
	return matchesAny(strings.ToLower(strings.TrimSpace(toolName)), shellTools)
}

// commandForm returns the shell command recorded in the bounded tool input, or
// the raw bounded tool input when it is not a JSON object.
func commandForm(payload map[string]any) string {
	input := strings.TrimSpace(payloadString(payload, "tool_input"))
	if input == "" {
		return ""
	}
	var decoded map[string]any
	if json.Unmarshal([]byte(input), &decoded) != nil {
		return input
	}
	for _, key := range []string{"command", "cmd", "script"} {
		if value, ok := decoded[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return input
}

func payloadString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}

func payloadBool(payload map[string]any, key string) (bool, bool) {
	switch value := payload[key].(type) {
	case bool:
		return value, true
	case string:
		parsed, err := strconv.ParseBool(value)
		return parsed, err == nil
	default:
		return false, false
	}
}

func payloadInt(payload map[string]any, key string) (int, bool) {
	switch value := payload[key].(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		if value != float64(int(value)) {
			return 0, false
		}
		return int(value), true
	case json.Number:
		parsed, err := strconv.Atoi(string(value))
		return parsed, err == nil
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		return parsed, err == nil
	default:
		return 0, false
	}
}
