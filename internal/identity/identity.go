package identity

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"strings"
)

type Hasher struct {
	secret []byte
}

func New(secret []byte) (*Hasher, error) {
	if len(secret) < 32 {
		return nil, errors.New("IDENTITY_SECRET must be at least 32 bytes")
	}
	copySecret := append([]byte(nil), secret...)
	return &Hasher{secret: copySecret}, nil
}

func (h *Hasher) UserID(normalizedJID string) string {
	mac := hmac.New(sha256.New, h.secret)
	_, _ = mac.Write([]byte(strings.TrimSpace(strings.ToLower(normalizedJID))))
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(mac.Sum(nil))
	return "u_" + strings.ToLower(encoded[:10])
}

// ScopeToken creates a short, stable presentation token without exposing the
// provider's child ID. The domain keeps scope tokens separate from actor IDs.
func (h *Hasher) ScopeToken(scopeID string) string {
	return h.derive("child-scope", scopeID, "s_")
}

func (h *Hasher) derive(domain, value, prefix string) string {
	mac := hmac.New(sha256.New, h.secret)
	_, _ = mac.Write([]byte(domain + "\x00" + strings.TrimSpace(value)))
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(mac.Sum(nil))
	return prefix + strings.ToLower(encoded[:10])
}
