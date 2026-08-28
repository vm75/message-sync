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

type webhookAPI interface {
	ChannelWebhooks(channelID string, options ...discordgo.RequestOption) ([]*discordgo.Webhook, error)
	WebhookCreate(channelID, name, avatar string, options ...discordgo.RequestOption) (*discordgo.Webhook, error)
	WebhookExecute(webhookID, token string, wait bool, data *discordgo.WebhookParams, options ...discordgo.RequestOption) (*discordgo.Message, error)
	WebhookMessageEdit(webhookID, token, messageID string, data *discordgo.WebhookEdit, options ...discordgo.RequestOption) (*discordgo.Message, error)
	WebhookMessageDelete(webhookID, token, messageID string, options ...discordgo.RequestOption) error
}

type managedWebhookClient struct {
	api       webhookAPI
	botUserID func() string

	mu    sync.RWMutex
	hooks map[string]managedWebhookCredential
}

func newManagedWebhookClient(session *discordgo.Session) *managedWebhookClient {
	return &managedWebhookClient{
		api: session,
		botUserID: func() string {
			if session == nil || session.State == nil || session.State.User == nil {
				return ""
			}
			return strings.TrimSpace(session.State.User.ID)
		},
		hooks: make(map[string]managedWebhookCredential),
	}
}

// Prepare finds or creates exactly one bridge-owned webhook per configured
// channel and keeps its credential only in process memory. Re-running Prepare
// reuses the existing webhook instead of creating one per source user or restart.
func (m *managedWebhookClient) Prepare(ctx context.Context, channelIDs []string) error {
	if m == nil || m.api == nil {
		return errors.New("Discord webhook manager is not initialized")
	}

	botUserID := ""
	if m.botUserID != nil {
		botUserID = strings.TrimSpace(m.botUserID())
	}

	next := make(map[string]managedWebhookCredential, len(channelIDs))
	for _, rawChannelID := range channelIDs {
		channelID := strings.TrimSpace(rawChannelID)
		if channelID == "" {
			return errors.New("Discord webhook channel is required")
		}

		webhooks, err := m.api.ChannelWebhooks(
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
			managed, err = m.api.WebhookCreate(
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
		Content:         message.Content,
		Username:        message.Username,
		AllowedMentions: &discordgo.MessageAllowedMentions{},
	}
	if message.File != nil {
		params.Files = []*discordgo.File{{
			Name:        message.File.Name,
			ContentType: message.File.ContentType,
			Reader:      bytes.NewReader(message.File.Data),
		}}
	}

	created, err := m.api.WebhookExecute(
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

	_, err := m.api.WebhookMessageEdit(
		credential.id,
		credential.token,
		messageID,
		&discordgo.WebhookEdit{
			Content:         &content,
			AllowedMentions: &discordgo.MessageAllowedMentions{},
		},
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

	err := m.api.WebhookMessageDelete(
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
