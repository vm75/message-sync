package telegram

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
)

const (
	testGroupID      int64 = -123456789
	testSupergroupID int64 = -1001234567890
	testSenderID     int64 = 323456789
	testBotUserID    int64 = 423456789
)

func testNormalizer(t *testing.T, mode config.UsernameMode) *Normalizer {
	t.Helper()
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	normalizer, err := NewNormalizer(map[string]string{
		"team-telegram":  strconv.FormatInt(testGroupID, 10),
		"super-telegram": strconv.FormatInt(testSupergroupID, 10),
	}, hasher, mode)
	if err != nil {
		t.Fatal(err)
	}
	return normalizer
}

func testMessage(chatID int64, chatType models.ChatType) *models.Message {
	return &models.Message{
		ID:   101,
		Date: 1_700_000_000,
		Chat: models.Chat{ID: chatID, Type: chatType, Title: "Private chat title"},
		From: &models.User{
			ID:        testSenderID,
			FirstName: "Alice",
			LastName:  "Example",
			Username:  "alice_private_username",
		},
		Text: "private Telegram message body",
	}
}

func TestNormalizeConfiguredGroupMessage(t *testing.T) {
	normalizer := testNormalizer(t, config.UsernameModePushName)
	msg := testMessage(testGroupID, models.ChatTypeGroup)
	msg.ReplyToMessage = &models.Message{
		ID:   77,
		Chat: models.Chat{ID: testGroupID, Type: models.ChatTypeGroup},
		Text: "quoted private Telegram body",
	}

	incoming, ok := normalizer.NormalizeMessage(msg, testBotUserID)
	if !ok {
		t.Fatal("configured Telegram group message was ignored")
	}
	if incoming.Endpoint != "team-telegram" || incoming.RemoteID != "101" || incoming.Kind != "text" {
		t.Fatalf("unexpected normalized Telegram metadata: %#v", incoming)
	}
	if incoming.Text != msg.Text || incoming.Sender.DisplayName != "Alice Example" {
		t.Fatal("transient Telegram content/display data was not preserved")
	}
	if incoming.Sender.PhoneNumber != "" {
		t.Fatalf("Telegram ingress unexpectedly populated phone number: %q", incoming.Sender.PhoneNumber)
	}
	if incoming.Sender.OpaqueID == "" || strings.Contains(incoming.Sender.OpaqueID, strconv.FormatInt(testSenderID, 10)) {
		t.Fatalf("Telegram user ID was not replaced by HMAC identity: %q", incoming.Sender.OpaqueID)
	}
	if incoming.ReplyTo == nil || incoming.ReplyTo.Endpoint != "team-telegram" || incoming.ReplyTo.RemoteMessageID != "77" {
		t.Fatalf("unexpected Telegram reply mapping: %#v", incoming.ReplyTo)
	}
	if incoming.QuotedText != "quoted private Telegram body" {
		t.Fatalf("quoted text = %q", incoming.QuotedText)
	}
	wantTime := time.Unix(1_700_000_000, 0).UTC()
	if !incoming.Timestamp.Equal(wantTime) {
		t.Fatalf("timestamp = %v, want %v", incoming.Timestamp, wantTime)
	}
}

func TestNormalizeConfiguredSupergroupCaption(t *testing.T) {
	normalizer := testNormalizer(t, config.UsernameModePushName)
	msg := testMessage(testSupergroupID, models.ChatTypeSupergroup)
	msg.Text = ""
	msg.Caption = "transient Telegram caption"

	incoming, ok := normalizer.NormalizeMessage(msg, testBotUserID)
	if !ok {
		t.Fatal("configured Telegram supergroup caption was ignored")
	}
	if incoming.Endpoint != "super-telegram" || incoming.Text != msg.Caption {
		t.Fatalf("unexpected supergroup normalization: %#v", incoming)
	}
}

func TestNormalizeHashModeDropsTelegramDisplayName(t *testing.T) {
	normalizer := testNormalizer(t, config.UsernameModeHash)
	incoming, ok := normalizer.NormalizeMessage(testMessage(testGroupID, models.ChatTypeGroup), testBotUserID)
	if !ok {
		t.Fatal("configured Telegram message was ignored")
	}
	if incoming.Sender.DisplayName != "" {
		t.Fatalf("hash mode retained Telegram display name: %q", incoming.Sender.DisplayName)
	}
}

func TestNormalizeTelegramActorIDIsStableAndNamespaced(t *testing.T) {
	normalizer := testNormalizer(t, config.UsernameModeHash)
	first, ok := normalizer.NormalizeMessage(testMessage(testGroupID, models.ChatTypeGroup), testBotUserID)
	if !ok {
		t.Fatal("first Telegram message was ignored")
	}
	secondMessage := testMessage(testGroupID, models.ChatTypeGroup)
	secondMessage.ID = 102
	second, ok := normalizer.NormalizeMessage(secondMessage, testBotUserID)
	if !ok {
		t.Fatal("second Telegram message was ignored")
	}
	if first.Sender.OpaqueID != second.Sender.OpaqueID {
		t.Fatalf("same Telegram sender produced unstable HMAC IDs: %q != %q", first.Sender.OpaqueID, second.Sender.OpaqueID)
	}

	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	want := hasher.UserID("telegram:" + strconv.FormatInt(testSenderID, 10))
	if first.Sender.OpaqueID != want {
		t.Fatalf("Telegram actor HMAC was not transport-namespaced: got %q want %q", first.Sender.OpaqueID, want)
	}
}

func TestNormalizeFiltersPrivateUnconfiguredBotAndUnsupportedMessages(t *testing.T) {
	normalizer := testNormalizer(t, config.UsernameModePushName)

	tests := []struct {
		name      string
		message   *models.Message
		botUserID int64
	}{
		{
			name:    "private chat",
			message: testMessage(123456789, models.ChatTypePrivate),
		},
		{
			name:    "channel",
			message: testMessage(testGroupID, models.ChatTypeChannel),
		},
		{
			name:    "unconfigured group",
			message: testMessage(-987654321, models.ChatTypeGroup),
		},
		{
			name: "missing sender",
			message: func() *models.Message {
				msg := testMessage(testGroupID, models.ChatTypeGroup)
				msg.From = nil
				return msg
			}(),
		},
		{
			name: "bridge bot",
			message: func() *models.Message {
				msg := testMessage(testGroupID, models.ChatTypeGroup)
				msg.From.ID = testBotUserID
				msg.From.IsBot = true
				return msg
			}(),
			botUserID: testBotUserID,
		},
		{
			name: "service message",
			message: func() *models.Message {
				msg := testMessage(testGroupID, models.ChatTypeGroup)
				msg.Text = ""
				msg.NewChatTitle = "private new chat title"
				return msg
			}(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := normalizer.NormalizeMessage(tc.message, tc.botUserID); ok {
				t.Fatal("Telegram message unexpectedly crossed the ingress boundary")
			}
		})
	}
}

func TestNormalizeTelegramTextMentionUsesHMACIdentity(t *testing.T) {
	normalizer := testNormalizer(t, config.UsernameModePushName)
	mentionedID := int64(523456789)
	msg := testMessage(testGroupID, models.ChatTypeGroup)
	msg.Text = "Hello Alice and @username_only"
	msg.Entities = []models.MessageEntity{
		{
			Type:   models.MessageEntityTypeTextMention,
			Offset: 6,
			Length: 5,
			User: &models.User{
				ID:        mentionedID,
				FirstName: "Bob",
				LastName:  "Example",
			},
		},
		{
			Type:   models.MessageEntityTypeMention,
			Offset: 16,
			Length: 14,
		},
	}

	incoming, ok := normalizer.NormalizeMessage(msg, testBotUserID)
	if !ok {
		t.Fatal("Telegram message with mention was ignored")
	}
	if len(incoming.Mentions) != 1 {
		t.Fatalf("structured mentions = %d, want 1", len(incoming.Mentions))
	}
	mention := incoming.Mentions[0]
	if mention.Name != "Bob Example" || mention.RemoteID == "" || !strings.HasPrefix(mention.RemoteID, "u_") {
		t.Fatalf("unexpected privacy-safe Telegram mention: %#v", mention)
	}
	if strings.Contains(mention.RemoteID, strconv.FormatInt(mentionedID, 10)) {
		t.Fatalf("raw Telegram mentioned-user ID escaped normalization: %#v", mention)
	}
	if !strings.Contains(incoming.Text, "@username_only") {
		t.Fatalf("username-only mention should remain transient text: %q", incoming.Text)
	}
}

func TestNewNormalizerRejectsDuplicateTelegramChat(t *testing.T) {
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewNormalizer(map[string]string{
		"one": strconv.FormatInt(testGroupID, 10),
		"two": strconv.FormatInt(testGroupID, 10),
	}, hasher, config.UsernameModeHash)
	if err == nil {
		t.Fatal("expected duplicate configured Telegram chat to be rejected")
	}
}
