package discord

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/transport"
)

func TestPrivateDiscordBotSendAndNativeReplyWithoutDeletion(t *testing.T) {
	session, err := discordgo.New("Bot private-test")
	if err != nil {
		t.Fatal(err)
	}
	session.State.User = &discordgo.User{ID: "777", Bot: true}
	var sent map[string]any
	deletes := 0
	posts := 0
	session.Client = &http.Client{Transport: mentionRoundTripper(func(req *http.Request) (*http.Response, error) {
		body := `{"id":"100","type":1,"recipients":[{"id":"42"}]}`
		if req.Method == http.MethodDelete {
			deletes++
		}
		if strings.HasSuffix(req.URL.Path, "/messages") {
			posts++
			if err := json.NewDecoder(req.Body).Decode(&sent); err != nil {
				t.Fatal(err)
			}
			body = `{"id":"200","channel_id":"100"}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	hasher, _ := identity.New([]byte("01234567890123456789012345678901"))
	a := &Adapter{session: session, connectionID: "owner", privateDMEnabled: true, hasher: hasher, privateEvents: make(chan transport.PrivateIncoming, 5)}
	token, _ := json.Marshal(privateInboxKey{Channel: "100", User: "42"})
	inbox := transport.PrivateInbox{ConnectionID: "owner", Transport: "discord", OwnerID: a.privateOwnerID(), ChatID: "100", Token: token}
	result, err := a.SendPrivate(context.Background(), inbox, "DM from group/Anonymous\nhello")
	if err != nil || result.RemoteMessageID != "200" {
		t.Fatalf("private send=%+v %v", result, err)
	}
	allowed := sent["allowed_mentions"].(map[string]any)
	if len(allowed["parse"].([]any)) != 0 || sent["flags"] != float64(discordgo.MessageFlagsSuppressEmbeds) {
		t.Fatal("private send allowed native pings or previews")
	}
	message := &discordgo.Message{ID: "201", ChannelID: "100", Author: &discordgo.User{ID: "42"}, Content: "answer", Timestamp: time.Now(), MessageReference: &discordgo.MessageReference{MessageID: "200", ChannelID: "100"}}
	if !a.handlePrivateMessage(session, message) {
		t.Fatal("verified DM was rejected")
	}
	in := <-a.PrivateEvents()
	if in.ActorID != hasher.UserID("discord:42") || in.ReplyToMessageID != "200" || string(in.Inbox.Token) != string(token) {
		t.Fatal("native reply lost exact inbox identity")
	}
	if a.DeletePrivateCopyForMe(context.Background(), transport.PrivateCopy{ConnectionID: "owner", Transport: "discord", Token: token}) == nil || deletes != 0 || posts != 1 {
		t.Fatal("Discord cleanup deleted a participant-visible message")
	}
	message.Author.Bot = true
	a.handlePrivateMessage(session, message)
	select {
	case <-a.PrivateEvents():
		t.Fatal("bot echo recursively handled")
	default:
	}
	if discordGatewayIntents()&discordgo.IntentsDirectMessages != 0 || discordGatewayIntents(true)&discordgo.IntentsDirectMessages == 0 {
		t.Fatal("DM intent did not follow explicit enablement")
	}
}

func TestGroupDiscordDMCommandKeepsGroupSemantics(t *testing.T) {
	hasher, _ := identity.New([]byte("01234567890123456789012345678901"))
	normalizer, _ := NewNormalizer(map[string]string{"group": "123"}, hasher, config.UsernameModeHash)
	text := "/dm @{other/P1} group message"
	in, ok := normalizer.NormalizeMessage(&discordgo.MessageCreate{Message: &discordgo.Message{ID: "12345", ChannelID: "123", GuildID: "456", Author: &discordgo.User{ID: "42"}, Content: text, Timestamp: time.Now()}}, "999", nil)
	if !ok || in.Endpoint != "group" || in.Kind != "text" || in.Text != text {
		t.Fatal("group /dm was intercepted or changed")
	}
}

func TestPrivateDiscordDisablesSDKCreateRetries(t *testing.T) {
	for _, status := range []int{http.StatusBadGateway, http.StatusTooManyRequests, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			session, _ := discordgo.New("Bot synthetic")
			session.State.User = &discordgo.User{ID: "777", Bot: true}
			session.MaxRestRetries = 3
			session.ShouldRetryOnRateLimit = true
			creates := 0
			session.Client = &http.Client{Transport: mentionRoundTripper(func(req *http.Request) (*http.Response, error) {
				code, body := 200, `{"id":"100","type":1,"recipients":[{"id":"42"}]}`
				if strings.HasSuffix(req.URL.Path, "/messages") {
					creates++
					code = status
					body = `{"message":"fixture failure","retry_after":0.001}`
				}
				return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			hasher, _ := identity.New([]byte("01234567890123456789012345678901"))
			a := &Adapter{session: session, connectionID: "owner", privateDMEnabled: true, hasher: hasher}
			token, _ := json.Marshal(privateInboxKey{Channel: "100", User: "42"})
			_, err := a.SendPrivate(context.Background(), transport.PrivateInbox{ConnectionID: "owner", Transport: "discord", OwnerID: a.privateOwnerID(), ChatID: "100", Token: token}, "private body")
			if err == nil || creates != 1 {
				t.Fatalf("private create attempted %d times, error=%v", creates, err)
			}
		})
	}
}
