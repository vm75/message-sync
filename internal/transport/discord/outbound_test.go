package discord

import (
	"context"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/transport"
)

type fakeChannelWebhook struct {
	managed  map[string]string
	nextID   int
	executed []WebhookMessage
	channels []string
	edits    []string
	deletes  []string
}

func (f *fakeChannelWebhook) IsManagedWebhook(channelID, webhookID string) bool {
	return f != nil && f.managed[channelID] == webhookID
}

func (f *fakeChannelWebhook) Execute(_ context.Context, channelID string, message WebhookMessage) (string, error) {
	f.nextID++
	f.channels = append(f.channels, channelID)
	f.executed = append(f.executed, message)
	return "discord-copy-" + string(rune('0'+f.nextID)), nil
}

func (f *fakeChannelWebhook) Edit(_ context.Context, channelID, messageID, content string) error {
	f.edits = append(f.edits, channelID+"|"+messageID+"|"+content)
	return nil
}

func (f *fakeChannelWebhook) Delete(_ context.Context, channelID, messageID string) error {
	f.deletes = append(f.deletes, channelID+"|"+messageID)
	return nil
}

type fakeDiscordAPI struct {
	replySends []*discordgo.MessageSend
	pollSends  []*discordgo.MessageSend
	adds       []string
	removes    []string
	message    *discordgo.Message
	channel    *discordgo.Channel
}

func (f *fakeDiscordAPI) ChannelMessages(_ string, _ int, _, _, _ string, _ ...discordgo.RequestOption) ([]*discordgo.Message, error) {
	return nil, nil
}

func (f *fakeDiscordAPI) ChannelMessage(_ string, _ string, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	return f.message, nil
}

func (f *fakeDiscordAPI) Channel(_ string, _ ...discordgo.RequestOption) (*discordgo.Channel, error) {
	return f.channel, nil
}

func (f *fakeDiscordAPI) ChannelMessageSendComplex(_ string, data *discordgo.MessageSend, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.replySends = append(f.replySends, data)
	if data.Poll != nil {
		f.pollSends = append(f.pollSends, data)
	}
	return &discordgo.Message{ID: "reply-marker"}, nil
}

func (f *fakeDiscordAPI) MessageReactionAdd(channelID, messageID, emojiID string, _ ...discordgo.RequestOption) error {
	f.adds = append(f.adds, channelID+"|"+messageID+"|"+emojiID)
	return nil
}

func (f *fakeDiscordAPI) MessageReactionRemove(channelID, messageID, emojiID, userID string, _ ...discordgo.RequestOption) error {
	f.removes = append(f.removes, channelID+"|"+messageID+"|"+emojiID+"|"+userID)
	return nil
}

func newOutboundTestAdapter(webhook *fakeChannelWebhook, api *fakeDiscordAPI) *Adapter {
	return &Adapter{
		webhook:           webhook,
		api:               api,
		targets:           map[transport.EndpointID]string{"discord": testChannelID},
		mediaEnabled:      true,
		mediaMaxBytes:     1024,
		reactionState:     make(map[reactionKey]string),
		suppressedDeletes: make(map[string]struct{}),
	}
}

func TestWebhookSenderRenderingUsesDistinctTransientNamesAndHashFallback(t *testing.T) {
	webhook := &fakeChannelWebhook{managed: make(map[string]string)}
	adapter := newOutboundTestAdapter(webhook, &fakeDiscordAPI{})

	cases := []struct {
		name     string
		sender   transport.Sender
		text     string
		wantUser string
	}{
		{name: "alice", sender: transport.Sender{DisplayName: "Alice Example", OpaqueID: "u_alicehash"}, text: "hello", wantUser: "Alice Example"},
		{name: "bob", sender: transport.Sender{DisplayName: "Bob Example", OpaqueID: "u_bobhash"}, text: "hi", wantUser: "Bob Example"},
		{name: "hash fallback", sender: transport.Sender{OpaqueID: "u_fallbackhash"}, text: "fallback", wantUser: "u_fallbackhash"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := adapter.Send(context.Background(), transport.Outgoing{
				Endpoint:   "discord",
				Sender:     tc.sender,
				SourceText: tc.text,
				Text:       "*_whatsapp/attribution_*: " + tc.text,
				Kind:       "text",
			})
			if err != nil {
				t.Fatal(err)
			}
			got := webhook.executed[len(webhook.executed)-1]
			if got.Username != tc.wantUser {
				t.Fatalf("webhook username = %q, want %q", got.Username, tc.wantUser)
			}
			if got.Content != tc.text {
				t.Fatalf("webhook content = %q, want raw source %q", got.Content, tc.text)
			}
			if strings.Contains(got.Content, tc.wantUser) {
				t.Fatalf("sender identity was duplicated into webhook content: %q", got.Content)
			}
		})
	}
}

func TestWebhookUsesCentralFriendlyRendering(t *testing.T) {
	webhook := &fakeChannelWebhook{managed: make(map[string]string)}
	adapter := newOutboundTestAdapter(webhook, &fakeDiscordAPI{})
	_, err := adapter.Send(context.Background(), transport.Outgoing{
		Endpoint: "discord", OriginEndpoint: "discord", Sender: transport.Sender{DisplayName: "Alice"},
		SourceText: "Dinner at 7?", Text: "Dinner at 7?", RenderedText: "*_family:Travel/Alice_*: Dinner at 7?", Kind: "text",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := webhook.executed[0].Content; got != "*_family:Travel/Alice_*: Dinner at 7?" {
		t.Fatalf("Discord adapter discarded friendly rendering: %q", got)
	}
}

func TestWebhookUsernameIncludesGroupPrefixForCrossEndpointMessages(t *testing.T) {
	webhook := &fakeChannelWebhook{managed: make(map[string]string)}
	adapter := newOutboundTestAdapter(webhook, &fakeDiscordAPI{})

	cases := []struct {
		name           string
		endpoint       transport.EndpointID
		originEndpoint transport.EndpointID
		sender         transport.Sender
		wantUser       string
	}{
		// Cross-endpoint: message from g1 forwarded to discord → prefix g1/
		{
			name:     "cross-endpoint display name",
			endpoint: "discord", originEndpoint: "g1",
			sender:   transport.Sender{DisplayName: "Alice", OpaqueID: "u_hash"},
			wantUser: "g1/Alice",
		},
		{
			name:     "cross-endpoint hash fallback",
			endpoint: "discord", originEndpoint: "g1",
			sender:   transport.Sender{OpaqueID: "u_hash"},
			wantUser: "g1/u_hash",
		},
		// Same-endpoint: Discord message echoed back → no prefix
		{
			name:     "same-endpoint no prefix",
			endpoint: "discord", originEndpoint: "discord",
			sender:   transport.Sender{DisplayName: "Bob", OpaqueID: "u_bobhash"},
			wantUser: "Bob",
		},
		// No originEndpoint set → no prefix
		{
			name:     "no origin no prefix",
			endpoint: "discord", originEndpoint: "",
			sender:   transport.Sender{DisplayName: "Carol", OpaqueID: "u_carolhash"},
			wantUser: "Carol",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := adapter.Send(context.Background(), transport.Outgoing{
				Endpoint:       tc.endpoint,
				OriginEndpoint: tc.originEndpoint,
				Sender:         tc.sender,
				SourceText:     "hello",
				Kind:           "text",
			})
			if err != nil {
				t.Fatal(err)
			}
			got := webhook.executed[len(webhook.executed)-1]
			if got.Username != tc.wantUser {
				t.Fatalf("webhook username = %q, want %q", got.Username, tc.wantUser)
			}
		})
	}
}

func TestDiscordSuppressesAttributionOnlyCompanion(t *testing.T) {
	webhook := &fakeChannelWebhook{managed: make(map[string]string)}
	adapter := newOutboundTestAdapter(webhook, &fakeDiscordAPI{})

	ref, err := adapter.Send(context.Background(), transport.Outgoing{
		Endpoint:        "discord",
		AttributionOnly: true,
		Kind:            "text",
		Text:            "private attribution",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref.Endpoint != "discord" || ref.RemoteMessageID != "" {
		t.Fatalf("unexpected companion ref: %#v", ref)
	}
	if len(webhook.executed) != 0 {
		t.Fatal("Discord emitted a redundant attribution companion")
	}
}

func TestDiscordMediaUsesTransientBytesAndSafeGeneratedFilename(t *testing.T) {
	webhook := &fakeChannelWebhook{managed: make(map[string]string)}
	adapter := newOutboundTestAdapter(webhook, &fakeDiscordAPI{})
	media := []byte("private-media-bytes")

	ref, err := adapter.Send(context.Background(), transport.Outgoing{
		Endpoint:   "discord",
		Sender:     transport.Sender{DisplayName: "Alice", OpaqueID: "u_hash"},
		SourceText: "caption",
		Text:       "*_wa/Alice_*: caption",
		Kind:       "document",
		MediaBytes: media,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref.RemoteMessageID == "" {
		t.Fatal("missing Discord remote copy id")
	}
	got := webhook.executed[len(webhook.executed)-1]
	if got.Username != "Alice" || got.Content != "caption" {
		t.Fatalf("unexpected webhook rendering: %#v", got)
	}
	if got.File == nil || got.File.Name != "document.bin" || string(got.File.Data) != string(media) {
		t.Fatalf("unexpected transient webhook file: %#v", got.File)
	}
	if strings.Contains(got.File.Name, "private") {
		t.Fatal("source filename/data-derived name leaked into outbound filename")
	}

	adapter.mediaMaxBytes = 4
	if _, err := adapter.Send(context.Background(), transport.Outgoing{
		Endpoint:   "discord",
		Sender:     transport.Sender{OpaqueID: "u_hash"},
		Kind:       "document",
		MediaBytes: media,
	}); err == nil {
		t.Fatal("expected configured media-size rejection")
	}
}

func TestDiscordReplyUsesSingleWebhookMessageWhenLinkUnavailable(t *testing.T) {
	webhook := &fakeChannelWebhook{managed: make(map[string]string)}
	api := &fakeDiscordAPI{}
	adapter := newOutboundTestAdapter(webhook, api)

	_, err := adapter.Send(context.Background(), transport.Outgoing{
		Endpoint:   "discord",
		Sender:     transport.Sender{DisplayName: "Alice"},
		SourceText: "reply body",
		Text:       "*_wa/Alice_*: reply body",
		Kind:       "text",
		ReplyTo: &transport.MessageRef{
			Endpoint:        "discord",
			RemoteMessageID: "known-discord-copy",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(api.replySends) != 0 {
		t.Fatal("single-message reply unexpectedly sent a separate native marker")
	}
	got := webhook.executed[len(webhook.executed)-1]
	if got.Username != "Alice" || !strings.Contains(got.Content, "reply to source") || !strings.Contains(got.Content, "reply body") {
		t.Fatalf("reply content lost webhook APP rendering: %#v", got)
	}
}

func TestDiscordReplyUsesTransientChannelLookupForClickableLink(t *testing.T) {
	webhook := &fakeChannelWebhook{managed: make(map[string]string)}
	api := &fakeDiscordAPI{channel: &discordgo.Channel{ID: testChannelID, GuildID: "guild"}}
	adapter := newOutboundTestAdapter(webhook, api)

	_, err := adapter.Send(context.Background(), transport.Outgoing{
		Endpoint:       "discord",
		OriginEndpoint: "g1",
		SourceText:     "reply body",
		Text:           "reply body",
		QuotedText:     "*_d1/vm_*: original message\nsecond line",
		Kind:           "text",
		ReplyTo: &transport.MessageRef{
			Endpoint:        "discord",
			RemoteMessageID: "known-discord-copy",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(webhook.executed) != 1 {
		t.Fatalf("webhook messages = %d, want 1", len(webhook.executed))
	}
	content := webhook.executed[0].Content
	if !strings.Contains(content, "[↪ reply to d1/vm: original message](https://discord.com/channels/guild/"+testChannelID+"/known-discord-copy)") {
		t.Fatalf("clickable reply link missing: %q", content)
	}
	if !strings.Contains(content, "reply body") {
		t.Fatalf("reply body missing: %q", content)
	}
}

func TestDiscordReplyLinkLabelUsesFirstQuotedLine(t *testing.T) {
	// Quoted message from d1 posted to destination "discord": d1 != "discord" → show "d1/vm".
	got := discordReplyLinkLabel("discord", "g1", "*_d1/vm_*: first line\nsecond line")
	if got != "↪ reply to d1/vm: first line" {
		t.Fatalf("reply link label (cross-endpoint) = %q", got)
	}
	// Quoted message from the same endpoint as destination: strip group prefix → show "vm".
	got = discordReplyLinkLabel("d1", "g1", "*_d1/vm_*: first line\nsecond line")
	if got != "↪ reply to vm: first line" {
		t.Fatalf("reply link label (same-endpoint) = %q", got)
	}
	// Unstructured quotedText: fall back to origin as sender label.
	got = discordReplyLinkLabel("discord", "wa-one", "raw quoted text")
	if got != "↪ reply to wa-one: raw quoted text" {
		t.Fatalf("reply link label (unstructured) = %q", got)
	}
}

func TestDiscordMessageLink(t *testing.T) {
	got := discordMessageLink("guild", "channel", "message")
	if got != "https://discord.com/channels/guild/channel/message" {
		t.Fatalf("message link = %q", got)
	}
	if discordMessageLink("", "channel", "message") != "" {
		t.Fatal("incomplete message link was not rejected")
	}
}

func TestDiscordReplyFallbackUsesAliasNotRemoteTarget(t *testing.T) {
	webhook := &fakeChannelWebhook{managed: make(map[string]string)}
	adapter := newOutboundTestAdapter(webhook, &fakeDiscordAPI{})

	_, err := adapter.Send(context.Background(), transport.Outgoing{
		Endpoint:       "discord",
		OriginEndpoint: "wa-source",
		Sender:         transport.Sender{DisplayName: "Alice"},
		SourceText:     "new body",
		Text:           "*_wa-source/Alice_*: new body",
		Kind:           "text",
		ReplyFallback:  true,
		QuotedText:     "quoted body",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := webhook.executed[len(webhook.executed)-1]
	if !strings.Contains(got.Content, "reply to wa-source") || !strings.Contains(got.Content, "quoted body") || !strings.Contains(got.Content, "new body") {
		t.Fatalf("reply fallback = %q", got.Content)
	}
	if len(adapter.api.(*fakeDiscordAPI).replySends) != 0 {
		t.Fatal("missing-copy fallback unexpectedly attempted a native reply")
	}
}

func TestDiscordEditDeleteAndReactionLifecycle(t *testing.T) {
	webhook := &fakeChannelWebhook{managed: make(map[string]string)}
	api := &fakeDiscordAPI{}
	adapter := newOutboundTestAdapter(webhook, api)
	ref := transport.MessageRef{Endpoint: "discord", RemoteMessageID: "discord-copy"}

	if err := adapter.Edit(context.Background(), ref, "*_wa/Alice_*: edited body"); err != nil {
		t.Fatal(err)
	}
	if len(webhook.edits) != 1 || !strings.HasSuffix(webhook.edits[0], "|edited body") {
		t.Fatalf("webhook edit = %#v", webhook.edits)
	}

	if err := adapter.React(context.Background(), transport.Reaction{Endpoint: "discord", TargetRemoteID: "discord-copy", Emoji: "👍"}); err != nil {
		t.Fatal(err)
	}
	if err := adapter.React(context.Background(), transport.Reaction{Endpoint: "discord", TargetRemoteID: "discord-copy", Emoji: "🎉"}); err != nil {
		t.Fatal(err)
	}
	if err := adapter.React(context.Background(), transport.Reaction{Endpoint: "discord", TargetRemoteID: "discord-copy", Emoji: ""}); err != nil {
		t.Fatal(err)
	}
	if len(api.adds) != 2 || len(api.removes) != 2 {
		t.Fatalf("reaction calls adds=%#v removes=%#v", api.adds, api.removes)
	}
	if !strings.Contains(api.removes[0], "👍") || !strings.Contains(api.removes[1], "🎉") {
		t.Fatalf("reaction change/remove did not remove prior emoji: %#v", api.removes)
	}

	if err := adapter.Delete(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if len(webhook.deletes) != 1 {
		t.Fatalf("webhook delete calls = %#v", webhook.deletes)
	}
	if !adapter.consumeSuppressedDelete(testChannelID, "discord-copy") {
		t.Fatal("bridge-initiated Discord delete was not suppression-tracked")
	}
}

func TestSanitizeWebhookUsernameCollapsesControlsAndLimitsLength(t *testing.T) {
	input := "  Alice\n\tExample  " + strings.Repeat("x", 100)
	got := sanitizeWebhookUsername(input)
	if strings.ContainsAny(got, "\n\t\r") {
		t.Fatalf("control whitespace remained in username %q", got)
	}
	if len([]rune(got)) > maxWebhookUsernameRunes {
		t.Fatalf("username exceeds %d runes: %d", maxWebhookUsernameRunes, len([]rune(got)))
	}
}

func TestDiscordPollUsesDeterministicTextRepresentation(t *testing.T) {
	webhook := &fakeChannelWebhook{managed: make(map[string]string)}
	adapter := newOutboundTestAdapter(webhook, &fakeDiscordAPI{})

	_, err := adapter.Send(context.Background(), transport.Outgoing{
		Endpoint:            "discord",
		Sender:              transport.Sender{DisplayName: "Alice", OpaqueID: "u_hash"},
		SourceText:          "Lunch?",
		Kind:                "poll",
		PollOptions:         []string{"Pizza", "Salad", "Tacos", "Soup", "Rice", "Bread", "Fruit", "Cake", "Tea", "Coffee", "Water"},
		PollSelectableCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := webhook.executed[len(webhook.executed)-1]
	want := "Poll: Lunch?\n1. Pizza\n2. Salad\n3. Tacos\n4. Soup\n5. Rice\n6. Bread\n7. Fruit\n8. Cake\n9. Tea\n10. Coffee\n11. Water\nChoose one option."
	if got.Content != want {
		t.Fatalf("poll fallback = %q, want %q", got.Content, want)
	}
	if got.Username != "Alice" {
		t.Fatalf("poll sender username = %q", got.Username)
	}
}

func TestDiscordRepresentablePollUsesNativeMessage(t *testing.T) {
	webhook := &fakeChannelWebhook{managed: make(map[string]string)}
	api := &fakeDiscordAPI{}
	adapter := newOutboundTestAdapter(webhook, api)
	ref, err := adapter.Send(context.Background(), transport.Outgoing{
		Endpoint: "discord", OriginEndpoint: "wa-family", Sender: transport.Sender{OpaqueID: "u_hash"}, SourceText: "Lunch?", Kind: "poll",
		PollAttribution: "*_wa-family:Travel/Alice_*:",
		PollOptions:     []string{"Pizza", "Salad"}, PollSelectableCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref.RemoteMessageID != "reply-marker" || len(api.pollSends) != 1 || len(webhook.executed) != 0 {
		t.Fatalf("native poll send = ref %#v, polls %d, webhook messages %d", ref, len(api.pollSends), len(webhook.executed))
	}
	if api.pollSends[0].Poll.Answers[0].Media.Text != "Pizza" || api.pollSends[0].Poll.AllowMultiselect {
		t.Fatalf("unexpected native poll payload: %#v", api.pollSends[0].Poll)
	}
	if api.pollSends[0].Content != "*_wa-family:Travel/Alice_*:" {
		t.Fatalf("native poll attribution = %q", api.pollSends[0].Content)
	}
}

func TestDiscordOutboundMentionsNeverExposeWhatsAppRemoteIdentity(t *testing.T) {
	webhook := &fakeChannelWebhook{managed: make(map[string]string)}
	adapter := newOutboundTestAdapter(webhook, &fakeDiscordAPI{})
	remoteID := "15551234567"

	_, err := adapter.Send(context.Background(), transport.Outgoing{
		Endpoint:   "discord",
		Sender:     transport.Sender{OpaqueID: "u_hash"},
		SourceText: "hello @" + remoteID,
		Kind:       "text",
		Mentions: []transport.Mention{{
			RemoteID: remoteID,
			Name:     remoteID,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := webhook.executed[len(webhook.executed)-1].Content
	if strings.Contains(got, remoteID) {
		t.Fatalf("WhatsApp remote identity leaked into Discord content: %q", got)
	}
	if !strings.Contains(got, "@participant") {
		t.Fatalf("safe mention fallback missing: %q", got)
	}
}

func TestDiscordStickerUsesWebPAttachmentFallback(t *testing.T) {
	webhook := &fakeChannelWebhook{managed: make(map[string]string)}
	adapter := newOutboundTestAdapter(webhook, &fakeDiscordAPI{})

	_, err := adapter.Send(context.Background(), transport.Outgoing{
		Endpoint:   "discord",
		Sender:     transport.Sender{DisplayName: "Alice", OpaqueID: "u_hash"},
		Kind:       "sticker",
		MediaBytes: []byte("private-webp"),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := webhook.executed[len(webhook.executed)-1]
	if got.File == nil || got.File.Name != "sticker.webp" || got.File.ContentType != "image/webp" {
		t.Fatalf("sticker fallback = %#v", got.File)
	}
	if string(got.File.Data) != "private-webp" {
		t.Fatal("sticker bytes were not forwarded transiently")
	}
	if got.Username != "Alice" {
		t.Fatalf("sticker sender username = %q", got.Username)
	}
}
