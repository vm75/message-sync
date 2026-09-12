package discord

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

const storedCredentialVersion = 1

type StoredCredential struct {
	Version    int    `json:"version"`
	BotToken   string `json:"botToken"`
	WebhookURL string `json:"webhookUrl,omitempty"`
	ChannelID  string `json:"channelId,omitempty"`
}

func EncodeStoredCredential(mode, botToken, webhookURL, channelID string) ([]byte, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	botToken = strings.TrimSpace(botToken)
	webhookURL = strings.TrimSpace(webhookURL)
	channelID = strings.TrimSpace(channelID)
	if botToken == "" {
		return nil, errors.New("Discord bot token is required")
	}
	if mode == "" || mode == "managed" {
		if webhookURL != "" || channelID != "" {
			return nil, errors.New("managed Discord webhook mode does not accept an explicit webhook URL or channel ID")
		}
		// Preserve the legacy on-disk representation for existing/default managed-webhook connections.
		return []byte(botToken), nil
	}
	if mode != "webhook" {
		return nil, errors.New("unsupported Discord integration mode")
	}
	if channelID == "" {
		return nil, errors.New("Discord explicit webhook mode requires a channel ID")
	}
	if _, _, err := ParseWebhookURL(webhookURL); err != nil {
		return nil, err
	}
	return json.Marshal(StoredCredential{
		Version:    storedCredentialVersion,
		BotToken:   botToken,
		WebhookURL: webhookURL,
		ChannelID:  channelID,
	})
}

func DecodeStoredCredential(mode string, raw []byte) (StoredCredential, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" || mode == "managed" {
		token := strings.TrimSpace(string(raw))
		if token == "" {
			return StoredCredential{}, errors.New("Discord bot token is required")
		}
		return StoredCredential{Version: storedCredentialVersion, BotToken: token}, nil
	}
	if mode != "webhook" {
		return StoredCredential{}, errors.New("unsupported Discord integration mode")
	}
	var credential StoredCredential
	if err := json.Unmarshal(raw, &credential); err != nil {
		return StoredCredential{}, errors.New("decode Discord webhook credential")
	}
	credential.BotToken = strings.TrimSpace(credential.BotToken)
	credential.WebhookURL = strings.TrimSpace(credential.WebhookURL)
	credential.ChannelID = strings.TrimSpace(credential.ChannelID)
	if credential.BotToken == "" || credential.ChannelID == "" {
		return StoredCredential{}, errors.New("Discord webhook credential is incomplete")
	}
	if _, _, err := ParseWebhookURL(credential.WebhookURL); err != nil {
		return StoredCredential{}, err
	}
	return credential, nil
}

func ParseWebhookURL(raw string) (string, string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil {
		return "", "", errors.New("invalid Discord webhook URL")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return "", "", errors.New("Discord webhook URL must use HTTPS")
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	switch host {
	case "discord.com", "www.discord.com", "discordapp.com", "www.discordapp.com", "canary.discord.com", "ptb.discord.com":
	default:
		return "", "", errors.New("Discord webhook URL must use an official Discord host")
	}
	parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	for i, part := range parts {
		if part != "webhooks" || i+2 >= len(parts) {
			continue
		}
		id, idErr := url.PathUnescape(parts[i+1])
		token, tokenErr := url.PathUnescape(parts[i+2])
		id = strings.TrimSpace(id)
		token = strings.TrimSpace(token)
		if idErr == nil && tokenErr == nil && id != "" && token != "" {
			return id, token, nil
		}
	}
	return "", "", errors.New("invalid Discord webhook URL")
}
