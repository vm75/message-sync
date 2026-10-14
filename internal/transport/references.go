package transport

import (
	"regexp"
	"strings"
)

// CrossReferences are explicit bridge syntax, not arbitrary @words. Rewriting
// is limited to references already authorized by the canonical router.
var crossReferenceTokenPattern = regexp.MustCompile(`@\{([A-Za-z0-9][A-Za-z0-9_-]{0,63})/([^{}\r\n]{1,96})\}`)
var stableReferenceHandle = regexp.MustCompile(`^P[1-9][0-9]*package transport

import (
	"regexp"
	"strings"
)

// CrossReferences are explicit bridge syntax, not arbitrary @words. Rewriting
// is limited to references already authorized by the canonical router.
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

// FormatCrossReferences turns the explicit bridge input syntax into readable
// destination text whether or not a provider can notify the participant.
// Existing native @mentions are never parsed. Only a resolved, authorized
// CrossReference may be passed to the native callback.
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

func FormatCrossReferences(content string, refs []CrossReference, anonymized bool, native func(CrossReference) (string, bool)) string {
	authorized := make(map[string]CrossReference, len(refs))
	for _, ref := range refs {
		if ref.Token != "" && ref.ActorID != "" {
			authorized[ref.Token] = ref
		}
	}
	last := 0
	var result strings.Builder
	for _, loc := range crossReferenceTokenPattern.FindAllStringSubmatchIndex(content, -1) {
		backslashes := 0
		for j := loc[0] - 1; j >= 0 && content[j] == '\\'; j-- {
			backslashes++
		}
		if backslashes%2 == 1 {
			continue
		}
		token := content[loc[0]:loc[1]]
		endpoint := content[loc[2]:loc[3]]
		rawLabel := content[loc[4]:loc[5]]
		ref, resolved := authorized[token]
		label := rawLabel
		if resolved {
			label = ReferenceDisplayLabel(ref, anonymized)
		} else if anonymized && !stableReferenceHandle.MatchString(rawLabel) {
			// An unresolved real name must never be copied into an
			// anonymization-enabled Discord destination as a reference.
			label = "unknown"
		}
		if strings.TrimSpace(label) == "" {
			label = "unknown"
		}
		label = strings.NewReplacer("@", "", "{", "(", "}", ")", "\n", " ", "\r", " ").Replace(label)
		replacement := "@" + endpoint + "/" + label
		if resolved && native != nil {
			if providerText, ok := native(ref); ok && providerText != "" {
				replacement = providerText
			}
		}
		result.WriteString(content[last:loc[0]])
		result.WriteString(replacement)
		last = loc[1]
	}
	if last == 0 {
		return content
	}
	result.WriteString(content[last:])
	return result.String()
}
