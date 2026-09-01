package discord

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/transport"
)

type recoveryDiscordAPI struct {
	pages    [][]*discordgo.Message
	afterIDs []string
	err      error
}

func (f *recoveryDiscordAPI) ChannelMessages(_ string, _ int, _, afterID, _ string, _ ...discordgo.RequestOption) ([]*discordgo.Message, error) {
	f.afterIDs = append(f.afterIDs, afterID)
	if f.err != nil {
		return nil, f.err
	}
	if len(f.pages) == 0 {
		return nil, nil
	}
	page := f.pages[0]
	f.pages = f.pages[1:]
	return page, nil
}

func (f *recoveryDiscordAPI) ChannelMessage(string, string, ...discordgo.RequestOption) (*discordgo.Message, error) {
	return nil, nil
}
func (f *recoveryDiscordAPI) ChannelMessageSendComplex(string, *discordgo.MessageSend, ...discordgo.RequestOption) (*discordgo.Message, error) {
	return nil, nil
}
func (f *recoveryDiscordAPI) MessageReactionAdd(string, string, string, ...discordgo.RequestOption) error {
	return nil
}
func (f *recoveryDiscordAPI) MessageReactionRemove(string, string, string, string, ...discordgo.RequestOption) error {
	return nil
}

func newRecoveryAdapter(t *testing.T, api discordAPI) *Adapter {
	t.Helper()
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	normalizer, err := NewNormalizer(map[string]string{"team-discord": testChannelID}, hasher, config.UsernameModeHash)
	if err != nil {
		t.Fatal(err)
	}
	return &Adapter{
		api: api, normalizer: normalizer,
		targets:       map[transport.EndpointID]string{"team-discord": testChannelID},
		historyStatus: make(map[string]HistoryStatus),
	}
}

func recoveryMessage(id, content string) *discordgo.Message {
	return &discordgo.Message{
		ID: id, ChannelID: testChannelID, GuildID: testGuildID, Content: content,
		Author:    &discordgo.User{ID: testAuthorID},
		Timestamp: time.Now().UTC().Add(-time.Minute).Add(time.Duration(id[len(id)-1]-'0') * time.Second),
	}
}

func TestDiscordRecoveryIsBoundedOldestFirstAndUsesCheckpoint(t *testing.T) {
	api := &recoveryDiscordAPI{pages: [][]*discordgo.Message{{
		recoveryMessage("423456789012345680", "new"),
		recoveryMessage("423456789012345679", "old"),
	}}}
	adapter := newRecoveryAdapter(t, api)
	var got []transport.Incoming
	err := adapter.Recover(context.Background(), transport.RecoveryRequest{
		Cursor:    transport.Checkpoint{StreamKey: "team-discord", Position: 423456789012345678},
		MaxEvents: 2, MaxAge: time.Hour,
	}, func(_ context.Context, incoming transport.Incoming) error {
		got = append(got, incoming)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Text != "old" || got[1].Text != "new" {
		t.Fatalf("recovered order = %#v, want old then new", got)
	}
	if len(api.afterIDs) != 1 || api.afterIDs[0] != "423456789012345678" {
		t.Fatalf("history cursor = %#v", api.afterIDs)
	}
	if got[0].Checkpoint.StreamKey != "team-discord" || got[1].Checkpoint.Position <= got[0].Checkpoint.Position {
		t.Fatalf("invalid recovery checkpoints: %#v", got)
	}
}

func TestDiscordRecoveryUsesEditNormalizerAndSuppressesFilteredMessages(t *testing.T) {
	edited := recoveryMessage("423456789012345681", "edited")
	edited.EditedTimestamp = &edited.Timestamp
	bot := recoveryMessage("423456789012345682", "bot")
	bot.Author = &discordgo.User{ID: "bot-user"}
	managed := recoveryMessage("423456789012345683", "managed")
	managed.WebhookID = "managed-hook"
	unsupported := recoveryMessage("423456789012345684", "unsupported")
	unsupported.Type = discordgo.MessageTypeGuildMemberJoin
	api := &recoveryDiscordAPI{pages: [][]*discordgo.Message{{edited, bot, managed, unsupported}}}
	adapter := newRecoveryAdapter(t, api)
	state := discordgo.NewState()
	state.User = &discordgo.User{ID: "bot-user"}
	adapter.session = &discordgo.Session{State: state}
	adapter.webhook = &fakeChannelWebhook{managed: map[string]string{testChannelID: "managed-hook"}}
	var got []transport.Incoming
	err := adapter.Recover(context.Background(), transport.RecoveryRequest{Cursor: transport.Checkpoint{StreamKey: "team-discord", Valid: true}, MaxEvents: 10}, func(_ context.Context, incoming transport.Incoming) error {
		got = append(got, incoming)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Kind != "edit" || got[0].Text != "edited" {
		t.Fatalf("filtered/edit recovery = %#v", got)
	}
}

func TestDiscordRecoveryReportsPermissionAndCancellationSafely(t *testing.T) {
	permissionErr := discordRESTError(403, 50013)
	adapter := newRecoveryAdapter(t, &recoveryDiscordAPI{err: permissionErr})
	err := adapter.Recover(context.Background(), transport.RecoveryRequest{Cursor: transport.Checkpoint{StreamKey: "team-discord", Valid: true}, MaxEvents: 1}, func(context.Context, transport.Incoming) error { return nil })
	if err == nil || transport.Classify(err).Class != transport.FailurePermissionDenied {
		t.Fatalf("permission error = %v", err)
	}
	if adapter.historyStatus["team-discord"] != HistoryStatusMissingPermission {
		t.Fatalf("history status = %q", adapter.historyStatus["team-discord"])
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := adapter.Recover(ctx, transport.RecoveryRequest{Cursor: transport.Checkpoint{StreamKey: "team-discord", Valid: true}, MaxEvents: 1}, func(context.Context, transport.Incoming) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled recovery error = %v", err)
	}
}

func TestDiscordRecoverySignalsReadyAndResume(t *testing.T) {
	adapter := &Adapter{recoverySignals: make(chan struct{}, 1)}
	adapter.handleReady(nil, &discordgo.Ready{})
	adapter.handleResumed(nil, &discordgo.Resumed{})
	select {
	case <-adapter.RecoverySignals():
	default:
		t.Fatal("ready/resume did not signal recovery")
	}
}
