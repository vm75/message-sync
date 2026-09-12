package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/bwmarrin/discordgo"
)

type explicitWebhookClient struct {
	api       webhookAPI
	channelID string
	id        string
	token     string
}

func newExplicitWebhookClient(api webhookAPI, channelID, webhookURL string) (*explicitWebhookClient, error) {
	if api == nil {
		return nil, errors.New("Discord webhook API is required")
	}
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return nil, errors.New("Discord explicit webhook channel is required")
	}
	id, token, err := ParseWebhookURL(webhookURL)
	if err != nil {
		return nil, err
	}
	return &explicitWebhookClient{api: api, channelID: channelID, id: id, token: token}, nil
}

func (c *explicitWebhookClient) Prepare(_ context.Context, channelIDs []string) error {
	for _, channelID := range channelIDs {
		if strings.TrimSpace(channelID) != c.channelID {
			return errors.New("Discord explicit webhook connection is bound to one configured channel")
		}
	}
	return nil
}

func (c *explicitWebhookClient) Readiness(channelID string) WebhookStatus {
	if c == nil || strings.TrimSpace(channelID) != c.channelID {
		return WebhookStatusUnavailable
	}
	return WebhookStatusReady
}

func (c *explicitWebhookClient) IsManagedWebhook(channelID, webhookID string) bool {
	return c != nil && strings.TrimSpace(channelID) == c.channelID && strings.TrimSpace(webhookID) == c.id
}

func (c *explicitWebhookClient) Execute(ctx context.Context, channelID string, message WebhookMessage) (string, error) {
	if err := c.requireChannel(channelID); err != nil {
		return "", err
	}
	created, err := c.api.WebhookExecute(c.id, c.token, true, explicitWebhookParams(message), discordgo.WithContext(ctx), discordgo.WithRetryOnRatelimit(true))
	if err != nil {
		return "", classifyDiscordFailure(err)
	}
	if created == nil || strings.TrimSpace(created.ID) == "" {
		return "", errors.New("Discord explicit webhook did not return a message id")
	}
	return strings.TrimSpace(created.ID), nil
}

func (c *explicitWebhookClient) ExecuteInThread(ctx context.Context, channelID, threadID string, message WebhookMessage) (string, error) {
	if err := c.requireChannel(channelID); err != nil {
		return "", err
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return "", errors.New("Discord explicit webhook thread is required")
	}
	params := explicitWebhookParams(message)
	if executor, ok := c.api.(interface {
		WebhookThreadExecute(string, string, bool, string, *discordgo.WebhookParams, ...discordgo.RequestOption) (*discordgo.Message, error)
	}); ok {
		created, err := executor.WebhookThreadExecute(c.id, c.token, true, threadID, params, discordgo.WithContext(ctx), discordgo.WithRetryOnRatelimit(true))
		if err != nil {
			return "", classifyDiscordFailure(err)
		}
		if created == nil || strings.TrimSpace(created.ID) == "" {
			return "", errors.New("Discord threaded explicit webhook returned an incomplete message")
		}
		return strings.TrimSpace(created.ID), nil
	}
	requester, ok := c.api.(interface {
		RequestWithBucketID(string, string, interface{}, string, ...discordgo.RequestOption) ([]byte, error)
	})
	if !ok {
		return "", errors.New("Discord client does not support threaded webhook execution")
	}
	endpoint := discordgo.EndpointWebhookToken(c.id, c.token) + "?wait=true&thread_id=" + url.QueryEscape(threadID)
	response, err := requester.RequestWithBucketID(http.MethodPost, endpoint, params, endpoint, discordgo.WithContext(ctx), discordgo.WithRetryOnRatelimit(true))
	if err != nil {
		return "", classifyDiscordFailure(err)
	}
	var created discordgo.Message
	if err := json.Unmarshal(response, &created); err != nil || strings.TrimSpace(created.ID) == "" {
		return "", errors.New("Discord threaded explicit webhook returned an incomplete message")
	}
	return strings.TrimSpace(created.ID), nil
}

func (c *explicitWebhookClient) Edit(ctx context.Context, channelID, messageID, content string) error {
	if err := c.requireChannel(channelID); err != nil {
		return err
	}
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return errors.New("Discord message id is required")
	}
	_, err := c.api.WebhookMessageEdit(c.id, c.token, messageID, &discordgo.WebhookEdit{Content: &content, AllowedMentions: &discordgo.MessageAllowedMentions{}}, discordgo.WithContext(ctx), discordgo.WithRetryOnRatelimit(true))
	if isDiscordUnknownMessage(err) {
		return nil
	}
	if err != nil {
		return classifyDiscordFailure(err)
	}
	return nil
}

func (c *explicitWebhookClient) Delete(ctx context.Context, channelID, messageID string) error {
	if err := c.requireChannel(channelID); err != nil {
		return err
	}
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return errors.New("Discord message id is required")
	}
	err := c.api.WebhookMessageDelete(c.id, c.token, messageID, discordgo.WithContext(ctx), discordgo.WithRetryOnRatelimit(true))
	if isDiscordUnknownMessage(err) {
		return nil
	}
	if err != nil {
		return classifyDiscordFailure(err)
	}
	return nil
}

func (c *explicitWebhookClient) EditInThread(ctx context.Context, channelID, threadID, messageID, content string) error {
	return c.threadMessageRequest(ctx, http.MethodPatch, channelID, threadID, messageID, &discordgo.WebhookEdit{Content: &content, AllowedMentions: &discordgo.MessageAllowedMentions{}})
}

func (c *explicitWebhookClient) DeleteInThread(ctx context.Context, channelID, threadID, messageID string) error {
	return c.threadMessageRequest(ctx, http.MethodDelete, channelID, threadID, messageID, nil)
}

func (c *explicitWebhookClient) threadMessageRequest(ctx context.Context, method, channelID, threadID, messageID string, payload *discordgo.WebhookEdit) error {
	if err := c.requireChannel(channelID); err != nil {
		return err
	}
	threadID = strings.TrimSpace(threadID)
	messageID = strings.TrimSpace(messageID)
	if threadID == "" || messageID == "" {
		return errors.New("Discord explicit webhook thread message is required")
	}
	requester, ok := c.api.(interface {
		RequestWithBucketID(string, string, interface{}, string, ...discordgo.RequestOption) ([]byte, error)
	})
	if !ok {
		return errors.New("Discord client does not support threaded webhook lifecycle")
	}
	endpoint := discordgo.EndpointWebhookToken(c.id, c.token) + "/messages/" + url.PathEscape(messageID) + "?thread_id=" + url.QueryEscape(threadID)
	var body interface{}
	if payload != nil {
		body = payload
	}
	_, err := requester.RequestWithBucketID(method, endpoint, body, endpoint, discordgo.WithContext(ctx), discordgo.WithRetryOnRatelimit(true))
	if isDiscordUnknownMessage(err) {
		return nil
	}
	if err != nil {
		return classifyDiscordFailure(err)
	}
	return nil
}

func (c *explicitWebhookClient) requireChannel(channelID string) error {
	if c == nil || strings.TrimSpace(channelID) != c.channelID {
		return errors.New("Discord explicit webhook is not configured for this channel")
	}
	return nil
}

func explicitWebhookParams(message WebhookMessage) *discordgo.WebhookParams {
	params := &discordgo.WebhookParams{Content: message.Content, Username: message.Username, AllowedMentions: &discordgo.MessageAllowedMentions{}}
	if message.File != nil {
		params.Files = []*discordgo.File{{Name: message.File.Name, ContentType: message.File.ContentType, Reader: bytes.NewReader(message.File.Data)}}
	}
	return params
}
