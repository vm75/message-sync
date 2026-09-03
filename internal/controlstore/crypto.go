package controlstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
)

const (
	CredentialKeyDomain = "message-sync-credential-encryption-v1"
	CurrentKeyVersion   = 1
)

var (
	ErrCredentialDecryptionFailed = errors.New("credential decryption failed")
	ErrInvalidSecret              = errors.New("IDENTITY_SECRET must be at least 32 bytes")
)

type CredentialCipher struct {
	key []byte
}

func DeriveCredentialKey(secret []byte) ([]byte, error) {
	if len(secret) < 32 {
		return nil, ErrInvalidSecret
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(CredentialKeyDomain))
	return mac.Sum(nil), nil
}

func NewCredentialCipher(secret []byte) (*CredentialCipher, error) {
	key, err := DeriveCredentialKey(secret)
	if err != nil {
		return nil, err
	}
	return &CredentialCipher{key: key}, nil
}

func (c *CredentialCipher) Encrypt(plaintext []byte) (ciphertext, nonce []byte, err error) {
	if c == nil || len(c.key) == 0 {
		return nil, nil, errors.New("cipher not initialized")
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, nil, fmt.Errorf("create block cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, fmt.Errorf("create gcm: %w", err)
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("generate nonce: %w", err)
	}
	ciphertext = gcm.Seal(nil, nonce, plaintext, nil)
	return ciphertext, nonce, nil
}

func (c *CredentialCipher) Decrypt(ciphertext, nonce []byte) ([]byte, error) {
	if c == nil || len(c.key) == 0 {
		return nil, ErrCredentialDecryptionFailed
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, ErrCredentialDecryptionFailed
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrCredentialDecryptionFailed
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, ErrCredentialDecryptionFailed
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, ErrCredentialDecryptionFailed
	}
	return plaintext, nil
}
