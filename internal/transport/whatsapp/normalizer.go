package whatsapp

import (
	"errors"
	"fmt"
	"strings"

	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/transport"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type Normalizer struct {
	endpoints    map[string]transport.EndpointID
	hasher       *identity.Hasher
	usernameMode string
}

func NewNormalizer(groupJIDs map[string]string, hasher *identity.Hasher, usernameMode string) (*Normalizer, error) {
	if hasher == nil {
		return nil, errors.New("identity hasher is required")
	}
	if usernameMode != "push_name" && usernameMode != "hash" {
		return nil, errors.New("username mode must be push_name or hash")
	}

	endpoints := make(map[string]transport.EndpointID, len(groupJIDs))
	for alias, rawJID := range groupJIDs {
		jid, err := types.ParseJID(strings.TrimSpace(rawJID))
		if err != nil || jid.Server != types.GroupServer || jid.User == "" {
			return nil, fmt.Errorf("group %q has invalid WhatsApp group JID", alias)
		}
		key := jid.ToNonAD().String()
		if _, duplicate := endpoints[key]; duplicate {
			return nil, fmt.Errorf("group %q duplicates a configured WhatsApp group", alias)
		}
		endpoints[key] = transport.EndpointID(alias)
	}
	return &Normalizer{endpoints: endpoints, hasher: hasher, usernameMode: usernameMode}, nil
}

func (n *Normalizer) NormalizeMessage(evt *events.Message) (transport.Incoming, bool) {
	if n == nil || evt == nil || evt.Message == nil || !evt.Info.IsGroup || evt.Info.Chat.Server != types.GroupServer {
		return transport.Incoming{}, false
	}
	endpoint, configured := n.endpoints[evt.Info.Chat.ToNonAD().String()]
	if !configured || strings.TrimSpace(string(evt.Info.ID)) == "" || evt.Info.Sender.IsEmpty() {
		return transport.Incoming{}, false
	}

	displayName := ""
	if n.usernameMode == "push_name" {
		displayName = strings.TrimSpace(evt.Info.PushName)
	}
	kind, text := normalizedPayload(evt.Message)

	return transport.Incoming{
		Endpoint: endpoint,
		RemoteID: string(evt.Info.ID),
		Sender: transport.Sender{
			DisplayName: displayName,
			OpaqueID:    n.hasher.UserID(evt.Info.Sender.ToNonAD().String()),
		},
		FromSelf:  evt.Info.IsFromMe,
		Kind:      kind,
		Text:      text,
		Timestamp: evt.Info.Timestamp,
	}, true
}

func normalizedPayload(msg *waE2E.Message) (kind, text string) {
	if msg == nil {
		return "other", ""
	}
	if msg.Conversation != nil {
		return "text", msg.GetConversation()
	}
	if content := msg.GetExtendedTextMessage(); content != nil {
		return "text", content.GetText()
	}
	if content := msg.GetImageMessage(); content != nil {
		return "image", content.GetCaption()
	}
	if content := msg.GetVideoMessage(); content != nil {
		return "video", content.GetCaption()
	}
	if content := msg.GetDocumentMessage(); content != nil {
		return "document", content.GetCaption()
	}
	if msg.GetAudioMessage() != nil {
		return "audio", ""
	}
	if msg.GetStickerMessage() != nil {
		return "sticker", ""
	}
	return "other", ""
}
