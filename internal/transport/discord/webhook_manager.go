package discord

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/transport"
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

	mu     sync.RWMutex
	hooks  map[string]managedWebhookCredential
	states map[string]WebhookStatus
	repair map[string]*sync.Mutex
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
		hooks:  make(map[string]managedWebhookCredential),
		states: make(map[string]WebhookStatus),
		repair: make(map[string]*sync.Mutex),
	}
}

// Prepare finds or creates exactly one bridge-owned webhook per configured
// channel and keeps its credential only in process memory. Re-running Prepare
// reuses the existing webhook instead of creating one per source user or restart.
func (m *managedWebhookClient) Prepare(ctx context.Context, channelIDs []string) error {
	if m == nil || m.api == nil {
		return errors.New("Discord webhook manager is not initialized")
	}
	m.mu.Lock()
	if m.hooks == nil {
		m.hooks = make(map[string]managedWebhookCredential)
	}
	if m.states == nil {
		m.states = make(map[string]WebhookStatus)
	}
	m.mu.Unlock()

	botUserID := ""
	if m.botUserID != nil {
		botUserID = strings.TrimSpace(m.botUserID())
	}

	for _, rawChannelID := range channelIDs {
		channelID := strings.TrimSpace(rawChannelID)
		if channelID == "" {
			return errors.New("Discord webhook channel is required")
		}
		credential, status, err := m.prepareChannel(ctx, channelID, botUserID)
		if err != nil {
			return err
		}
		m.mu.Lock()
		if status == WebhookStatusReady {
			m.hooks[channelID] = credential
		} else {
			delete(m.hooks, channelID)
		}
		m.states[channelID] = status
		m.mu.Unlock()
	}
	return nil
}

func (m *managedWebhookClient) prepareChannel(ctx context.Context, channelID, botUserID string) (managedWebhookCredential, WebhookStatus, error) {
	webhooks, err := m.api.ChannelWebhooks(channelID, discordgo.WithContext(ctx), discordgo.WithRetryOnRatelimit(true))
	if isDiscordForbidden(err) {
		return managedWebhookCredential{}, WebhookStatusMissingPermission, nil
	}
	if err != nil {
		return managedWebhookCredential{}, WebhookStatusUnavailable, classifyDiscordFailure(err)
	}
	var managed *discordgo.Webhook
	for _, webhook := range webhooks {
		if webhook == nil || webhook.Type != discordgo.WebhookTypeIncoming || webhook.Name != managedWebhookName || strings.TrimSpace(webhook.Token) == "" {
			continue
		}
		if botUserID != "" && (webhook.User == nil || strings.TrimSpace(webhook.User.ID) != botUserID) {
			continue
		}
		managed = webhook
		break
	}
	if managed == nil {
		managed, err = m.api.WebhookCreate(channelID, managedWebhookName, "", discordgo.WithContext(ctx), discordgo.WithRetryOnRatelimit(true))
		if isDiscordForbidden(err) {
			return managedWebhookCredential{}, WebhookStatusMissingPermission, nil
		}
		if err != nil {
			return managedWebhookCredential{}, WebhookStatusUnavailable, classifyDiscordFailure(err)
		}
	}
	if managed == nil || strings.TrimSpace(managed.ID) == "" || strings.TrimSpace(managed.Token) == "" {
		return managedWebhookCredential{}, WebhookStatusUnavailable, errors.New("managed Discord webhook credential is unavailable")
	}
	return managedWebhookCredential{id: strings.TrimSpace(managed.ID), token: strings.TrimSpace(managed.Token)}, WebhookStatusReady, nil
}

func (m *managedWebhookClient) Readiness(channelID string) WebhookStatus {
	if m == nil {
		return WebhookStatusUnavailable
	}
	m.mu.RLock()
	status, ok := m.states[strings.TrimSpace(channelID)]
	m.mu.RUnlock()
	if !ok {
		return WebhookStatusUnavailable
	}
	return status
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
		if err := m.repairChannel(ctx, channelID, managedWebhookCredential{}); err != nil {
			return "", err
		}
		credential, ok = m.credential(channelID)
		if !ok {
			return "", errors.New("managed Discord webhook is unavailable")
		}
	}
	return m.executeWithRepair(ctx, channelID, credential, message)
}

func (m *managedWebhookClient) executeWithRepair(ctx context.Context, channelID string, credential managedWebhookCredential, message WebhookMessage) (string, error) {
	created, err := m.executeOnce(ctx, credential, message)
	if !isManagedWebhookInvalid(err) {
		return created, err
	}
	m.invalidate(channelID, credential)
	if repairErr := m.repairChannel(ctx, channelID, credential); repairErr != nil {
		return "", repairErr
	}
	credential, ok := m.credential(channelID)
	if !ok {
		return "", errors.New("managed Discord webhook is unavailable")
	}
	return m.executeOnce(ctx, credential, message)
}

func (m *managedWebhookClient) executeOnce(ctx context.Context, credential managedWebhookCredential, message WebhookMessage) (string, error) {

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
		return "", classifyDiscordFailure(err)
	}
	if created == nil || strings.TrimSpace(created.ID) == "" {
		return "", errors.New("Discord webhook did not return a message id")
	}
	return strings.TrimSpace(created.ID), nil
}

func (m *managedWebhookClient) Edit(ctx context.Context, channelID, messageID, content string) error {
	credential, ok := m.credential(channelID)
	if !ok {
		if err := m.repairChannel(ctx, channelID, managedWebhookCredential{}); err != nil {
			return err
		}
		credential, ok = m.credential(channelID)
		if !ok {
			return errors.New("managed Discord webhook is unavailable")
		}
	}
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return errors.New("Discord message id is required")
	}

	err := m.editOnce(ctx, credential, messageID, content)
	if isDiscordUnknownMessage(err) {
		return nil
	}
	if !isManagedWebhookInvalid(err) {
		return err
	}
	m.invalidate(channelID, credential)
	if repairErr := m.repairChannel(ctx, channelID, credential); repairErr != nil {
		return repairErr
	}
	credential, ok = m.credential(channelID)
	if !ok {
		return errors.New("managed Discord webhook is unavailable")
	}
	err = m.editOnce(ctx, credential, messageID, content)
	if isDiscordUnknownMessage(err) {
		return nil
	}
	return err
}

func (m *managedWebhookClient) editOnce(ctx context.Context, credential managedWebhookCredential, messageID, content string) error {
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
	if err != nil {
		return classifyDiscordFailure(err)
	}
	return nil
}

func (m *managedWebhookClient) Delete(ctx context.Context, channelID, messageID string) error {
	credential, ok := m.credential(channelID)
	if !ok {
		if err := m.repairChannel(ctx, channelID, managedWebhookCredential{}); err != nil {
			return err
		}
		credential, ok = m.credential(channelID)
		if !ok {
			return errors.New("managed Discord webhook is unavailable")
		}
	}
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return errors.New("Discord message id is required")
	}

	err := m.deleteOnce(ctx, credential, messageID)
	if isDiscordUnknownMessage(err) {
		return nil
	}
	if !isManagedWebhookInvalid(err) {
		return err
	}
	m.invalidate(channelID, credential)
	if repairErr := m.repairChannel(ctx, channelID, credential); repairErr != nil {
		return repairErr
	}
	credential, ok = m.credential(channelID)
	if !ok {
		return errors.New("managed Discord webhook is unavailable")
	}
	err = m.deleteOnce(ctx, credential, messageID)
	if isDiscordUnknownMessage(err) {
		return nil
	}
	return err
}

func (m *managedWebhookClient) deleteOnce(ctx context.Context, credential managedWebhookCredential, messageID string) error {
	err := m.api.WebhookMessageDelete(
		credential.id,
		credential.token,
		messageID,
		discordgo.WithContext(ctx),
		discordgo.WithRetryOnRatelimit(true),
	)
	if err != nil {
		return classifyDiscordFailure(err)
	}
	return nil
}

func (m *managedWebhookClient) repairChannel(ctx context.Context, channelID string, invalid managedWebhookCredential) error {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return errors.New("Discord webhook channel is required")
	}
	m.mu.Lock()
	if m.repair == nil {
		m.repair = make(map[string]*sync.Mutex)
	}
	lock := m.repair[channelID]
	if lock == nil {
		lock = &sync.Mutex{}
		m.repair[channelID] = lock
	}
	m.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	if current, ok := m.credential(channelID); ok && (invalid.id == "" || current != invalid) {
		return nil
	}
	m.mu.Lock()
	m.states[channelID] = WebhookStatusUnavailable
	m.mu.Unlock()
	botUserID := ""
	if m.botUserID != nil {
		botUserID = strings.TrimSpace(m.botUserID())
	}
	credential, status, err := m.prepareChannel(ctx, channelID, botUserID)
	if err != nil {
		m.mu.Lock()
		m.states[channelID] = WebhookStatusUnavailable
		m.mu.Unlock()
		return err
	}
	m.mu.Lock()
	m.states[channelID] = status
	if status == WebhookStatusReady {
		m.hooks[channelID] = credential
	} else {
		delete(m.hooks, channelID)
	}
	m.mu.Unlock()
	if status == WebhookStatusMissingPermission {
		return transport.NewFailure(transport.FailurePermissionDenied, 0, errors.New("Discord webhook repair permission denied"))
	}
	if status != WebhookStatusReady {
		return transport.NewFailure(transport.FailureTransient, 0, errors.New("Discord webhook repair unavailable"))
	}
	return nil
}

func (m *managedWebhookClient) invalidate(channelID string, credential managedWebhookCredential) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.hooks[strings.TrimSpace(channelID)]
	if ok && current == credential {
		delete(m.hooks, strings.TrimSpace(channelID))
		m.states[strings.TrimSpace(channelID)] = WebhookStatusUnavailable
	}
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

func discordErrorCode(err error) int {
	var restErr *discordgo.RESTError
	if !errors.As(err, &restErr) || restErr == nil || restErr.Message == nil {
		return 0
	}
	return restErr.Message.Code
}

func isDiscordUnknownMessage(err error) bool {
	return discordErrorCode(err) == discordgo.ErrCodeUnknownMessage
}
func isDiscordUnknownWebhook(err error) bool {
	return discordErrorCode(err) == discordgo.ErrCodeUnknownWebhook
}
func isManagedWebhookInvalid(err error) bool {
	if err == nil {
		return false
	}
	if isDiscordUnknownWebhook(err) {
		return true
	}
	var restErr *discordgo.RESTError
	return errors.As(err, &restErr) && restErr != nil && restErr.Response != nil && restErr.Response.StatusCode == http.StatusUnauthorized
}

func isDiscordForbidden(err error) bool {
	if err == nil {
		return false
	}
	var restErr *discordgo.RESTError
	if !errors.As(err, &restErr) || restErr == nil || restErr.Response == nil {
		return false
	}
	return restErr.Response.StatusCode == http.StatusForbidden
}
