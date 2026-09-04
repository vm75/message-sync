package verification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
)

type Mailer interface {
	Send(context.Context, string, string, string) error
}
type ResendMailer struct {
	APIKey string
	From   string
	Client *http.Client
}

func NewResendMailerFromEnv() *ResendMailer {
	key := strings.TrimSpace(os.Getenv("VERIFICATION_MAIL_API_KEY"))
	from := strings.TrimSpace(os.Getenv("VERIFICATION_MAIL_FROM"))
	if key == "" || from == "" {
		return nil
	}
	return &ResendMailer{APIKey: key, From: from, Client: http.DefaultClient}
}
func (m *ResendMailer) Send(ctx context.Context, recipient, label, challenge string) error {
	if m == nil || m.APIKey == "" || m.From == "" {
		return errors.New("mailer unavailable")
	}
	subject := "Verify your membership application"
	text := fmt.Sprintf("Your %s verification challenge is: %s", label, challenge)
	if strings.HasPrefix(challenge, "https://") || strings.HasPrefix(challenge, "http://") {
		subject = "Your approved membership join link"
		text = fmt.Sprintf("Your approved %s membership join link is: %s", label, challenge)
	}
	payload := map[string]any{"from": m.From, "to": []string{recipient}, "subject": subject, "text": text}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.resend.com/emails", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("mailer rejected request")
	}
	return nil
}
