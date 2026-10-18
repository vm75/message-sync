package transport

import (
	"github.com/vm75/message-sync/internal/identity"
	"strings"
)

// RewriteCrossReferences preserves unresolved tokens and escaped literals.
// Callbacks may return a provider-native mention label only after verifying
// membership and platform-specific privacy conditions.
func RewriteCrossReferences(text string, refs []CrossReference, native func(CrossReference) (string, bool)) string {
	if len(refs) == 0 || native == nil {
		return text
	}
	authorized := make(map[string]CrossReference, len(refs))
	for _, ref := range refs {
		if ref.Token != "" && ref.ActorID != "" {
			authorized[ref.Token] = ref
		}
	}
	last := 0
	var result strings.Builder
	// Authorization and syntax validation belong to the router. Match exact
	// tokens here, including tokens HTML-escaped by a destination adapter.
	for _, span := range identity.AliasTokenSpans(text) {
		begin, end := span[0], span[1]
		token := text[begin:end]
		ref, ok := authorized[token]
		if !ok {
			continue
		}
		replacement, ok := native(ref)
		if !ok || replacement == "" {
			continue
		}
		result.WriteString(text[last:begin])
		result.WriteString(replacement)
		last = end
	}
	if last == 0 {
		return text
	}
	result.WriteString(text[last:])
	return result.String()
}

// ReferenceDisplayLabel renders the assigned address without guessing identity.
func ReferenceDisplayLabel(ref CrossReference, anonymized bool) string {
	if !anonymized && strings.TrimSpace(ref.Name) != "" && ref.Name != ref.Alias {
		return ref.Name + " (" + ref.Alias + ")"
	}
	return ref.Alias
}
