package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/transport"
)

func TestObservedChatDiscoveryIsBoundedAndGroupOnly(t *testing.T) {
	adapter := &Adapter{
		polling:  true,
		observed: make(map[int64]observedChatEntry),
	}
	adapter.observeChat(models.Chat{ID: 123, Type: models.ChatTypePrivate, FirstName: "Private"})
	for i := 0; i < observedChatLimit+1; i++ {
		adapter.observeChat(models.Chat{
			ID:       -1000000 - int64(i),
			Type:     models.ChatTypeSupergroup,
			Title:    fmt.Sprintf("Transient Group %03d", i),
			Username: fmt.Sprintf("transient_%03d", i),
		})
	}

	chats, err := adapter.DiscoverChats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != observedChatLimit {
		t.Fatalf("discovered chats=%d, want %d", len(chats), observedChatLimit)
	}
	for _, chat := range chats {
		if chat.ChatID == "123" {
			t.Fatal("private chat entered Telegram discovery cache")
		}
		if chat.Title == "Transient Group 000" {
			t.Fatal("oldest observed chat was not evicted from bounded cache")
		}
	}
}

func TestHandleUpdateObservesUnconfiguredGroupBeforeIngressFiltering(t *testing.T) {
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	normalizer, err := NewNormalizer(map[string]string{}, hasher, config.UsernameModeHash)
	if err != nil {
		t.Fatal(err)
	}
	adapter := &Adapter{
		normalizer: normalizer,
		events:     make(chan transport.Incoming, 1),
		botUserID:  testBotUserID,
		polling:    true,
		observed:   make(map[int64]observedChatEntry),
	}
	msg := testMessage(-1009876543210, models.ChatTypeSupergroup)
	msg.Chat.Title = "Observed Only In Memory"
	msg.Chat.Username = "observed_transient"
	adapter.handleUpdate(context.Background(), nil, &models.Update{ID: 1, Message: msg})

	select {
	case incoming := <-adapter.events:
		t.Fatalf("unconfigured Telegram chat emitted ingress: %#v", incoming)
	default:
	}
	chats, err := adapter.DiscoverChats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 1 || chats[0].ChatID != "-1009876543210" || chats[0].Title != "Observed Only In Memory" || chats[0].Username != "observed_transient" {
		t.Fatalf("unexpected transient discovery: %+v", chats)
	}
}

func TestTelegramAdminStatusExposesOnlySafeEndpointAliases(t *testing.T) {
	adapter := &Adapter{
		polling: true,
		normalizer: &Normalizer{endpoints: map[int64]transport.EndpointID{
			-1001111111111: "team-a",
			-1002222222222: "team-b",
		}},
	}
	status := adapter.AdminStatus(context.Background())
	if !status.TokenConfigured || !status.Running || status.Status != "running" {
		t.Fatalf("unexpected status: %+v", status)
	}
	if len(status.Endpoints) != 2 || status.Endpoints[0].Alias != "team-a" || status.Endpoints[0].Status != "ready" {
		t.Fatalf("unexpected endpoint readiness: %+v", status.Endpoints)
	}
	body, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"-1001111111111", "-1002222222222", "botToken"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("status exposed forbidden Telegram value %q: %s", forbidden, body)
		}
	}
}
