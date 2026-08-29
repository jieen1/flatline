package canonical

import (
	"os"
	"strings"
	"testing"
)

// A message that arrives under the user role but that no human typed must not
// be counted as a user turn. The cases below are the shapes seen in local
// transcripts; the teammate-message one is what the accuracy audit caught,
// where a message another agent sent was counted as a turn the user took.
func TestInjectedMessagePrefixRecognizesHarnessWrittenBlocks(t *testing.T) {
	for _, testCase := range []struct {
		name string
		text string
		want bool
	}{
		{name: "teammate message carries attributes before the closing angle",
			text: "<teammate-message teammate_id=\"team-lead\" summary=\"审计测试冗余\">\n你是审计员。\n</teammate-message>",
			want: true},
		{name: "teammate message with no attributes",
			text: "<teammate-message>hello</teammate-message>", want: true},
		{name: "leading whitespace does not hide the block",
			text: "\n  <teammate-message teammate_id=\"main\">go</teammate-message>", want: true},
		{name: "system reminder", text: "<system-reminder>\nbe careful\n</system-reminder>", want: true},
		{name: "task notification", text: "<task-notification>done</task-notification>", want: true},
		{name: "agents md instructions", text: "# AGENTS.md instructions\n\nread this", want: true},
		{name: "a user message that merely mentions the tag is still a user turn",
			text: "why does <teammate-message> show up in my counts?", want: false},
		{name: "an angle-bracketed word the harness does not write is a user turn",
			text: "<WORKFLOW> plan the next step", want: false},
		{name: "ordinary text", text: "please run the tests", want: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			prefix := InjectedMessagePrefix(testCase.text)
			if got := prefix != ""; got != testCase.want {
				t.Fatalf("InjectedMessagePrefix(%q) = %q, injected=%t, want injected=%t",
					testCase.text, prefix, got, testCase.want)
			}
		})
	}
}

// The SQL predicate and the Go check read the same list, so a block the reader
// leaves out of new transcripts is also left out of the counts for the records
// an older parser already stored.
func TestNotInjectedSQLCoversEveryPrefix(t *testing.T) {
	predicate := NotInjectedSQL("text")
	for _, prefix := range InjectedMessagePrefixes {
		if !strings.Contains(predicate, likeEscape(prefix)) {
			t.Errorf("prefix %q is not in the SQL predicate; stored records carrying it would still be counted", prefix)
		}
	}
}

// scripts/audit_accuracy.py is the reconciliation gate: it counts a transcript
// itself and compares that count with the API's. Its answer is only ground
// truth while it leaves out exactly the blocks this list names — a prefix
// present here but missing there turns a correct count into a reported
// mismatch, and someone is then told to "fix the parser" that was right.
// The script's comment claimed the two were kept in step; this is what makes
// that true.
func TestAuditScriptMirrorsInjectedPrefixes(t *testing.T) {
	const script = "../../scripts/audit_accuracy.py"
	body, err := os.ReadFile(script)
	if err != nil {
		t.Fatalf("read %s: %v", script, err)
	}
	text := string(body)
	for _, prefix := range InjectedMessagePrefixes {
		if !strings.Contains(text, `"`+prefix+`"`) {
			t.Errorf("%s does not list %q; the gate would report a correct count as a mismatch", script, prefix)
		}
	}
}
