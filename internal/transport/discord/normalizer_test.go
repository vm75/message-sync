package discord

import (
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
)

const (
	testChannelID = "123456789012345678"
	testGuildID   = "223456789012345678"
	testAuthorID  = "323456789012345678"
)

type testWebhookChecker map[string]struct{}

func (c testWebhookChecker) IsManagedWebhook(channelID, webhookID string) bool {
	_, ok := c[channelID+"\x00"+webhookID]
	return ok
}

func testNormalizer(t *testing.T, mode config.UsernameMode) *Normalizer {
	t.Helper()
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	normalizer, err := NewNormalizer(map[string]string{
		"team-discord": testChannelID,
	}, hasher, mode)
	if err != nil {
		t.Fatal(err)
	}
	return normalizer
}

func testMessage() *discordgo.MessageCreate {
	return &discordgo.MessageCreate{Message: &discordgo.Message{
		ID:        "423456789012345678",
		ChannelID: testChannelID,
		GuildID:   testGuildID,
		Content:   "private Discord message body",
		Author: &discordgo.User{
			ID:         testAuthorID,
			Username:   "alice-user",
			GlobalName: "Alice Example",
		},
		Member:    &discordgo.Member{Nick: "Alice Nick"},
		Timestamp: time.Unix(1_700_000_000, 0).UTC(),
	}}
}

func TestNormalizeConfiguredGuildMessage(t *testing.T) {
	normalizer := testNormalizer(t, config.UsernameModePushName)
	evt := testMessage()
	evt.Mentions = []*discordgo.User{{
		ID:         "523456789012345678",
		Username:   "bob-user",
		GlobalName: "Bob Example",
	}}
	evt.MessageReference = &discordgo.MessageReference{
		MessageID: "623456789012345678",
		ChannelID: testChannelID,
		GuildID:   testGuildID,
	}
	evt.ReferencedMessage = &discordgo.Message{Content: "quoted private body"}

	incoming, ok := normalizer.NormalizeMessage(evt, "723456789012345678", nil)
	if !ok {
		t.Fatal("configured Discord message was ignored")
	}
	if incoming.Endpoint != "team-discord" || incoming.RemoteID != evt.ID || incoming.Kind != "text" {
		t.Fatal("unexpected normalized Discord metadata")
	}
	if incoming.Text != evt.Content || incoming.Sender.DisplayName != "Alice Nick" {
		t.Fatal("transient Discord fields were not preserved")
	}
	if incoming.Sender.PhoneNumber != "" {
		t.Fatalf("Discord ingress unexpectedly populated phone number: %q", incoming.Sender.PhoneNumber)
	}
	if incoming.Sender.OpaqueID == "" || strings.Contains(incoming.Sender.OpaqueID, testAuthorID) {
		t.Fatalf("Discord user ID was not replaced by HMAC identity: %q", incoming.Sender.OpaqueID)
	}
	if incoming.ReplyTo == nil || incoming.ReplyTo.Endpoint != "team-discord" || incoming.ReplyTo.RemoteMessageID != evt.MessageReference.MessageID {
		t.Fatal("unexpected Discord reply mapping")
	}
	if incoming.QuotedText != "quoted private body" {
		t.Fatalf("quoted text = %q", incoming.QuotedText)
	}
	if len(incoming.Mentions) != 1 || incoming.Mentions[0].RemoteID != "523456789012345678" || incoming.Mentions[0].Name != "Bob Example" {
		t.Fatal("unexpected Discord mention normalization")
	}
	if !incoming.Timestamp.Equal(evt.Timestamp) {
		t.Fatalf("timestamp = %v, want %v", incoming.Timestamp, evt.Timestamp)
	}
}

func TestNormalizeHashModeDropsDiscordDisplayName(t *testing.T) {
	normalizer := testNormalizer(t, config.UsernameModeHash)
	incoming, ok := normalizer.NormalizeMessage(testMessage(), "", nil)
	if !ok {
		t.Fatal("configured Discord message was ignored")
	}
	if incoming.Sender.DisplayName != "" {
		t.Fatalf("hash mode retained Discord display name: %q", incoming.Sender.DisplayName)
	}
}

func TestNormalizeDiscordActorIDIsStableAndNamespaced(t *testing.T) {
	normalizer := testNormalizer(t, config.UsernameModeHash)
	first, ok := normalizer.NormalizeMessage(testMessage(), "", nil)
	if !ok {
		t.Fatal("first message was ignored")
	}
	secondEvent := testMessage()
	secondEvent.ID = "823456789012345678"
	second, ok := normalizer.NormalizeMessage(secondEvent, "", nil)
	if !ok {
		t.Fatal("second message was ignored")
	}
	if first.Sender.OpaqueID != second.Sender.OpaqueID {
		t.Fatalf("same Discord author produced unstable HMAC IDs: %q != %q", first.Sender.OpaqueID, second.Sender.OpaqueID)
	}

	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Sender.OpaqueID != hasher.UserID("discord:"+testAuthorID) {
		t.Fatalf("Discord actor HMAC was not transport-namespaced: %q", first.Sender.OpaqueID)
	}
}

func TestNormalizeFiltersDMUnconfiguredBotAndManagedWebhookMessages(t *testing.T) {
	normalizer := testNormalizer(t, config.UsernameModePushName)
	managedWebhookID := "923456789012345678"
	checker := testWebhookChecker{
		testChannelID + "\x00" + managedWebhookID: {},
	}

	tests := []struct {
		name      string
		mutate    func(*discordgo.MessageCreate)
		botUserID string
		webhooks  ManagedWebhookChecker
		want      bool
	}{
		{
			name: "dm",
			mutate: func(evt *discordgo.MessageCreate) {
				evt.GuildID = ""
			},
		},
		{
			name: "unconfigured channel",
			mutate: func(evt *discordgo.MessageCreate) {
				evt.ChannelID = "999999999999999999"
			},
		},
		{
			name: "missing author",
			mutate: func(evt *discordgo.MessageCreate) {
				evt.Author = nil
			},
		},
		{
			name:      "bridge bot",
			botUserID: testAuthorID,
		},
		{
			name: "managed webhook",
			mutate: func(evt *discordgo.MessageCreate) {
				evt.WebhookID = managedWebhookID
			},
			webhooks: checker,
		},
		{
			name: "external webhook remains ingress",
			mutate: func(evt *discordgo.MessageCreate) {
				evt.WebhookID = "103456789012345678"
			},
			webhooks: checker,
			want:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			evt := testMessage()
			if tc.mutate != nil {
				tc.mutate(evt)
			}
			_, ok := normalizer.NormalizeMessage(evt, tc.botUserID, tc.webhooks)
			if ok != tc.want {
				t.Fatalf("NormalizeMessage acceptance = %v, want %v", ok, tc.want)
			}
		})
	}
}

func TestReplyToUnconfiguredChannelIsNotExposed(t *testing.T) {
	normalizer := testNormalizer(t, config.UsernameModeHash)
	evt := testMessage()
	evt.MessageReference = &discordgo.MessageReference{
		MessageID: "113456789012345678",
		ChannelID: "999999999999999999",
		GuildID:   testGuildID,
	}
	evt.ReferencedMessage = &discordgo.Message{Content: "quoted secret"}

	incoming, ok := normalizer.NormalizeMessage(evt, "", nil)
	if !ok {
		t.Fatal("configured Discord message was ignored")
	}
	if incoming.ReplyTo != nil || incoming.QuotedText != "" {
		t.Fatal("unconfigured reply target leaked into normalized event")
	}
}
