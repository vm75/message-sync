package discord

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"
)

const managedWebhookName = "message-sync bridge"

type managedWebhookCredential struct {
	id    string
	token string
}

type managedWebhookClient struct {
	session *discordgo.Session

	mu    sync.RWMutex
	hooks map[string]managedWebhookCredential
}

func newManagedWebhookClient(session *discordgo.Session) *managedWebhookClient {
	return &managedWebhookClient{
		session: session,
		hooks:   make(map[string]managedWebhookCredential),
	}
}

// Prepare finds or creates exactly one bridge-owned webhook per configured
// channel and keeps its credential only in process memory. Re-running Prepare
// reuses the existing webhook instead of creating one per source user or restart.
func (m *managedWebhookClient) Prepare(ctx context.Context, channelIDs []string) error {
	if m == nil || m.session == nil {
		return errors.New("Discord webhook manager is not initialized")
	}

	botUserID := ""
	if m.session.State != nil && m.session.State.User != nil {
		botUserID = strings.TrimSpace(m.session.State.User.ID)
	}

	next := make(map[string]managedWebhookCredential, len(channelIDs))
	for _, rawChannelID := range channelIDs {
		channelID := strings.TrimSpace(rawChannelID)
		if channelID == "" {
			return errors.New("Discord webhook channel is required")
		}

		webhooks, err := m.session.ChannelWebhooks(
			channelID,
			discordgo.WithContext(ctx),
			discordgo.WithRetryOnRatelimit(true),
		)
		if err != nil {
			return errors.New("list Discord channel webhooks")
		}

		var managed *discordgo.Webhook
		for _, webhook := range webhooks {
			if webhook == nil ||
				webhook.Type != discordgo.WebhookTypeIncoming ||
				webhook.Name != managedWebhookName ||
				strings.TrimSpace(webhook.Token) == "" {
				continue
			}
			if botUserID != "" && (webhook.User == nil || strings.TrimSpace(webhook.User.ID) != botUserID) {
				continue
			}
			managed = webhook
			break
		}

		if managed == nil {
			managed, err = m.session.WebhookCreate(
				channelID,
				managedWebhookName,
				"",
				discordgo.WithContext(ctx),
				discordgo.WithRetryOnRatelimit(true),
			)
			if err != nil {
				return errors.New("create managed Discord webhook")
			}
		}
		if managed == nil || strings.TrimSpace(managed.ID) == "" || strings.TrimSpace(managed.Token) == "" {
			return errors.New("managed Discord webhook credential is unavailable")
		}
		next[channelID] = managedWebhookCredential{
			id:    strings.TrimSpace(managed.ID),
			token: strings.TrimSpace(managed.Token),
		}
	}

	m.mu.Lock()
	m.hooks = next
	m.mu.Unlock()
	return nil
}

func (m *managedWebhookClient) IsManagedWebhook(channelID, webhookID string) bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	credential, ok := m.hooks[strings.TrimSpace(channelID)]
	m.mu.RUnlock()
	return ok && credential.id != "" && credential.id == strings.TrimSpace(webhookID)
}

func (m *managedWebhookClient) Execute(ctx context.Context, channelID string, message WebhookMessage) (string, error) {
	credential, ok := m.credential(channelID)
	if !ok {
		return "", errors.New("managed Discord webhook is unavailable")
	}

	params := &discordgo.WebhookParams{
		Content:  message.Content,
		Username: message.Username,
	}
	if message.File != nil {
		params.Files = []*discordgo.File{{
			Name:        message.File.Name,
			ContentType: message.File.ContentType,
			Reader:      bytes.NewReader(message.File.Data),
		}}
	}

	created, err := m.session.WebhookExecute(
		credential.id,
		credential.token,
		true,
		params,
		discordgo.WithContext(ctx),
		discordgo.WithRetryOnRatelimit(true),
	)
	if err != nil {
		return "", errors.New("execute managed Discord webhook")
	}
	if created == nil || strings.TrimSpace(created.ID) == "" {
		return "", errors.New("Discord webhook did not return a message id")
	}
	return strings.TrimSpace(created.ID), nil
}

func (m *managedWebhookClient) Edit(ctx context.Context, channelID, messageID, content string) error {
	credential, ok := m.credential(channelID)
	if !ok {
		return errors.New("managed Discord webhook is unavailable")
	}
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return errors.New("Discord message id is required")
	}

	_, err := m.session.WebhookMessageEdit(
		credential.id,
		credential.token,
		messageID,
		&discordgo.WebhookEdit{Content: &content},
		discordgo.WithContext(ctx),
		discordgo.WithRetryOnRatelimit(true),
	)
	if isDiscordNotFound(err) {
		return nil
	}
	if err != nil {
		return errors.New("edit managed Discord webhook message")
	}
	return nil
}

func (m *managedWebhookClient) Delete(ctx context.Context, channelID, messageID string) error {
	credential, ok := m.credential(channelID)
	if !ok {
		return errors.New("managed Discord webhook is unavailable")
	}
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return errors.New("Discord message id is required")
	}

	err := m.session.WebhookMessageDelete(
		credential.id,
		credential.token,
		messageID,
		discordgo.WithContext(ctx),
		discordgo.WithRetryOnRatelimit(true),
	)
	if isDiscordNotFound(err) {
		return nil
	}
	if err != nil {
		return errors.New("delete managed Discord webhook message")
	}
	return nil
}

func (m *managedWebhookClient) credential(channelID string) (managedWebhookCredential, bool) {
	if m == nil {
		return managedWebhookCredential{}, false
	}
	m.mu.RLock()
	credential, ok := m.hooks[strings.TrimSpace(channelID)]
	m.mu.RUnlock()
	return credential, ok
}

func isDiscordNotFound(err error) bool {
	if err == nil {
		return false
	}
	var restErr *discordgo.RESTError
	if !errors.As(err, &restErr) || restErr == nil || restErr.Response == nil {
		return false
	}
	return restErr.Response.StatusCode == http.StatusNotFound
}
