package transport

import (
	"regexp"
	"strings"
)

// CrossReferences are explicit bridge syntax, not arbitrary @words. Rewriting
// is limited to references already authorized by the canonical router.
var crossReferenceTokenPattern = regexp.MustCompile(`@\{[A-Za-z0-9][A-Za-z0-9_-]{0,63}/P[1-9][0-9]*\}`)

// RewriteCrossReferences preserves unresolved tokens and escaped literals.
// Callbacks may return a provider-native mention label only after verifying
// membership and platform-specific privacy conditions.
func RewriteCrossReferences(text string, refs []CrossReference, native func(CrossReference) (string, bool)) string {
	if len(refs) == 0 || native == nil {
		return text
	}
	authorized := make(map[string]CrossReference, len(refs))
	for _, ref := range refs {
		if ref.Token != "" {
			authorized[ref.Token] = ref
		}
	}
	last := 0
	var result strings.Builder
	for _, loc := range crossReferenceTokenPattern.FindAllStringIndex(text, -1) {
		token := text[loc[0]:loc[1]]
		ref, ok := authorized[token]
		if !ok {
			continue
		}
		// An odd number of backslashes escapes this occurrence.
		backslashes := 0
		for i := loc[0] - 1; i >= 0 && text[i] == '\\'; i-- {
			backslashes++
		}
		if backslashes%2 == 1 {
			continue
		}
		replacement, ok := native(ref)
		if !ok {
			continue
		}
		result.WriteString(text[last:loc[0]])
		result.WriteString(replacement)
		last = loc[1]
	}
	if last == 0 {
		return text
	}
	result.WriteString(text[last:])
	return result.String()
}
