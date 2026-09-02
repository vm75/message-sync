package discord

import (
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/transport"
)

func TestNormalizeDiscordLifecycleEvents(t *testing.T) {
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	normalizer, err := NewNormalizer(map[string]string{"discord": testChannelID}, hasher, config.UsernameModePushName)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("edit", func(t *testing.T) {
		message := testMessage().Message
		message.Content = "edited private body"
		incoming, ok := normalizer.NormalizeUpdate(message, "", nil)
		if !ok {
			t.Fatal("edit was not normalized")
		}
		if incoming.Kind != "edit" || incoming.Text != "edited private body" || incoming.ReplyTo == nil || incoming.ReplyTo.RemoteMessageID != message.ID {
			t.Fatalf("unexpected edit normalization: %#v", incoming)
		}
	})

	t.Run("delete", func(t *testing.T) {
		incoming, ok := normalizer.NormalizeDelete(&discordgo.MessageDelete{Message: &discordgo.Message{
			ID:        "923456789012345670",
			ChannelID: testChannelID,
			GuildID:   testGuildID,
		}})
		if !ok {
			t.Fatal("delete was not normalized")
		}
		if incoming.Kind != "delete" || incoming.ReplyTo == nil || incoming.ReplyTo.RemoteMessageID != "923456789012345670" {
			t.Fatalf("unexpected delete normalization: %#v", incoming)
		}
	})

	t.Run("delete payload without guild id", func(t *testing.T) {
		incoming, ok := normalizer.NormalizeDelete(&discordgo.MessageDelete{Message: &discordgo.Message{
			ID:        "923456789012345673",
			ChannelID: testChannelID,
		}})
		if !ok {
			t.Fatal("configured-channel delete without guild id was ignored")
		}
		if incoming.Kind != "delete" || incoming.ReplyTo == nil || incoming.ReplyTo.RemoteMessageID != "923456789012345673" {
			t.Fatalf("unexpected delete normalization: %#v", incoming)
		}
	})

	t.Run("bulk delete payload", func(t *testing.T) {
		adapter := &Adapter{
			normalizer: normalizer,
			events:     make(chan transport.Incoming, 2),
		}
		adapter.handleMessageDeleteBulk(nil, &discordgo.MessageDeleteBulk{
			ChannelID: testChannelID,
			Messages:  []string{"923456789012345674", "923456789012345675"},
		})
		for _, wantID := range []string{"923456789012345674", "923456789012345675"} {
			select {
			case incoming := <-adapter.events:
				if incoming.Kind != "delete" || incoming.ReplyTo == nil || incoming.ReplyTo.RemoteMessageID != wantID {
					t.Fatalf("unexpected bulk delete normalization: %#v", incoming)
				}
			default:
				t.Fatalf("bulk delete did not emit %s", wantID)
			}
		}
	})

	t.Run("reaction add and remove", func(t *testing.T) {
		reaction := &discordgo.MessageReaction{
			UserID:    testAuthorID,
			MessageID: "923456789012345671",
			ChannelID: testChannelID,
			GuildID:   testGuildID,
			Emoji:     discordgo.Emoji{Name: "👍"},
		}
		add, ok := normalizer.NormalizeReaction(reaction, "different-bot", false)
		if !ok {
			t.Fatal("reaction add was not normalized")
		}
		if add.Kind != "reaction" || add.Text != "👍" || add.Sender.OpaqueID == "" || add.Sender.OpaqueID == testAuthorID || add.FromSelf {
			t.Fatalf("unexpected reaction add: %#v", add)
		}
		remove, ok := normalizer.NormalizeReaction(reaction, "different-bot", true)
		if !ok {
			t.Fatal("reaction remove was not normalized")
		}
		if remove.Text != "" || remove.FromSelf {
			t.Fatalf("unexpected reaction remove: %#v", remove)
		}
		if _, ok := normalizer.NormalizeReaction(reaction, testAuthorID, false); ok {
			t.Fatal("bridge bot reaction echo was not filtered")
		}
		reaction.GuildID = ""
		if incoming, ok := normalizer.NormalizeReaction(reaction, "different-bot", false); !ok || incoming.Kind != "reaction" {
			t.Fatal("configured-channel reaction without guild id was ignored")
		}
	})
}

func TestBridgeInitiatedDiscordDeleteGatewayEchoIsIgnored(t *testing.T) {
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	normalizer, err := NewNormalizer(map[string]string{"discord": testChannelID}, hasher, config.UsernameModeHash)
	if err != nil {
		t.Fatal(err)
	}
	adapter := &Adapter{
		normalizer:        normalizer,
		events:            make(chan transport.Incoming, 1),
		suppressedDeletes: make(map[string]struct{}),
	}
	adapter.markSuppressedDelete(testChannelID, "923456789012345672")
	adapter.handleMessageDelete(nil, &discordgo.MessageDelete{Message: &discordgo.Message{
		ID:        "923456789012345672",
		ChannelID: testChannelID,
		GuildID:   testGuildID,
	}})

	select {
	case <-adapter.events:
		t.Fatal("bridge-initiated Discord delete re-entered canonical ingress")
	default:
	}
}

func TestDiscordLiveReactionAndDeleteDoNotReuseTargetAsCheckpoint(t *testing.T) {
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	normalizer, err := NewNormalizer(map[string]string{"discord": testChannelID}, hasher, config.UsernameModeHash)
	if err != nil {
		t.Fatal(err)
	}
	adapter := &Adapter{
		normalizer:        normalizer,
		events:            make(chan transport.Incoming, 3),
		suppressedDeletes: make(map[string]struct{}),
	}

	update := testMessage().Message
	update.Content = "edited body"
	adapter.handleMessageUpdate(nil, &discordgo.MessageUpdate{Message: update})
	adapter.handleMessageReactionAdd(nil, &discordgo.MessageReactionAdd{MessageReaction: &discordgo.MessageReaction{
		UserID: testAuthorID, MessageID: "923456789012345680", ChannelID: testChannelID,
		Emoji: discordgo.Emoji{Name: "👍"},
	}})
	adapter.handleMessageDelete(nil, &discordgo.MessageDelete{Message: &discordgo.Message{
		ID: "923456789012345681", ChannelID: testChannelID,
	}})

	for _, kind := range []string{"edit", "reaction", "delete"} {
		select {
		case incoming := <-adapter.events:
			if incoming.Kind != kind {
				t.Fatalf("event kind = %q, want %q", incoming.Kind, kind)
			}
			if incoming.Checkpoint.Valid {
				t.Fatalf("%s reused target message snowflake as a recovery checkpoint", kind)
			}
		default:
			t.Fatalf("missing %s event", kind)
		}
	}
}
