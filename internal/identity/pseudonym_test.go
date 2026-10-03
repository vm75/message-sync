package identity

import (
	"strings"
	"testing"
	"unicode"
)

func TestPseudonymDeterministic(t *testing.T) {
	fixtures := []string{
		"u_x8vm2k9pa3",
		"u_abcdefghij",
		"u_1234567890",
		"u_memberone1",
	}

	for _, fixture := range fixtures {
		first := Pseudonym(fixture)
		for i := 0; i < 5; i++ {
			if got := Pseudonym(fixture); got != first {
				t.Fatalf("Pseudonym(%q) not deterministic: got %q, want %q", fixture, got, first)
			}
		}
	}
}

func TestPseudonymDistinctForDifferentInputs(t *testing.T) {
	seen := make(map[string]string)
	fixtures := []string{
		"u_alice00001",
		"u_bob0000002",
		"u_charlie003",
		"u_dave000004",
		"u_eve0000005",
	}

	for _, f := range fixtures {
		pseudo := Pseudonym(f)
		if prev, exists := seen[pseudo]; exists {
			t.Fatalf("Pseudonym collision between %q and %q: %q", prev, f, pseudo)
		}
		seen[pseudo] = f
	}
}

func TestPseudonymEmptyFallback(t *testing.T) {
	if got := Pseudonym(""); got != "Member" {
		t.Fatalf("Pseudonym(\"\") = %q, want %q", got, "Member")
	}
	if got := Pseudonym("   "); got != "Member" {
		t.Fatalf("Pseudonym(\"   \") = %q, want %q", got, "Member")
	}
}

func TestPseudonymNeverContainsOpaqueID(t *testing.T) {
	fixtures := []string{
		"u_alice12345",
		"15551234567",
		"alice@example.com",
		"secret_token_123",
	}

	for _, f := range fixtures {
		pseudo := Pseudonym(f)
		if strings.Contains(pseudo, f) {
			t.Fatalf("Pseudonym(%q) = %q leaked input substring", f, pseudo)
		}
		// Also check that parts of input are not leaked
		for _, part := range strings.Split(f, "_") {
			if len(part) >= 4 && strings.Contains(strings.ToLower(pseudo), strings.ToLower(part)) {
				t.Fatalf("Pseudonym(%q) = %q leaked input part %q", f, pseudo, part)
			}
		}
	}
}

func TestPseudonymLengthAndSafeCharacters(t *testing.T) {
	fixtures := []string{
		"u_short",
		"u_a_very_long_opaque_identifier_string_for_testing_1234567890",
		"u_test1",
		"u_test2",
	}

	for _, f := range fixtures {
		pseudo := Pseudonym(f)
		if len(pseudo) > 35 {
			t.Fatalf("Pseudonym(%q) length %d > 35: %q", f, len(pseudo), pseudo)
		}
		for _, r := range pseudo {
			if !unicode.IsPrint(r) || r < 0x20 || r > 0x7e {
				t.Fatalf("Pseudonym(%q) contains non-printable or non-ASCII character %q: %q", f, r, pseudo)
			}
		}
		parts := strings.Split(pseudo, " ")
		if len(parts) != 3 {
			t.Fatalf("Pseudonym(%q) expected 3 parts (Adjective Noun Suffix), got %q", f, pseudo)
		}
	}
}

func TestActorIDStableAndDomainSeparated(t *testing.T) {
	h, err := New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	mentionID1 := h.ActorID("mention", "15551234567@s.whatsapp.net")
	mentionID2 := h.ActorID("mention", "15551234567@s.whatsapp.net")
	if mentionID1 != mentionID2 {
		t.Fatalf("ActorID is not deterministic: %q vs %q", mentionID1, mentionID2)
	}
	if !strings.HasPrefix(mentionID1, "u_") {
		t.Fatalf("ActorID %q does not have u_ prefix", mentionID1)
	}
	if strings.Contains(mentionID1, "15551234567") {
		t.Fatalf("ActorID %q leaks raw input", mentionID1)
	}

	// Different domain produces different ID
	otherDomain := h.ActorID("user", "15551234567@s.whatsapp.net")
	if mentionID1 == otherDomain {
		t.Fatalf("ActorID failed domain separation: %q vs %q", mentionID1, otherDomain)
	}
}
