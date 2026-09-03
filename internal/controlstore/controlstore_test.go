package controlstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenCreatesSensitiveSchemaAndRestrictsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var foreignKeys int
	if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}
	for _, table := range []string{"users", "sessions", "user_invites", "audit_events", "verification_pipelines", "membership_requests", "email_challenges", "verification_assessments", "transport_connections"} {
		var count int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("missing table %q", table)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("control.db permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestControlSchemaConstraintsAndNoRoutingSchemaMix(t *testing.T) {
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.db.Exec(`INSERT INTO users(id, username, password_hash, role, created_at, updated_at) VALUES ('u1', 'Admin', 'hash', 'admin', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO users(id, username, password_hash, role, created_at, updated_at) VALUES ('u2', 'admin', 'hash', 'operator', 1, 1)`); err == nil {
		t.Fatal("case-insensitive username uniqueness was not enforced")
	}
	if _, err := s.db.Exec(`INSERT INTO sessions(token_hash, user_id, expires_at, created_at, last_seen_at) VALUES ('token-hash', 'u1', 2, 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO sessions(token_hash, user_id, expires_at, created_at, last_seen_at) VALUES ('token-hash', 'u1', 2, 1, 1)`); err == nil {
		t.Fatal("session token hash uniqueness was not enforced")
	}
	var routingTables int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('canonical_messages', 'message_copies', 'endpoints')").Scan(&routingTables); err != nil {
		t.Fatal(err)
	}
	if routingTables != 0 {
		t.Fatal("control database contains routing tables")
	}
}

func TestPruneRetentionRemovesBoundedTerminalStateAndReturnsEvidence(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UnixMilli()
	if _, err := s.db.Exec(`INSERT INTO users(id,username,password_hash,role,created_at,updated_at) VALUES('u','operator','hash','operator',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO verification_pipelines(id,public_token,label,target_transport,endpoint_alias,creator_user_id,created_at,updated_at) VALUES('p','token','test','whatsapp','alias','u',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour).UnixMilli()
	if _, err := s.db.Exec(`INSERT INTO membership_requests(id,pipeline_id,status,applicant_work_email,verification_state,evidence_reference,created_at,updated_at) VALUES('r','p','rejected','private@example.test','failed','opaque-evidence',?,?)`, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO email_challenges(id,membership_request_id,token_hash,expires_at) VALUES('c','r','challenge-hash',?)`, old); err != nil {
		t.Fatal(err)
	}
	refs, err := s.PruneRetention(context.Background(), time.Now(), 24*time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0] != "opaque-evidence" {
		t.Fatalf("evidence refs = %v", refs)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM membership_requests`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("terminal request count = %d, want 0", count)
	}
}

func TestCredentialCipherAndKeyDerivation(t *testing.T) {
	_, err := DeriveCredentialKey([]byte("too-short"))
	if err == nil || !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("expected ErrInvalidSecret for short secret, got: %v", err)
	}

	secret := []byte("0123456789abcdef0123456789abcdef")
	cipher, err := NewCredentialCipher(secret)
	if err != nil {
		t.Fatal(err)
	}

	plaintext := []byte("bot_token_secret_12345")
	ciphertext, nonce, err := cipher.Encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(ciphertext, plaintext) {
		t.Fatal("ciphertext must not equal plaintext")
	}

	// Successful decryption
	decrypted, err := cipher.Decrypt(ciphertext, nonce)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decrypted = %q, want %q", decrypted, plaintext)
	}

	// Tampered ciphertext returns safe fixed error
	tamperedCiphertext := append([]byte(nil), ciphertext...)
	tamperedCiphertext[0] ^= 0xff
	if _, err := cipher.Decrypt(tamperedCiphertext, nonce); !errors.Is(err, ErrCredentialDecryptionFailed) {
		t.Fatalf("expected ErrCredentialDecryptionFailed for tampered ciphertext, got: %v", err)
	}

	// Tampered nonce returns safe fixed error
	tamperedNonce := append([]byte(nil), nonce...)
	tamperedNonce[0] ^= 0xff
	if _, err := cipher.Decrypt(ciphertext, tamperedNonce); !errors.Is(err, ErrCredentialDecryptionFailed) {
		t.Fatalf("expected ErrCredentialDecryptionFailed for tampered nonce, got: %v", err)
	}

	// Wrong secret returns safe fixed error
	otherSecret := []byte("fedcba9876543210fedcba9876543210")
	otherCipher, err := NewCredentialCipher(otherSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := otherCipher.Decrypt(ciphertext, nonce); !errors.Is(err, ErrCredentialDecryptionFailed) {
		t.Fatalf("expected ErrCredentialDecryptionFailed for different secret, got: %v", err)
	}
}

func TestTransportConnectionConstraintsAndCRUD(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "control.db")
	s, err := Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	secret := []byte("0123456789abcdef0123456789abcdef")
	cipher, err := NewCredentialCipher(secret)
	if err != nil {
		t.Fatal(err)
	}

	rawToken := "discord-bot-secret-token"
	encCred, nonce, err := cipher.Encrypt([]byte(rawToken))
	if err != nil {
		t.Fatal(err)
	}

	// WhatsApp connection must not store credentials
	waConn := Connection{
		ID:                  "conn-wa-1",
		Transport:           "whatsapp",
		Label:               "WhatsApp Main",
		Enabled:             true,
		EncryptedCredential: encCred,
		CredentialNonce:     nonce,
	}
	if err := s.CreateConnection(ctx, waConn); err == nil {
		t.Fatal("expected error creating WhatsApp connection with credentials")
	}

	// Raw SQLite check constraint prevents WhatsApp credentials
	if _, err := s.db.Exec(`INSERT INTO transport_connections (id, transport, label, enabled, encrypted_credential, credential_nonce, created_at, updated_at) VALUES ('wa-raw', 'whatsapp', 'wa', 1, X'1234', X'5678', 1, 1)`); err == nil {
		t.Fatal("expected DB CHECK constraint violation inserting WhatsApp credentials")
	}

	// WhatsApp connection without credentials succeeds
	waValid := Connection{
		ID:        "conn-wa-1",
		Transport: "whatsapp",
		Label:     "WhatsApp Main",
		Enabled:   true,
	}
	if err := s.CreateConnection(ctx, waValid); err != nil {
		t.Fatalf("create valid WhatsApp connection failed: %v", err)
	}

	// Discord connection requires credentials
	dcInvalid := Connection{
		ID:        "conn-dc-1",
		Transport: "discord",
		Label:     "Discord Bot",
		Enabled:   true,
	}
	if err := s.CreateConnection(ctx, dcInvalid); err == nil {
		t.Fatal("expected error creating Discord connection without credentials")
	}

	// Discord connection with credentials succeeds
	dcValid := Connection{
		ID:                  "conn-dc-1",
		Transport:           "discord",
		Label:               "Discord Bot",
		Enabled:             true,
		EncryptedCredential: encCred,
		CredentialNonce:     nonce,
	}
	if err := s.CreateConnection(ctx, dcValid); err != nil {
		t.Fatalf("create valid Discord connection failed: %v", err)
	}

	// Telegram connection with credentials succeeds
	tgToken := "telegram-bot-secret-token"
	tgEncCred, tgNonce, err := cipher.Encrypt([]byte(tgToken))
	if err != nil {
		t.Fatal(err)
	}
	tgValid := Connection{
		ID:                  "conn-tg-1",
		Transport:           "telegram",
		Label:               "Telegram Bot",
		Enabled:             true,
		EncryptedCredential: tgEncCred,
		CredentialNonce:     tgNonce,
	}
	if err := s.CreateConnection(ctx, tgValid); err != nil {
		t.Fatalf("create valid Telegram connection failed: %v", err)
	}

	// Verify GetConnection and Decryption
	retrieved, err := s.GetConnection(ctx, "conn-dc-1")
	if err != nil {
		t.Fatal(err)
	}
	if retrieved.ID != "conn-dc-1" || retrieved.Transport != "discord" || retrieved.Label != "Discord Bot" || !retrieved.Enabled {
		t.Fatalf("unexpected connection details: %+v", retrieved)
	}
	decryptedToken, err := cipher.Decrypt(retrieved.EncryptedCredential, retrieved.CredentialNonce)
	if err != nil {
		t.Fatal(err)
	}
	if string(decryptedToken) != rawToken {
		t.Fatalf("decrypted = %q, want %q", string(decryptedToken), rawToken)
	}

	// Verify ListConnections
	all, err := s.ListConnections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("ListConnections returned %d conns, want 3", len(all))
	}

	// Verify JSON marshaling does NOT expose credentials
	jsonBytes, err := json.Marshal(retrieved)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(jsonBytes), rawToken) || strings.Contains(string(jsonBytes), "encryptedCredential") || strings.Contains(string(jsonBytes), "credentialNonce") {
		t.Fatalf("JSON marshaling exposed credentials: %s", string(jsonBytes))
	}

	// Plaintext secret tokens are absent from the SQLite database file bytes
	s.Close()
	fileBytes, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(fileBytes, []byte(rawToken)) {
		t.Fatal("plaintext Discord bot token found in control.db file!")
	}
	if bytes.Contains(fileBytes, []byte(tgToken)) {
		t.Fatal("plaintext Telegram bot token found in control.db file!")
	}
}
