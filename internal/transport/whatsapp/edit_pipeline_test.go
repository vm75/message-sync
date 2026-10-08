package whatsapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/recovery"
	"github.com/vm75/message-sync/internal/router"
	"github.com/vm75/message-sync/internal/store"
	"github.com/vm75/message-sync/internal/transport"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.mau.fi/whatsmeow/util/gcmutil"
	"go.mau.fi/whatsmeow/util/hkdfutil"
	"google.golang.org/protobuf/proto"
)

type pipelineEdit struct {
	ref  transport.MessageRef
	text string
}

type editPipelineSender struct {
	creates chan transport.Outgoing
	edits   chan pipelineEdit
}

func (s *editPipelineSender) Send(_ context.Context, out transport.Outgoing) (transport.MessageRef, error) {
	s.creates <- out
	return transport.MessageRef{Endpoint: out.Endpoint, RemoteMessageID: string(out.Endpoint) + "-copy", IsTargetFromMe: true}, nil
}
func (s *editPipelineSender) Edit(_ context.Context, ref transport.MessageRef, text string) error {
	s.edits <- pipelineEdit{ref, text}
	return nil
}
func (*editPipelineSender) React(context.Context, transport.Reaction) error    { return nil }
func (*editPipelineSender) Delete(context.Context, transport.MessageRef) error { return nil }

// Exercise provider unwrapping and both adapter ingress paths before the shared
// recovery coordinator. Starting with a normalized edit would miss history
// edits that ParseWebMessage unwraps without setting IsEdit.
func TestWhatsAppPlainTextEditPipeline(t *testing.T) {
	for _, shape := range []string{"live protocol", "live wrapped", "history protocol", "history wrapped", "live secret", "history secret"} {
		for _, fromSelf := range []bool{false, true} {
			t.Run(shape+map[bool]string{false: "/participant", true: "/linked self"}[fromSelf], func(t *testing.T) {
				ctx := context.Background()
				db, err := store.Open(ctx, t.TempDir()+"/sync.db")
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				cfg := &config.Config{
					Endpoints: map[string]config.Endpoint{
						"source":   {Transport: config.TransportWhatsApp, RemoteID: "111@g.us"},
						"discord":  {Transport: config.TransportDiscord, RemoteID: "222"},
						"whatsapp": {Transport: config.TransportWhatsApp, RemoteID: "333@g.us"},
						"telegram": {Transport: config.TransportTelegram, RemoteID: "-444"},
					},
					SyncSets: []config.SyncSet{{ID: "mesh", Endpoints: []string{"source", "discord", "whatsapp", "telegram"}}},
					Identity: config.Identity{UsernameMode: config.UsernameModePushName},
				}
				sender := &editPipelineSender{creates: make(chan transport.Outgoing, 10), edits: make(chan pipelineEdit, 10)}
				mesh, err := router.New(cfg, db, sender)
				if err != nil {
					t.Fatal(err)
				}
				defer mesh.Close()
				coordinator, err := recovery.NewCoordinator(db, mesh)
				if err != nil {
					t.Fatal(err)
				}
				hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
				if err != nil {
					t.Fatal(err)
				}
				normalizer, err := NewNormalizer(map[string]string{"source": "111@g.us"}, hasher, config.UsernameModePushName)
				if err != nil {
					t.Fatal(err)
				}
				adapter := &Adapter{
					normalizer: normalizer, client: &whatsmeow.Client{},
					mediaEnabled: false, recoveryEnabled: true,
					events: make(chan transport.Incoming, 10), pcache: newParticipantCache(10),
					lifecycle: newLifecycleSuppression(), logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
				}
				timestamp := time.Now().UTC().Truncate(time.Second)
				info := types.MessageInfo{
					MessageSource: types.MessageSource{Chat: types.NewJID("111", types.GroupServer), Sender: types.NewJID("15551234567", types.DefaultUserServer), IsGroup: true, IsFromMe: fromSelf},
					ID:            "original", PushName: "Alice", Timestamp: timestamp,
				}
				adapter.handleEvent((&events.Message{Info: info, RawMessage: &waE2E.Message{Conversation: proto.String("before")}}).UnwrapRaw())
				original := receivePipelineIncoming(t, adapter)
				if _, err := coordinator.Handle(ctx, original); err != nil {
					t.Fatal(err)
				}
				for range 3 {
					select {
					case <-sender.creates:
					case <-time.After(time.Second):
						t.Fatal("original was not forwarded")
					}
				}
				// Wait for copy commits and then accept the original checkpoint.
				deadline := time.Now().Add(time.Second)
				for {
					outcome, err := coordinator.Handle(ctx, original)
					if err != nil {
						t.Fatal(err)
					}
					if outcome.SafeToAdvance {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("original delivery did not settle")
					}
					time.Sleep(time.Millisecond)
				}
				key := &waCommon.MessageKey{ID: proto.String("original"), Participant: proto.String(info.Sender.String()), FromMe: proto.Bool(fromSelf)}
				raw := &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
					Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(), Key: key,
					EditedMessage: &waE2E.Message{Conversation: proto.String("after")},
				}}
				if shape == "live wrapped" || shape == "history wrapped" {
					raw = &waE2E.Message{EditedMessage: &waE2E.FutureProofMessage{Message: raw}}
				}
				if shape == "live secret" || shape == "history secret" {
					secret := sha256.Sum256([]byte("synthetic original message secret"))
					protocolStore := &pipelineSecretStore{secret: secret[:], sender: info.Sender}
					adapter.client.Store = &waStore.Device{MsgSecrets: protocolStore, LIDs: protocolStore}
					raw = encryptedPipelineEdit(t, key, info.Sender, secret[:], raw.ProtocolMessage.EditedMessage)
				}
				if shape == "live protocol" || shape == "live wrapped" || shape == "live secret" {
					editInfo := info
					editInfo.ID = "edit-notification"
					editInfo.Edit = types.EditAttributeMessageEdit
					adapter.handleEvent((&events.Message{Info: editInfo, RawMessage: raw}).UnwrapRaw())
				} else {
					adapter.handleEvent(&events.HistorySync{Data: &waHistorySync.HistorySync{Conversations: []*waHistorySync.Conversation{{
						ID: proto.String(info.Chat.String()),
						Messages: []*waHistorySync.HistorySyncMsg{{Message: &waWeb.WebMessageInfo{
							Key:     &waCommon.MessageKey{ID: proto.String("history-edit-notification"), FromMe: proto.Bool(fromSelf), Participant: proto.String(info.Sender.String())},
							Message: raw, MessageTimestamp: proto.Uint64(uint64(timestamp.Unix())), PushName: proto.String("Alice"),
							OriginalSelfAuthorUserJIDString: proto.String(info.Sender.String()),
						}}},
					}}}})
				}
				edit := receivePipelineIncoming(t, adapter)
				if _, err := coordinator.Handle(ctx, edit); err != nil {
					t.Fatal(err)
				}
				seen := map[transport.EndpointID]bool{}
				for range 3 {
					select {
					case call := <-sender.edits:
						if call.ref.Endpoint == "source" || seen[call.ref.Endpoint] {
							t.Fatalf("unexpected edit destination: %+v", call.ref)
						}
						seen[call.ref.Endpoint] = true
						if call.ref.RemoteMessageID != string(call.ref.Endpoint)+"-copy" {
							t.Fatalf("wrong edit target: %+v", call.ref)
						}
						if call.text != "*_source/Alice_*: after" {
							t.Fatalf("edit text = %q", call.text)
						}
					case <-time.After(time.Second):
						t.Fatalf("plain text edit did not reach every target: kind=%s, edits=%v", edit.Kind, seen)
					}
				}
				select {
				case call := <-sender.creates:
					t.Fatalf("edit created duplicate: %+v", call)
				default:
				}
				if edit.Kind != "edit" || edit.ReplyTo == nil || edit.ReplyTo.RemoteMessageID != "original" || edit.Checkpoint.Valid {
					t.Fatalf("incorrect normalized edit metadata: %+v", edit)
				}
				if fromSelf {
					adapter.lifecycle.mark("source", "original", "edit", "", time.Now().UTC())
					echoInfo := info
					echoInfo.ID = "bridge-edit-echo"
					echoInfo.Edit = types.EditAttributeMessageEdit
					adapter.handleEvent((&events.Message{Info: echoInfo, RawMessage: raw}).UnwrapRaw())
					select {
					case incoming := <-adapter.Events():
						t.Fatalf("matching bridge echo was emitted: %+v", incoming)
					default:
					}
				}
			})
		}
	}
}

// Only protocol-owned secret access is needed by the client to decrypt the
// synthetic fixture. No message plaintext is persisted by this test double.
type pipelineSecretStore struct {
	waStore.MsgSecretStore
	waStore.LIDStore
	secret []byte
	sender types.JID
	err    error
}

func (s *pipelineSecretStore) GetMessageSecret(context.Context, types.JID, types.JID, types.MessageID) ([]byte, types.JID, error) {
	return s.secret, s.sender, s.err
}
func (*pipelineSecretStore) GetLIDForPN(context.Context, types.JID) (types.JID, error) {
	return types.EmptyJID, nil
}
func (*pipelineSecretStore) GetPNForLID(context.Context, types.JID) (types.JID, error) {
	return types.EmptyJID, nil
}

func encryptedPipelineEdit(t *testing.T, target *waCommon.MessageKey, sender types.JID, secret []byte, body *waE2E.Message) *waE2E.Message {
	t.Helper()
	plaintext, err := proto.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	// Fixture key derivation follows WhatsApp's MESSAGE_EDIT use case. The
	// production client, rather than a stub decryptor, must decrypt this event.
	info := target.GetID() + sender.ToNonAD().String() + sender.ToNonAD().String() + string(whatsmeow.EncSecretMessageEdit)
	key := hkdfutil.SHA256(secret, nil, []byte(info), 32)
	iv := make([]byte, 12)
	ciphertext, err := gcmutil.Encrypt(key, iv, plaintext, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &waE2E.Message{SecretEncryptedMessage: &waE2E.SecretEncryptedMessage{
		SecretEncType: waE2E.SecretEncryptedMessage_MESSAGE_EDIT.Enum(), TargetMessageKey: target,
		EncIV: iv, EncPayload: ciphertext,
	}}
}

func receivePipelineIncoming(t *testing.T, adapter *Adapter) transport.Incoming {
	t.Helper()
	select {
	case incoming := <-adapter.Events():
		return incoming
	case <-time.After(time.Second):
		t.Fatal("WhatsApp event was not emitted")
		return transport.Incoming{}
	}
}

func TestEncryptedEditFailureDoesNotEmitOrLogProviderData(t *testing.T) {
	for _, failure := range []string{"missing secret", "provider error", "invalid ciphertext", "missing target"} {
		t.Run(failure, func(t *testing.T) {
			hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
			if err != nil {
				t.Fatal(err)
			}
			normalizer, err := NewNormalizer(map[string]string{"source": "111@g.us"}, hasher, config.UsernameModePushName)
			if err != nil {
				t.Fatal(err)
			}
			sender := types.NewJID("15551234567", types.DefaultUserServer)
			secret := sha256.Sum256([]byte("synthetic original message secret"))
			protocolStore := &pipelineSecretStore{secret: secret[:], sender: sender}
			key := &waCommon.MessageKey{ID: proto.String("original"), Participant: proto.String(sender.String())}
			raw := encryptedPipelineEdit(t, key, sender, secret[:], &waE2E.Message{Conversation: proto.String("private edited body")})
			switch failure {
			case "missing secret":
				protocolStore.secret = nil
			case "provider error":
				protocolStore.err = errors.New("private provider error: 15551234567")
			case "invalid ciphertext":
				raw.SecretEncryptedMessage.EncPayload[0] ^= 1
			case "missing target":
				raw.SecretEncryptedMessage.TargetMessageKey = nil
			}
			var logs bytes.Buffer
			adapter := &Adapter{
				normalizer: normalizer, client: &whatsmeow.Client{Store: &waStore.Device{MsgSecrets: protocolStore, LIDs: protocolStore}},
				events: make(chan transport.Incoming, 1), pcache: newParticipantCache(10), lifecycle: newLifecycleSuppression(),
				logger: slog.New(slog.NewTextHandler(&logs, nil)),
			}
			adapter.handleEvent((&events.Message{RawMessage: raw, Info: types.MessageInfo{
				MessageSource: types.MessageSource{Chat: types.NewJID("111", types.GroupServer), Sender: sender, IsGroup: true},
				ID:            "edit-notification", Timestamp: time.Now(), PushName: "Private Display Name",
			}}).UnwrapRaw())
			select {
			case incoming := <-adapter.Events():
				t.Fatalf("failed edit was emitted: %+v", incoming)
			default:
			}
			if !strings.Contains(logs.String(), "whatsapp_edit_dropped") {
				t.Fatal("missing safe failure diagnostic")
			}
			for _, canary := range []string{"15551234567", "111@g.us", "private edited body", "Private Display Name", "private provider error"} {
				if strings.Contains(logs.String(), canary) {
					t.Fatalf("provider data leaked to log: %q", canary)
				}
			}
		})
	}
}
