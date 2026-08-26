package canonical

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	exitCodePattern = regexp.MustCompile(`(?im)^\s*exit\s+(?:code|status)\s*[:=]?\s*([0-9]+)\b`)
	// Codex's exec_command/exec wrapper prints its own header before the
	// program output; the status line is the only place the exit code appears.
	processExitPattern = regexp.MustCompile(`(?m)^.*\bProcess exited with code (\d+)`)
)

// NormalizeToolFailure reads an explicit failure out of a recorded tool result.
// Explicit fields win; the text rules below only fire when the source recorded
// no field, and each one matches a literal status line a harness prints, never
// a message that merely contains the word "error".
func NormalizeToolFailure(output string, explicitIsError *bool, explicitExitCode *int) (*bool, *int) {
	var isError *bool
	if explicitIsError != nil {
		value := *explicitIsError
		isError = &value
	} else if strings.Contains(output, "<tool_use_error>") {
		value := true
		isError = &value
	} else if strings.HasPrefix(strings.TrimSpace(output), "Script failed") {
		// Codex's exec tool reports a failed JS script with this prefix and no
		// exit code of its own.
		value := true
		isError = &value
	}
	var exitCode *int
	if explicitExitCode != nil {
		value := *explicitExitCode
		exitCode = &value
	} else if value, ok := firstIntMatch(exitCodePattern, output); ok {
		exitCode = &value
	} else if value, ok := firstIntMatch(processExitPattern, output); ok {
		exitCode = &value
	}
	return isError, exitCode
}

func firstIntMatch(pattern *regexp.Regexp, output string) (int, bool) {
	matches := pattern.FindStringSubmatch(output)
	if len(matches) != 2 {
		return 0, false
	}
	value, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0, false
	}
	return value, true
}
