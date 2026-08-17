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
