package telegram

import (
	"context"
	"errors"
	"html"
	"strings"
	"testing"

	telegrambot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/vm75/message-sync/internal/transport"
)

type crossMentionAPI struct {
	*fakeTelegramAPI
	checks []*telegrambot.GetChatMemberParams
	lookup func(*telegrambot.GetChatMemberParams) (*models.ChatMember, error)
}

func (f *crossMentionAPI) GetChatMember(_ context.Context, params *telegrambot.GetChatMemberParams) (*models.ChatMember, error) {
	f.checks = append(f.checks, params)
	if f.lookup != nil {
		return f.lookup(params)
	}
	return &models.ChatMember{Type: models.ChatMemberTypeMember, Member: &models.ChatMemberMember{User: &models.User{ID: params.UserID}}}, nil
}

func TestTelegramCrossMentionMembershipAndLabels(t *testing.T) {
	for _, tc := range []struct {
		name   string
		member *models.ChatMember
		err    error
		native bool
	}{
		{name: "member", member: &models.ChatMember{Member: &models.ChatMemberMember{User: &models.User{ID: 123}}}, native: true},
		{name: "owner", member: &models.ChatMember{Owner: &models.ChatMemberOwner{User: &models.User{ID: 123}}}, native: true},
		{name: "administrator", member: &models.ChatMember{Administrator: &models.ChatMemberAdministrator{User: models.User{ID: 123}}}, native: true},
		{name: "restricted member", member: &models.ChatMember{Restricted: &models.ChatMemberRestricted{User: &models.User{ID: 123}, IsMember: true}}, native: true},
		{name: "departed", member: &models.ChatMember{Left: &models.ChatMemberLeft{User: &models.User{ID: 123}}}},
		{name: "banned", member: &models.ChatMember{Banned: &models.ChatMemberBanned{User: &models.User{ID: 123}}}},
		{name: "restricted nonmember", member: &models.ChatMember{Restricted: &models.ChatMemberRestricted{User: &models.User{ID: 123}}}},
		{name: "nil membership"},
		{name: "empty membership", member: &models.ChatMember{}},
		{name: "wrong member", member: &models.ChatMember{Member: &models.ChatMemberMember{User: &models.User{ID: 456}}}},
		{name: "lookup error", err: errors.New("unavailable")},
	} {
		for _, label := range []string{"Alice Smith", "Rust Ocean Q7M5K", "Rust Ocean", "P1", strings.Repeat("&", 96)} {
			t.Run(tc.name+"/"+label, func(t *testing.T) {
				api := &crossMentionAPI{fakeTelegramAPI: &fakeTelegramAPI{}, lookup: func(params *telegrambot.GetChatMemberParams) (*models.ChatMember, error) {
					if params.ChatID != int64(-1001234567890) || params.UserID != 123 {
						t.Fatalf("checked wrong destination or member: %+v", params)
					}
					return tc.member, tc.err
				}}
				adapter := newOutboundTestAdapter(t, api.fakeTelegramAPI)
				adapter.client = api
				adapter.rememberCrossMentionParticipant("addressed", "u_abcdefghij", 123)
				token := "@{addressed/" + label + "}"
				body := "Hi " + token + ` and \` + token
				outgoing := transport.Outgoing{Endpoint: "tg", Kind: "text", SourceText: body,
					References: []transport.CrossReference{{Token: token, Endpoint: "addressed", ActorID: "u_abcdefghij", Name: label, Handle: "P1", Pseudonym: "Rust Ocean Q7M5K"}}}
				if _, err := adapter.Send(context.Background(), outgoing); err != nil {
					t.Fatal(err)
				}
				want := body
				if tc.native {
					want = `Hi <a href="tg://user?id=123">@` + html.EscapeString(label) + `</a> and \` + html.EscapeString(token)
				}
				if len(api.texts) != 1 || api.texts[0] != want {
					t.Fatalf("Telegram text = %#v, want %q", api.texts, want)
				}
				if tc.native && api.textParseMode != models.ParseModeHTML {
					t.Fatalf("native mention parse mode = %q", api.textParseMode)
				}
			})
		}
	}
}

func TestCrossMentionInAnotherDestinationGroup(t *testing.T) {
	api := &crossMentionAPI{fakeTelegramAPI: &fakeTelegramAPI{}}
	adapter := newOutboundTestAdapter(t, api.fakeTelegramAPI)
	adapter.client = api
	adapter.rememberCrossMentionParticipant("addressed", "u_abcdefghij", 123)
	_, err := adapter.Send(context.Background(), transport.Outgoing{
		Endpoint: "tg", OriginEndpoint: "source", Kind: "text",
		SourceText: "Hi @{addressed/Alice}",
		References: []transport.CrossReference{{Token: "@{addressed/Alice}", Endpoint: "addressed", ActorID: "u_abcdefghij", Handle: "P1", Name: "Alice"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(api.texts) != 1 || !strings.Contains(api.texts[0], `<a href="tg://user?id=123">@Alice</a>`) {
		t.Fatalf("missing native mention for a verified shared member: %#v", api.texts)
	}
}
