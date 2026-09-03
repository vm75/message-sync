package identity

import (
	"strings"
	"testing"
)

func TestUserIDStableAndOpaque(t *testing.T) {
	h, err := New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	a := h.UserID("15551234567@s.whatsapp.net")
	b := h.UserID("15551234567@s.whatsapp.net")
	if a != b {
		t.Fatalf("IDs differ: %q %q", a, b)
	}
	if a == "15551234567@s.whatsapp.net" {
		t.Fatal("raw identifier leaked")
	}
}

func TestSecretMinimumLength(t *testing.T) {
	if _, err := New([]byte("short")); err == nil {
		t.Fatal("expected error")
	}
}

func TestScopeTokenStableAndOpaque(t *testing.T) {
	h, err := New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	first := h.ScopeToken("123456789012345678")
	if first != h.ScopeToken("123456789012345678") {
		t.Fatal("scope token is not stable")
	}
	if first == h.UserID("123456789012345678") || !strings.HasPrefix(first, "s_") {
		t.Fatalf("scope token = %q, expected domain-separated s_ token", first)
	}
	if strings.Contains(first, "123456789012345678") {
		t.Fatal("scope token exposes the provider scope")
	}
}
