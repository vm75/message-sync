package discord

import "testing"

func TestStoredCredentialManagedAndExplicitWebhook(t *testing.T) {
	managed, err := EncodeStoredCredential("managed", "bot-secret", "", "")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeStoredCredential("managed", managed)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.BotToken != "bot-secret" || decoded.WebhookURL != "" || decoded.ChannelID != "" {
		t.Fatalf("unexpected managed credential: %#v", decoded)
	}

	webhookURL := "https://discord.com/api/webhooks/123456789/token-value"
	raw, err := EncodeStoredCredential("webhook", "bot-secret", webhookURL, "987654321")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err = DecodeStoredCredential("webhook", raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.BotToken != "bot-secret" || decoded.WebhookURL != webhookURL || decoded.ChannelID != "987654321" {
		t.Fatalf("unexpected webhook credential: %#v", decoded)
	}
}

func TestParseWebhookURLRejectsNonDiscordHost(t *testing.T) {
	if _, _, err := ParseWebhookURL("https://example.com/api/webhooks/123/token"); err == nil {
		t.Fatal("expected non-Discord webhook host to be rejected")
	}
}
