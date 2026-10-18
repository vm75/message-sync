package identity

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

const AliasPoolSize int64 = 256 * 256

// AliasTokenPattern is bounded and ASCII-only so parsing and SQLite NOCASE
// agree. Vocabulary membership and assignment are checked by the store.
const AliasTokenPattern = `@\{([A-Za-z]{2,16} [A-Za-z]{2,16}(?: [2-9]| [1-9][0-9]{1,14})?)\}`

var aliasToken = regexp.MustCompile(`^` + AliasTokenPattern + `$`)

// SyncSetAlias maps a permanent instance-wide ordinal to a collision-free name.
// Its assignment belongs to one (sync set, actor), even though ordinals are global.
func SyncSetAlias(ordinal int64) (string, error) {
	return syncSetAlias(ordinal, true)
}

// SequentialSyncSetAlias preserves the original issued ordering for databases
// that allocated aliases before the scrambled ordering was introduced.
func SequentialSyncSetAlias(ordinal int64) (string, error) {
	return syncSetAlias(ordinal, false)
}

func syncSetAlias(ordinal int64, scrambled bool) (string, error) {
	if ordinal < 1 {
		return "", errors.New("alias ordinal must be positive")
	}
	zero := ordinal - 1
	slot := uint16(zero % AliasPoolSize)
	if scrambled {
		slot = scrambledAliasSlot(slot)
	}
	alias := aliasAdjectives[slot/256] + " " + aliasNouns[slot%256]
	if tier := zero/AliasPoolSize + 1; tier > 1 {
		alias += " " + strconv.FormatInt(tier, 10)
	}
	return alias, nil
}

// scrambledAliasSlot is a frozen permutation of all 16-bit slots. XOR shifts
// and multiplication by odd numbers are individually invertible, so composition
// scrambles consecutive ordinals without collisions. uint16 overflow is deliberate.
// This obscures the visual sequence; it is not a secret or cryptographic identity.
func scrambledAliasSlot(slot uint16) uint16 {
	slot ^= slot >> 7
	slot *= 0x9e37
	slot ^= slot >> 9
	slot *= 0x85eb
	slot ^= slot >> 8
	return slot
}

// ParseAliasToken accepts an entire explicit reference, never a substring or
// a provider-native mention. It preserves source spelling for literal fallback.
func ParseAliasToken(token string) (string, bool) {
	match := aliasToken.FindStringSubmatch(token)
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

// ValidAlias checks the bounded address grammar without allocating an identity.
func ValidAlias(alias string) bool {
	_, ok := ParseAliasToken("@{" + alias + "}")
	return ok && !strings.ContainsAny(alias, "{}")
}

// AliasTokenSpans finds whole, unescaped, unnested references. Both authorization
// and rendering use these boundaries so a token authorized elsewhere cannot
// accidentally activate an escaped or malformed occurrence of the same text.
func AliasTokenSpans(text string) [][2]int {
	var spans [][2]int
	for start := 0; start < len(text); {
		offset := strings.Index(text[start:], "@{")
		if offset < 0 {
			break
		}
		begin := start + offset
		close := strings.IndexByte(text[begin:], '}')
		if close < 0 {
			break
		}
		end := begin + close + 1
		start = end
		if _, ok := ParseAliasToken(text[begin:end]); !ok {
			continue
		}
		backslashes := 0
		for i := begin - 1; i >= 0 && text[i] == '\\'; i-- {
			backslashes++
		}
		if backslashes%2 == 0 {
			spans = append(spans, [2]int{begin, end})
		}
	}
	return spans
}
