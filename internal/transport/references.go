package transport

import "strings"

// NormalizeParticipantLabel provides exact, case-insensitive name matching
// with provider whitespace collapsed consistently at every lookup boundary.
func NormalizeParticipantLabel(label string) string {
	return strings.ToLower(strings.Join(strings.Fields(label), " "))
}

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
	for start := 0; start < len(text); {
		offset := strings.Index(text[start:], "@{")
		if offset < 0 {
			break
		}
		begin := start + offset
		end := strings.IndexByte(text[begin:], '}')
		if end < 0 {
			break
		}
		end += begin + 1
		start = end
		token := text[begin:end]
		ref, ok := authorized[token]
		if !ok {
			// An unmatched opener may precede an authorized token, which
			// the router still recognizes independently.
			start = begin + 2
			continue
		}
		// An odd number of backslashes escapes this occurrence.
		backslashes := 0
		for i := begin - 1; i >= 0 && text[i] == '\\'; i-- {
			backslashes++
		}
		if backslashes%2 == 1 {
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

// ReferenceDisplayLabel selects a destination-safe label without changing the
// addressed identity. A configured pseudonym is preferred in anonymized output;
// numeric handles are a fallback, never the primary anonymization scheme.
func ReferenceDisplayLabel(ref CrossReference, anonymized bool) string {
	if anonymized && strings.TrimSpace(ref.Pseudonym) != "" {
		return ref.Pseudonym
	}
	if !anonymized && strings.TrimSpace(ref.Name) != "" {
		return ref.Name
	}
	if strings.TrimSpace(ref.Handle) != "" {
		return ref.Handle
	}
	return "unknown"
}
