package identity

import (
	"crypto/sha256"
	"fmt"
	"math"
	"regexp"
	"strings"
	"testing"
)

func TestAliasVocabularyFrozenAndBijective(t *testing.T) {
	if len(aliasAdjectives) != 256 || len(aliasNouns) != 256 {
		t.Fatal("alias vocabulary must contain exactly 256 adjectives and nouns")
	}
	word := regexp.MustCompile(`^[A-Z][a-z]{1,15}$`)
	for _, list := range [][]string{aliasAdjectives[:], aliasNouns[:]} {
		seen := make(map[string]bool)
		for _, entry := range list {
			key := strings.ToLower(entry)
			if !word.MatchString(entry) || seen[key] {
				t.Fatalf("invalid or duplicate vocabulary entry %q", entry)
			}
			seen[key] = true
		}
	}
	checksum := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(aliasAdjectives[:], " ")+"\n"+strings.Join(aliasNouns[:], " "))))
	if checksum != "5a0543c85c497c2b8348971bb0b2802446c024cbfaa42eeec24be8aa163e6d31" {
		t.Fatal("alias vocabulary changed: issued addresses must remain frozen")
	}
	seen := make(map[string]bool)
	for ordinal := int64(1); ordinal <= AliasPoolSize; ordinal++ {
		alias, err := SyncSetAlias(ordinal)
		key := strings.ToLower(alias)
		if err != nil || !ValidAlias(alias) || seen[key] || len(strings.Fields(alias)) != 2 {
			t.Fatalf("invalid/nonunique pair at %d: %q %v", ordinal, alias, err)
		}
		seen[key] = true
		// Reserve ample room within Discord's 80-rune username for every tier.
		if len(alias)+1+15 > 48 {
			t.Fatalf("alias exceeds presentation budget: %q", alias)
		}
	}
}

func TestAliasBoundariesAndTokens(t *testing.T) {
	for _, tc := range []struct {
		ordinal int64
		want    string
	}{
		{1, "Amber Badger"}, {256, "Amber Rainbow"}, {257, "Azure Badger"},
		{65536, "Spring Rainbow"}, {65537, "Amber Badger 2"},
		{131072, "Spring Rainbow 2"}, {131073, "Amber Badger 3"},
	} {
		got, err := SyncSetAlias(tc.ordinal)
		if err != nil || got != tc.want {
			t.Fatalf("alias(%d)=%q, %v; want %q", tc.ordinal, got, err, tc.want)
		}
	}
	for _, ordinal := range []int64{-1, 0, math.MinInt64} {
		if _, err := SyncSetAlias(ordinal); err == nil {
			t.Fatalf("accepted ordinal %d", ordinal)
		}
	}
	for _, ordinal := range []int64{255, 65535, math.MaxInt64} {
		alias, err := SyncSetAlias(ordinal)
		if err != nil || !ValidAlias(alias) {
			t.Fatalf("invalid boundary %d: %q %v", ordinal, alias, err)
		}
	}
	for _, token := range []string{"@{Amber Badger}", "@{amber BADGER}", "@{Amber Badger 2}", "@{Amber Badger 10}"} {
		if _, ok := ParseAliasToken(token); !ok {
			t.Fatalf("rejected token %q", token)
		}
	}
	for _, token := range []string{"@Amber Badger", "@{set/Amber Badger}", "@{Amber Badger 1}", "@{Amber Badger 02}", "@{Amber\nBadger}", "@{Amber Badger Third}", "@{Am{ber Badger}", "@{Amber Badger}x", "@{Amber Badger 1234567890123456}"} {
		if _, ok := ParseAliasToken(token); ok {
			t.Fatalf("accepted token %q", token)
		}
	}
}
