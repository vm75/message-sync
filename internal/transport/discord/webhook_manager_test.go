package discord

import (
	"context"
	"net/http"
	"testing"

	"github.com/bwmarrin/discordgo"
)

type fakeWebhookAPI struct {
	channels    map[string][]*discordgo.Webhook
	listErr     error
	createErr   error
	createCalls int
	execCalls   int
	lastParams  *discordgo.WebhookParams
}

func (f *fakeWebhookAPI) ChannelWebhooks(channelID string, _ ...discordgo.RequestOption) ([]*discordgo.Webhook, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.channels[channelID], nil
}

func (f *fakeWebhookAPI) WebhookCreate(channelID, name, _ string, _ ...discordgo.RequestOption) (*discordgo.Webhook, error) {
	f.createCalls++
	if f.createErr != nil {
		return nil, f.createErr
	}
	webhook := &discordgo.Webhook{
		ID:        "managed-" + channelID,
		Type:      discordgo.WebhookTypeIncoming,
		ChannelID: channelID,
		User:      &discordgo.User{ID: "bridge-bot"},
		Name:      name,
		Token:     "transient-webhook-token",
	}
	f.channels[channelID] = append(f.channels[channelID], webhook)
	return webhook, nil
}

func (f *fakeWebhookAPI) WebhookExecute(_ string, _ string, _ bool, data *discordgo.WebhookParams, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.execCalls++
	f.lastParams = data
	return &discordgo.Message{ID: "created-message"}, nil
}

func (f *fakeWebhookAPI) WebhookMessageEdit(_ string, _ string, messageID string, _ *discordgo.WebhookEdit, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	return &discordgo.Message{ID: messageID}, nil
}

func (f *fakeWebhookAPI) WebhookMessageDelete(_ string, _ string, _ string, _ ...discordgo.RequestOption) error {
	return nil
}

func TestManagedWebhookPrepareReusesExistingBridgeWebhook(t *testing.T) {
	api := &fakeWebhookAPI{channels: map[string][]*discordgo.Webhook{
		testChannelID: {{
			ID:        "existing-managed-webhook",
			Type:      discordgo.WebhookTypeIncoming,
			ChannelID: testChannelID,
			User:      &discordgo.User{ID: "bridge-bot"},
			Name:      managedWebhookName,
			Token:     "existing-transient-token",
		}},
	}}
	manager := &managedWebhookClient{
		api:       api,
		botUserID: func() string { return "bridge-bot" },
		hooks:     make(map[string]managedWebhookCredential),
	}

	if err := manager.Prepare(context.Background(), []string{testChannelID}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Prepare(context.Background(), []string{testChannelID}); err != nil {
		t.Fatal(err)
	}
	if api.createCalls != 0 {
		t.Fatalf("existing managed webhook was duplicated: create calls=%d", api.createCalls)
	}
	if !manager.IsManagedWebhook(testChannelID, "existing-managed-webhook") {
		t.Fatal("existing managed webhook was not registered for loop prevention")
	}
}

func TestManagedWebhookPrepareCreatesOneAndReusesOnReconnect(t *testing.T) {
	api := &fakeWebhookAPI{channels: make(map[string][]*discordgo.Webhook)}
	manager := &managedWebhookClient{
		api:       api,
		botUserID: func() string { return "bridge-bot" },
		hooks:     make(map[string]managedWebhookCredential),
	}

	for i := 0; i < 2; i++ {
		if err := manager.Prepare(context.Background(), []string{testChannelID}); err != nil {
			t.Fatal(err)
		}
	}
	if api.createCalls != 1 {
		t.Fatalf("managed webhook create calls=%d, want 1", api.createCalls)
	}
	if !manager.IsManagedWebhook(testChannelID, "managed-"+testChannelID) {
		t.Fatal("created managed webhook was not registered")
	}

	messageID, err := manager.Execute(context.Background(), testChannelID, WebhookMessage{
		Username: "Alice",
		Content:  "private body",
		File: &WebhookFile{
			Name:        "document.bin",
			ContentType: "application/octet-stream",
			Data:        []byte("private media"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if messageID != "created-message" || api.execCalls != 1 {
		t.Fatalf("execute result id=%q calls=%d", messageID, api.execCalls)
	}
	if api.lastParams == nil || api.lastParams.Username != "Alice" || api.lastParams.Content != "private body" || len(api.lastParams.Files) != 1 {
		t.Fatalf("unexpected webhook params: %#v", api.lastParams)
	}
	if api.lastParams.AllowedMentions == nil || len(api.lastParams.AllowedMentions.Parse) != 0 {
		t.Fatalf("bridged content must not enable implicit Discord mentions: %#v", api.lastParams.AllowedMentions)
	}
}

func TestManagedWebhookPrepareReportsMissingPermissionWithoutLeakingCredentials(t *testing.T) {
	tests := []struct {
		name      string
		listErr   error
		createErr error
	}{
		{
			name: "list forbidden",
			listErr: &discordgo.RESTError{
				Response: &http.Response{StatusCode: http.StatusForbidden},
			},
		},
		{
			name: "create forbidden",
			createErr: &discordgo.RESTError{
				Response: &http.Response{StatusCode: http.StatusForbidden},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeWebhookAPI{
				channels:  map[string][]*discordgo.Webhook{},
				listErr:   tc.listErr,
				createErr: tc.createErr,
			}
			manager := &managedWebhookClient{
				api:       api,
				botUserID: func() string { return "bridge-bot" },
				hooks:     make(map[string]managedWebhookCredential),
				states:    make(map[string]WebhookStatus),
			}

			if err := manager.Prepare(context.Background(), []string{testChannelID}); err != nil {
				t.Fatalf("missing Manage Webhooks permission should be a readiness state, got: %v", err)
			}
			if got := manager.Readiness(testChannelID); got != WebhookStatusMissingPermission {
				t.Fatalf("readiness=%q, want %q", got, WebhookStatusMissingPermission)
			}
			if _, ok := manager.credential(testChannelID); ok {
				t.Fatal("missing-permission channel unexpectedly retained a webhook credential")
			}
		})
	}
}
