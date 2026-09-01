package discord

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/transport"
)

type fakeWebhookAPI struct {
	mu          sync.Mutex
	channels    map[string][]*discordgo.Webhook
	listErr     error
	createErr   error
	createCalls int
	execCalls   int
	execErr     error
	invalidID   string
	editErr     error
	deleteErr   error
	lastParams  *discordgo.WebhookParams
}

func (f *fakeWebhookAPI) ChannelWebhooks(channelID string, _ ...discordgo.RequestOption) ([]*discordgo.Webhook, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]*discordgo.Webhook(nil), f.channels[channelID]...), nil
}

func (f *fakeWebhookAPI) WebhookCreate(channelID, name, _ string, _ ...discordgo.RequestOption) (*discordgo.Webhook, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	if f.createErr != nil {
		return nil, f.createErr
	}
	id := "managed-" + channelID
	if f.createCalls > 1 {
		id += "-replacement"
	}
	webhook := &discordgo.Webhook{
		ID:        id,
		Type:      discordgo.WebhookTypeIncoming,
		ChannelID: channelID,
		User:      &discordgo.User{ID: "bridge-bot"},
		Name:      name,
		Token:     "transient-webhook-token",
	}
	f.channels[channelID] = append(f.channels[channelID], webhook)
	return webhook, nil
}

func (f *fakeWebhookAPI) WebhookExecute(webhookID string, _ string, _ bool, data *discordgo.WebhookParams, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execCalls++
	f.lastParams = data
	if f.execErr != nil && (f.invalidID == "" || f.invalidID == webhookID) {
		return nil, f.execErr
	}
	return &discordgo.Message{ID: "created-message"}, nil
}

func (f *fakeWebhookAPI) WebhookMessageEdit(_ string, _ string, messageID string, _ *discordgo.WebhookEdit, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.editErr != nil {
		return nil, f.editErr
	}
	return &discordgo.Message{ID: messageID}, nil
}

func (f *fakeWebhookAPI) WebhookMessageDelete(_ string, _ string, _ string, _ ...discordgo.RequestOption) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deleteErr
}

func discordRESTError(status, code int) error {
	return &discordgo.RESTError{
		Response: &http.Response{StatusCode: status},
		Message:  &discordgo.APIErrorMessage{Code: code},
	}
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

func TestManagedWebhookRepairsDeletedWebhookAndRetriesOnce(t *testing.T) {
	api := &fakeWebhookAPI{channels: map[string][]*discordgo.Webhook{}}
	manager := &managedWebhookClient{api: api, botUserID: func() string { return "bridge-bot" }, hooks: make(map[string]managedWebhookCredential), states: make(map[string]WebhookStatus), repair: make(map[string]*sync.Mutex)}
	if err := manager.Prepare(context.Background(), []string{testChannelID}); err != nil {
		t.Fatal(err)
	}
	old := api.channels[testChannelID][0]
	api.channels[testChannelID] = nil
	api.execErr = discordRESTError(http.StatusNotFound, discordgo.ErrCodeUnknownWebhook)
	api.invalidID = old.ID
	messageID, err := manager.Execute(context.Background(), testChannelID, WebhookMessage{Content: "body"})
	if err != nil || messageID != "created-message" {
		t.Fatalf("repaired execute = %q, %v", messageID, err)
	}
	api.execErr = nil
	if api.createCalls != 2 || api.execCalls != 2 {
		t.Fatalf("create calls=%d execute calls=%d", api.createCalls, api.execCalls)
	}
	if manager.IsManagedWebhook(testChannelID, old.ID) {
		t.Fatal("deleted webhook remained registered")
	}
	if manager.Readiness(testChannelID) != WebhookStatusReady {
		t.Fatalf("readiness=%q", manager.Readiness(testChannelID))
	}
}

func TestManagedWebhookRepairIsSingleFlightPerChannel(t *testing.T) {
	api := &fakeWebhookAPI{channels: map[string][]*discordgo.Webhook{}}
	manager := &managedWebhookClient{api: api, botUserID: func() string { return "bridge-bot" }, hooks: make(map[string]managedWebhookCredential), states: make(map[string]WebhookStatus), repair: make(map[string]*sync.Mutex)}
	if err := manager.Prepare(context.Background(), []string{testChannelID}); err != nil {
		t.Fatal(err)
	}
	api.channels[testChannelID] = nil
	api.execErr = discordRESTError(http.StatusNotFound, discordgo.ErrCodeUnknownWebhook)
	api.invalidID = "managed-" + testChannelID
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = manager.Execute(context.Background(), testChannelID, WebhookMessage{Content: "body"})
		}()
	}
	wg.Wait()
	if api.createCalls != 2 {
		t.Fatalf("repair created %d webhooks, want 1 replacement", api.createCalls-1)
	}
}

func TestManagedWebhookEditDeleteDistinguishUnknownMessageAndWebhook(t *testing.T) {
	api := &fakeWebhookAPI{channels: map[string][]*discordgo.Webhook{}}
	manager := &managedWebhookClient{api: api, botUserID: func() string { return "bridge-bot" }, hooks: make(map[string]managedWebhookCredential), states: make(map[string]WebhookStatus), repair: make(map[string]*sync.Mutex)}
	if err := manager.Prepare(context.Background(), []string{testChannelID}); err != nil {
		t.Fatal(err)
	}
	api.editErr = discordRESTError(http.StatusNotFound, discordgo.ErrCodeUnknownMessage)
	api.deleteErr = discordRESTError(http.StatusNotFound, discordgo.ErrCodeUnknownMessage)
	if err := manager.Edit(context.Background(), testChannelID, "missing-message", "edited"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Delete(context.Background(), testChannelID, "missing-message"); err != nil {
		t.Fatal(err)
	}
	api.editErr = discordRESTError(http.StatusNotFound, discordgo.ErrCodeUnknownWebhook)
	api.channels[testChannelID] = nil
	if err := manager.Edit(context.Background(), testChannelID, "message", "edited"); err == nil || transport.Classify(err).Class != transport.FailureDestinationMissing {
		t.Fatalf("unknown webhook edit error=%v", err)
	}
}
