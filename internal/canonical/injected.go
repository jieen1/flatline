package canonical

import "strings"

// A harness writes blocks into a transcript under the user role that no user
// typed: the environment context it prepends to a turn, the instruction files
// it inlines, the marker it leaves where a turn was cut short, the
// notification it posts when a subagent finishes. Counting them as user turns
// overstates how much the user said — on this machine 485 of 6,609 stored user
// records, 7.3%, are one of these.
//
// The list lives here because two layers need the same closed set: the reader
// leaves them out of the transcript in the first place, and the session
// projection leaves them out of its counts for the records an older parser
// stored before the rule existed. Events are append-only, so an already-stored
// injected record stays where it is and is excluded at count time instead.
var InjectedMessagePrefixes = []string{
	"<local-command-caveat>", "<command-name>", "<command-message>", "<command-args>",
	"<local-command-stdout>", "<local-command-stderr>", "<local-command-result>",
	"<system-reminder>", "<task-notification>", "<subagent_notification>",
	"<environment_context>", "<user_instructions>", "<recommended_plugins>", "<turn_aborted>",
	"<fork-boilerplate>",
	userShellOpen, "<bash-stdout>", "<bash-stderr>",
	"# AGENTS.md instructions",
	"Async agent launched successfully", "(Bash completed", "File does not exist",
}

// A command the user ran themselves — Claude Code's `!` prefix — is written
// into the transcript under the user role as <bash-input>…</bash-input>, with
// what it printed following as <bash-stdout> and <bash-stderr>. Nobody typed
// any of it as a message, so all three are left out of the turn counts with
// the rest of the injected blocks. The input line is kept in the transcript
// anyway, because it is the only record that the command ran at all: the
// command projection reads it from there and records it under UserShellTool.
const (
	userShellOpen  = "<bash-input>"
	userShellClose = "</bash-input>"

	// UserShellTool is the tool name a command the user ran directly is
	// recorded under. It is not a tool the model called, so it is deliberately
	// not one of the harness's own tool names.
	UserShellTool = "user_shell"
)

// UserShellCommand returns the command inside a <bash-input> block, and false
// for any other text.
func UserShellCommand(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, userShellOpen) {
		return "", false
	}
	body := text[len(userShellOpen):]
	if end := strings.LastIndex(body, userShellClose); end >= 0 {
		body = body[:end]
	}
	command := strings.TrimSpace(body)
	return command, command != ""
}

// UserShellTextLike is the LIKE pattern that finds a stored user-shell record
// by its recorded text. It is a prefilter only — the match is decided by
// UserShellCommand — and it has to run against json_extract of the payload
// rather than the payload itself, because the stored JSON escapes < and >.
func UserShellTextLike() string { return "%" + userShellOpen + "%" }

// InjectedMessagePrefix returns the prefix that identifies text as one of
// those blocks, or "" when none does.
func InjectedMessagePrefix(text string) string {
	text = strings.TrimSpace(text)
	for _, prefix := range InjectedMessagePrefixes {
		if strings.HasPrefix(text, prefix) {
			return prefix
		}
	}
	return ""
}

// NotInjectedSQL is the predicate that excludes those blocks from a count over
// stored events. expr is the SQL expression holding the recorded text. It is
// built from the same list the reader uses, so the two cannot drift apart.
func NotInjectedSQL(expr string) string {
	var b strings.Builder
	b.WriteString("NOT (")
	for index, prefix := range InjectedMessagePrefixes {
		if index > 0 {
			b.WriteString(" OR ")
		}
		// The list is a compile-time constant; no prefix may carry a quote,
		// and the LIKE wildcards a real prefix does carry are escaped.
		if strings.ContainsAny(prefix, `'"`+"`") {
			panic("canonical: an injected message prefix must not be quoted: " + prefix)
		}
		b.WriteString(expr)
		b.WriteString(" LIKE '")
		b.WriteString(likeEscape(prefix))
		b.WriteString(`%' ESCAPE '\'`)
	}
	b.WriteString(")")
	return b.String()
}

// likeEscape escapes the two wildcards LIKE reads, so a prefix such as
// <subagent_notification> matches itself and not <subagentXnotification>.
func likeEscape(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}
