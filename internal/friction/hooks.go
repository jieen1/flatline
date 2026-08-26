package friction

import (
	"path/filepath"
	"strings"
)

// A hook block recorded in a transcript is evidence that the hook ran: the
// harness only writes "blocked by PreToolUse hook" after asking the hook and
// getting an answer. Whether that evidence can be attached to a registered
// hook asset depends on whether the message names the hook at all — many
// harnesses print only the hook's own message. HookReferences reads out the
// names a message does carry; a message that carries none produces none, and
// nothing is guessed from the surrounding text.

// hookFileExtensions are what a hook script or a hook configuration file is
// written in. A path with any other extension is not read as a hook.
var hookFileExtensions = map[string]struct{}{
	".py": {}, ".sh": {}, ".js": {}, ".mjs": {}, ".cjs": {}, ".ts": {}, ".json": {}, ".bash": {}, ".zsh": {},
}

// hookReferenceTrim are the characters a message wraps a name in.
const hookReferenceTrim = "\"'`()[]{}<>,;:.！。，、"

// HookReferences returns the hook identifiers a recorded message names: every
// token that is a path to a hook file, and every quoted or bracketed token
// that could be a hook's own name. Both are candidates only — the caller
// decides whether one of them matches a registered hook asset.
func HookReferences(text string) []string {
	if text == "" {
		return nil
	}
	out := make([]string, 0, 2)
	seen := make(map[string]struct{}, 2)
	add := func(value string) {
		if value == "" {
			return
		}
		if _, repeated := seen[value]; repeated {
			return
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	for _, field := range strings.FieldsFunc(text, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '"' || r == '\'' || r == '`'
	}) {
		token := strings.Trim(field, hookReferenceTrim)
		if token == "" {
			continue
		}
		if _, isHookFile := hookFileExtensions[strings.ToLower(filepath.Ext(token))]; !isHookFile {
			continue
		}
		if strings.ContainsAny(token, "/\\") {
			add(filepath.Clean(token))
			continue
		}
		// A bare file name is a name, not a path: it identifies a hook only
		// when exactly one registered hook is called that.
		add(token)
	}
	return out
}

// HookName is the name part of a hook reference: the file name without its
// extension, which is what a hook asset is named after.
func HookName(reference string) string {
	base := filepath.Base(reference)
	return strings.TrimSuffix(base, filepath.Ext(base))
}
