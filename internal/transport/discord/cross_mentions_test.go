package discord

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/transport"
)

type mentionRoundTripper func(*http.Request) (*http.Response, error)

func (f mentionRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type crossMentionWebhook struct{ *fakeChannelWebhook }

func (f crossMentionWebhook) ExecuteInThread(ctx context.Context, channelID, _ string, message WebhookMessage) (string, error) {
	return f.Execute(ctx, channelID, message)
}

type crossMentionThreadRecoveryAPI struct {
	*recoveryDiscordAPI
}

func (f *crossMentionThreadRecoveryAPI) ThreadsActive(string, ...discordgo.RequestOption) (*discordgo.ThreadsList, error) {
	return &discordgo.ThreadsList{Threads: []*discordgo.Channel{{ID: "history-thread", ParentID: testChannelID}}}, nil
}

func (f *crossMentionThreadRecoveryAPI) ThreadsArchived(string, *time.Time, int, ...discordgo.RequestOption) (*discordgo.ThreadsList, error) {
	return &discordgo.ThreadsList{}, nil
}

func TestDiscordCrossMentionsVerifyEachDestination(t *testing.T) {
	hasher, err := identity.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	actorID := hasher.UserID("discord:9001")
	for _, tc := range []struct {
		name          string
		member        bool
		view          bool
		private       bool
		threadMember  bool
		anonymized    bool
		label         string
		observedAt    transport.EndpointID
		recovered     bool
		historyThread bool
		roster        bool
		rosterPages   bool
		rosterDenied  bool
		rosterStalled bool
		foreignActor  bool
		native        bool
	}{
		{name: "addressed group", member: true, view: true, observedAt: "discord", native: true},
		{name: "shared group", member: true, view: true, observedAt: "addressed", native: true},
		{name: "destination observation", member: true, view: true, observedAt: "discord", native: true},
		{name: "departed member", view: true, observedAt: "addressed"},
		{name: "channel access revoked", member: true, observedAt: "addressed"},
		{name: "private thread member", member: true, view: true, private: true, threadMember: true, observedAt: "addressed", native: true},
		{name: "private thread outsider", member: true, view: true, private: true, observedAt: "addressed"},
		{name: "anonymized route", member: true, view: true, anonymized: true, observedAt: "addressed", native: true},
		{name: "pseudonym on anonymized route", member: true, view: true, anonymized: true, label: identity.Pseudonym(actorID), observedAt: "addressed", native: true},
		{name: "departed member on anonymized route", view: true, anonymized: true, observedAt: "addressed"},
		{name: "revoked access on anonymized route", member: true, anonymized: true, observedAt: "addressed"},
		{name: "unobserved user", member: true, view: true},
		{name: "recovered member", member: true, view: true, recovered: true, native: true},
		{name: "recovered thread member", member: true, view: true, recovered: true, historyThread: true, native: true},
		{name: "cold lookup by HMAC", member: true, view: true, roster: true, native: true},
		{name: "cold lookup on anonymized route", member: true, view: true, roster: true, anonymized: true, label: identity.Pseudonym(actorID), native: true},
		{name: "paginated cold lookup", member: true, view: true, roster: true, rosterPages: true, native: true},
		{name: "roster permission unavailable", member: true, view: true, roster: true, rosterDenied: true},
		{name: "nonadvancing roster", member: true, view: true, rosterPages: true, rosterStalled: true},
		{name: "cold member access revoked", member: true, roster: true},
		{name: "foreign provider identity", member: true, view: true, roster: true, foreignActor: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session, err := discordgo.New("Bot test-token")
			if err != nil {
				t.Fatal(err)
			}
			session.Client = &http.Client{Transport: mentionRoundTripper(func(req *http.Request) (*http.Response, error) {
				status := http.StatusOK
				var value any
				switch req.URL.Path {
				case "/api/v9/channels/" + testChannelID:
					channel := &discordgo.Channel{ID: testChannelID, GuildID: "guild"}
					if !tc.view {
						channel.PermissionOverwrites = []*discordgo.PermissionOverwrite{{ID: "9001", Type: discordgo.PermissionOverwriteTypeMember, Deny: discordgo.PermissionViewChannel}}
					}
					value = channel
				case "/api/v9/channels/thread":
					value = &discordgo.Channel{ID: "thread", GuildID: "guild", ParentID: testChannelID, Type: discordgo.ChannelTypeGuildPrivateThread}
				case "/api/v9/channels/thread/thread-members/9001":
					if !tc.threadMember {
						status = http.StatusNotFound
					}
					value = &discordgo.ThreadMember{ID: "thread", UserID: "9001"}
				case "/api/v9/guilds/guild/members/9001":
					if !tc.member {
						status = http.StatusNotFound
					}
					value = &discordgo.Member{User: &discordgo.User{ID: "9001"}}
				case "/api/v9/guilds/guild/members":
					members := []*discordgo.Member{}
					if tc.rosterPages && (req.URL.Query().Get("after") == "" || tc.rosterStalled) {
						for id := 1; id <= 1000; id++ {
							members = append(members, &discordgo.Member{User: &discordgo.User{ID: strconv.Itoa(id)}})
						}
					} else if tc.roster {
						members = append(members, &discordgo.Member{User: &discordgo.User{ID: "9001", GlobalName: "Alice"}})
					}
					if tc.rosterDenied {
						status = http.StatusForbidden
					}
					value = members
				case "/api/v9/guilds/guild":
					value = &discordgo.Guild{ID: "guild", Roles: []*discordgo.Role{{ID: "guild", Permissions: discordgo.PermissionViewChannel}}}
				default:
					t.Fatalf("unexpected request: %s", req.URL.Path)
				}
				body, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})}
			webhook := &fakeChannelWebhook{}
			adapter := newOutboundTestAdapter(webhook, &fakeDiscordAPI{})
			adapter.webhook = crossMentionWebhook{webhook}
			adapter.session = session
			adapter.hasher = hasher
			if tc.observedAt != "" {
				adapter.rememberCrossMentionParticipant(tc.observedAt, actorID, "9001")
			}
			if tc.recovered {
				adapter.normalizer, err = NewNormalizer(map[string]string{"discord": testChannelID}, hasher, config.UsernameModePushName)
				if err != nil {
					t.Fatal(err)
				}
				message := recoveryMessage("423456789012345680", "hello")
				message.Author.ID = "9001"
				adapter.api = &recoveryDiscordAPI{pages: [][]*discordgo.Message{{message}}}
				if tc.historyThread {
					message.ChannelID = "history-thread"
					adapter.api = &crossMentionThreadRecoveryAPI{recoveryDiscordAPI: &recoveryDiscordAPI{pages: [][]*discordgo.Message{nil, {message}}}}
				}
				if err := adapter.Recover(context.Background(), transport.RecoveryRequest{Cursor: transport.Checkpoint{StreamKey: "discord"}, MaxEvents: 1}, func(_ context.Context, incoming transport.Incoming) error {
					if incoming.Sender.OpaqueID != actorID {
						t.Fatalf("recovered actor = %q", incoming.Sender.OpaqueID)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			refEndpoint := transport.EndpointID("addressed")
			if tc.name == "addressed group" || tc.recovered {
				refEndpoint = "discord"
			}
			label := tc.label
			if label == "" {
				label = "Alice"
			}
			token := "@{" + string(refEndpoint) + "/" + label + "}"
			refActor := actorID
			if tc.foreignActor {
				refActor = hasher.UserID("987654321@lid")
			}
			outgoing := transport.Outgoing{Endpoint: "discord", Kind: "text", SourceText: "Hi " + token + ", " + token,
				References: []transport.CrossReference{{Token: token, Endpoint: refEndpoint, ActorID: refActor, Name: "Alice"}}, ReferenceAnonymized: tc.anonymized}
			if tc.private {
				outgoing.ChildScope = &transport.ChildScope{RemoteID: "thread"}
			}
			if _, err := adapter.Send(context.Background(), outgoing); err != nil {
				t.Fatal(err)
			}
			got := webhook.executed[0]
			want := outgoing.SourceText
			var allowed []string
			if tc.native {
				want = "Hi <@9001>, <@9001>"
				allowed = []string{"9001"}
			}
			if got.Content != want || !reflect.DeepEqual(append([]string(nil), got.AllowedUserIDs...), allowed) {
				t.Fatalf("webhook = %+v, want %q with allowed users %v", got, want, allowed)
			}
		})
	}
}

func TestResolveDiscordParticipantNameAfterRestart(t *testing.T) {
	hasher, err := identity.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	first := &discordgo.Member{Nick: "vm", User: &discordgo.User{ID: "111", Username: "vm", GlobalName: "Victor"}}
	second := &discordgo.Member{User: &discordgo.User{ID: "222", Username: "vm"}}
	actor := hasher.UserID("discord:111")
	otherActor := hasher.UserID("discord:222")
	for _, tc := range []struct {
		name         string
		label        string
		members      []*discordgo.Member
		actors       []string
		denied       bool
		searchDenied bool
		want         bool
	}{
		{name: "username alias", label: "vm", members: []*discordgo.Member{first}, actors: []string{actor}, want: true},
		{name: "display name", label: "Victor", members: []*discordgo.Member{first}, actors: []string{actor}, want: true},
		{name: "case normalized", label: " VM ", members: []*discordgo.Member{first}, actors: []string{actor}, want: true},
		{name: "ambiguous name", label: "vm", members: []*discordgo.Member{first, second}, actors: []string{actor, otherActor}},
		{name: "unregistered identity", label: "vm", members: []*discordgo.Member{first}, actors: []string{otherActor}},
		{name: "source access revoked", label: "vm", members: []*discordgo.Member{first}, actors: []string{actor}, denied: true},
		{name: "search unavailable", label: "vm", actors: []string{actor}, searchDenied: true},
		{name: "unknown name", label: "Missing", members: []*discordgo.Member{first}, actors: []string{actor}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session, err := discordgo.New("Bot test-token")
			if err != nil {
				t.Fatal(err)
			}
			session.Client = &http.Client{Transport: mentionRoundTripper(func(req *http.Request) (*http.Response, error) {
				status := http.StatusOK
				var value any
				switch {
				case req.URL.Path == "/api/v9/channels/"+testChannelID:
					channel := &discordgo.Channel{ID: testChannelID, GuildID: "guild"}
					if tc.denied {
						channel.PermissionOverwrites = []*discordgo.PermissionOverwrite{{ID: "111", Type: discordgo.PermissionOverwriteTypeMember, Deny: discordgo.PermissionViewChannel}}
					}
					value = channel
				case req.URL.Path == "/api/v9/guilds/guild/members/search":
					value = tc.members
					if tc.searchDenied {
						status = http.StatusForbidden
					}
				case strings.HasPrefix(req.URL.Path, "/api/v9/guilds/guild/members/"):
					value = &discordgo.Member{User: &discordgo.User{ID: strings.TrimPrefix(req.URL.Path, "/api/v9/guilds/guild/members/")}}
				case req.URL.Path == "/api/v9/guilds/guild":
					value = &discordgo.Guild{ID: "guild", Roles: []*discordgo.Role{{ID: "guild", Permissions: discordgo.PermissionViewChannel}}}
				default:
					t.Fatalf("unexpected provider operation: %s", req.URL.Path)
				}
				body, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})}
			adapter := &Adapter{session: session, hasher: hasher, targets: map[transport.EndpointID]string{"dg1": testChannelID}}
			got, found := adapter.ResolveParticipant(context.Background(), "dg1", tc.label, tc.actors)
			if found != tc.want || tc.want && got != actor || !tc.want && got != "" {
				t.Fatalf("resolved identity = %q, found=%t; want found=%t", got, found, tc.want)
			}
		})
	}
}
