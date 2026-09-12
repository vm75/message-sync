package controlstore

import (
	"bytes"
	"context"
	"testing"
)

func TestConnectionSecretStoreEncryptedAndIsolated(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cipher, _ := NewCredentialCipher([]byte("01234567890123456789012345678901"))
	for _, id := range []string{"mt-a", "mt-b"} {
		enc, nonce, _ := cipher.Encrypt([]byte(`{"version":1}`))
		if err := s.CreateConnection(ctx, Connection{ID: id, Transport: "telegram", IntegrationMode: "mtproto", Label: id, Enabled: true, EncryptedCredential: enc, CredentialNonce: nonce, CredentialKeyVersion: 1}); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := NewConnectionSecretStore(s.DB(), cipher, "mt-a")
	b, _ := NewConnectionSecretStore(s.DB(), cipher, "mt-b")
	secret := []byte(`{"version":1,"apiHash":"secret-hash","phone":"+15551234567","session":"c2Vzc2lvbg=="}`)
	if err := a.Store(ctx, secret); err != nil {
		t.Fatal(err)
	}
	got, err := a.Load(ctx)
	if err != nil || !bytes.Equal(got, secret) {
		t.Fatalf("load=%q err=%v", got, err)
	}
	other, _ := b.Load(ctx)
	if bytes.Equal(other, secret) {
		t.Fatal("MTProto state leaked across connections")
	}
	var raw []byte
	if err := s.DB().QueryRow(`SELECT encrypted_credential FROM transport_connections WHERE id='mt-a'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("secret-hash")) || bytes.Contains(raw, []byte("+1555")) || bytes.Contains(raw, []byte("session")) {
		t.Fatal("plaintext auth/session material stored in control.db")
	}
}
