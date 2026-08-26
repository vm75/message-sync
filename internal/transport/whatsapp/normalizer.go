package whatsapp

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/transport"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type Normalizer struct {
	endpoints    map[string]transport.EndpointID
	hasher       *identity.Hasher
	usernameMode config.UsernameMode
}

type MediaDownloader func(context.Context, whatsmeow.DownloadableMessage) ([]byte, error)
type VoteDecryptor func(context.Context, *events.Message) (*waE2E.PollVoteMessage, error)
type ContactGetter func(types.JID) types.ContactInfo

func NewNormalizer(groupJIDs map[string]string, hasher *identity.Hasher, usernameMode config.UsernameMode) (*Normalizer, error) {
	if hasher == nil {
		return nil, errors.New("identity hasher is required")
	}
	if !usernameMode.IsValid() {
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

func (n *Normalizer) NormalizeMessage(evt *events.Message, mediaEnabled bool, mediaMaxBytes uint64, downloader MediaDownloader, decryptor VoteDecryptor, contactGetter ContactGetter) (transport.Incoming, bool) {
	if n == nil || evt == nil || evt.Message == nil || !evt.Info.IsGroup || evt.Info.Chat.Server != types.GroupServer {
		return transport.Incoming{}, false
	}
	endpoint, configured := n.endpoints[evt.Info.Chat.ToNonAD().String()]
	if !configured || strings.TrimSpace(string(evt.Info.ID)) == "" || evt.Info.Sender.IsEmpty() {
		return transport.Incoming{}, false
	}

	displayName := ""
	if n.usernameMode == config.UsernameModePushName {
		displayName = strings.TrimSpace(evt.Info.PushName)
	}
	kind, text, downloadable, fileLength := normalizedPayload(evt.Message)

	if kind != "text" && kind != "other" && kind != "reaction" && kind != "poll" && kind != "poll_vote" {
		if !mediaEnabled {
			return transport.Incoming{}, false
		}
		if fileLength > mediaMaxBytes {
			// Skip oversized media
			return transport.Incoming{}, false
		}
	}

	var loader func(context.Context) ([]byte, error)
	if downloadable != nil && downloader != nil {
		loader = func(ctx context.Context) ([]byte, error) {
			return downloader(ctx, downloadable)
		}
	}

	var replyTo *transport.MessageRef
	var quotedText string
	var pollOptions []string
	var pollSelectableCount int
	var pollOptionHashes []string
	var extractedMentions []transport.Mention

	contextInfo := getContextInfo(evt.Message)
	if contextInfo != nil && len(contextInfo.GetMentionedJID()) > 0 {
		extractedMentions = extractMentions(contextInfo.GetMentionedJID(), contactGetter)
	}

	if kind == "poll" {
		if poll := getPollCreation(evt.Message); poll != nil {
			for _, opt := range poll.GetOptions() {
				pollOptions = append(pollOptions, opt.GetOptionName())
			}
			pollSelectableCount = int(poll.GetSelectableOptionsCount())
		}
	} else if kind == "poll_vote" {
		if pollUpdate := evt.Message.GetPollUpdateMessage(); pollUpdate != nil {
			key := pollUpdate.GetPollCreationMessageKey()
			if key != nil && key.GetID() != "" {
				replyTo = &transport.MessageRef{
					Endpoint:        endpoint,
					RemoteMessageID: key.GetID(),
				}
			}
			if decryptor != nil {
				if voteMsg, err := decryptor(context.Background(), evt); err == nil && voteMsg != nil {
					for _, hashBytes := range voteMsg.GetSelectedOptions() {
						pollOptionHashes = append(pollOptionHashes, hex.EncodeToString(hashBytes))
					}
				}
			}
		}
	} else if kind == "delete" {
		protoMsg := evt.Message.GetProtocolMessage()
		var targetID string
		if protoMsg != nil && protoMsg.GetKey() != nil {
			targetID = protoMsg.GetKey().GetID()
		}
		if targetID == "" {
			return transport.Incoming{}, false
		}
		replyTo = &transport.MessageRef{
			Endpoint:        endpoint,
			RemoteMessageID: targetID,
		}
	} else if kind == "edit" {
		var targetID string
		if protoMsg := evt.Message.GetProtocolMessage(); protoMsg != nil && protoMsg.GetKey() != nil {
			targetID = protoMsg.GetKey().GetID()
		}
		if targetID == "" {
			targetID = string(evt.Info.ID)
		}
		replyTo = &transport.MessageRef{
			Endpoint:        endpoint,
			RemoteMessageID: targetID,
		}
	} else if kind == "reaction" {
		reactionMsg := evt.Message.GetReactionMessage()
		if reactionMsg != nil && reactionMsg.GetKey() != nil && reactionMsg.GetKey().GetID() != "" {
			replyTo = &transport.MessageRef{
				Endpoint:        endpoint,
				RemoteMessageID: reactionMsg.GetKey().GetID(),
			}
		}
	} else {
		// Look for standard replies
		var contextInfo *waE2E.ContextInfo
		if evt.Message.GetExtendedTextMessage() != nil {
			contextInfo = evt.Message.GetExtendedTextMessage().GetContextInfo()
		} else if evt.Message.GetImageMessage() != nil {
			contextInfo = evt.Message.GetImageMessage().GetContextInfo()
		} else if evt.Message.GetVideoMessage() != nil {
			contextInfo = evt.Message.GetVideoMessage().GetContextInfo()
		} else if evt.Message.GetDocumentMessage() != nil {
			contextInfo = evt.Message.GetDocumentMessage().GetContextInfo()
		} else if evt.Message.GetAudioMessage() != nil {
			contextInfo = evt.Message.GetAudioMessage().GetContextInfo()
		} else if evt.Message.GetStickerMessage() != nil {
			contextInfo = evt.Message.GetStickerMessage().GetContextInfo()
		}

		if contextInfo != nil && contextInfo.GetStanzaID() != "" {
			replyTo = &transport.MessageRef{
				Endpoint:        endpoint,
				RemoteMessageID: contextInfo.GetStanzaID(),
			}
			if qm := contextInfo.GetQuotedMessage(); qm != nil {
				_, qText, _, _ := normalizedPayload(qm)
				quotedText = qText
			}
		}
	}

	phone := evt.Info.Sender.User
	if evt.Info.Sender.Server == types.HiddenUserServer || evt.Info.Sender.Server == types.HostedLIDServer {
		phone = ""
	}

	return transport.Incoming{
		Endpoint: endpoint,
		RemoteID: string(evt.Info.ID),
		Sender: transport.Sender{
			DisplayName: displayName,
			PhoneNumber: phone,
			OpaqueID:    n.hasher.UserID(evt.Info.Sender.ToNonAD().String()),
		},
		FromSelf:            evt.Info.IsFromMe,
		Kind:                kind,
		Text:                text,
		Mentions:            extractedMentions,
		ReplyTo:             replyTo,
		QuotedText:          quotedText,
		Timestamp:           evt.Info.Timestamp,
		MediaLoader:         loader,
		PollOptions:         pollOptions,
		PollSelectableCount: pollSelectableCount,
		PollOptionHashes:    pollOptionHashes,
	}, true
}

func extractMentions(mentions []string, contactGetter ContactGetter) []transport.Mention {
	if len(mentions) == 0 {
		return nil
	}
	var result []transport.Mention
	for _, rawJID := range mentions {
		jid, err := types.ParseJID(rawJID)
		if err != nil {
			continue
		}
		var name string
		if contactGetter != nil {
			info := contactGetter(jid)
			if info.Found {
				if info.PushName != "" {
					name = info.PushName
				} else if info.FullName != "" {
					name = info.FullName
				} else if info.BusinessName != "" {
					name = info.BusinessName
				}
			}
		}
		if name == "" {
			name = jid.User
		}
		result = append(result, transport.Mention{
			RemoteID: jid.User,
			Name:     name,
		})
	}
	return result
}

func getContextInfo(msg *waE2E.Message) *waE2E.ContextInfo {
	if msg == nil {
		return nil
	}
	if msg.ExtendedTextMessage != nil {
		return msg.ExtendedTextMessage.ContextInfo
	}
	if msg.ImageMessage != nil {
		return msg.ImageMessage.ContextInfo
	}
	if msg.VideoMessage != nil {
		return msg.VideoMessage.ContextInfo
	}
	if msg.DocumentMessage != nil {
		return msg.DocumentMessage.ContextInfo
	}
	if msg.AudioMessage != nil {
		return msg.AudioMessage.ContextInfo
	}
	if msg.StickerMessage != nil {
		return msg.StickerMessage.ContextInfo
	}
	return nil
}

func getPollCreation(msg *waE2E.Message) *waE2E.PollCreationMessage {
	if msg == nil {
		return nil
	}
	if p := msg.GetPollCreationMessage(); p != nil {
		return p
	}
	if p := msg.GetPollCreationMessageV2(); p != nil {
		return p
	}
	if p := msg.GetPollCreationMessageV3(); p != nil {
		return p
	}
	if p := msg.GetPollCreationMessageV5(); p != nil {
		return p
	}
	if p := msg.GetPollCreationMessageV6(); p != nil {
		return p
	}
	return nil
}

func normalizedPayload(msg *waE2E.Message) (kind, text string, dl whatsmeow.DownloadableMessage, length uint64) {
	if msg == nil {
		return "other", "", nil, 0
	}
	if poll := getPollCreation(msg); poll != nil {
		return "poll", poll.GetName(), nil, 0
	}
	if msg.GetPollUpdateMessage() != nil {
		return "poll_vote", "", nil, 0
	}
	if msg.Conversation != nil {
		return "text", msg.GetConversation(), nil, 0
	}
	if content := msg.GetExtendedTextMessage(); content != nil {
		return "text", content.GetText(), nil, 0
	}
	if content := msg.GetReactionMessage(); content != nil {
		return "reaction", content.GetText(), nil, 0
	}
	if content := msg.GetImageMessage(); content != nil {
		return "image", content.GetCaption(), content, content.GetFileLength()
	}
	if content := msg.GetVideoMessage(); content != nil {
		return "video", content.GetCaption(), content, content.GetFileLength()
	}
	if content := msg.GetDocumentMessage(); content != nil {
		return "document", content.GetCaption(), content, content.GetFileLength()
	}
	if content := msg.GetAudioMessage(); content != nil {
		return "audio", "", content, content.GetFileLength()
	}
	if content := msg.GetStickerMessage(); content != nil {
		return "sticker", "", content, content.GetFileLength()
	}
	if protoMsg := msg.GetProtocolMessage(); protoMsg != nil {
		if protoMsg.GetType() == waE2E.ProtocolMessage_REVOKE {
			return "delete", "", nil, 0
		}
		if protoMsg.GetType() == waE2E.ProtocolMessage_MESSAGE_EDIT {
			if edited := protoMsg.GetEditedMessage(); edited != nil {
				_, t, d, l := normalizedPayload(edited)
				return "edit", t, d, l
			}
			return "edit", "", nil, 0
		}
	}
	if editedMsg := msg.GetEditedMessage(); editedMsg != nil {
		if inner := editedMsg.GetMessage(); inner != nil {
			_, t, d, l := normalizedPayload(inner)
			return "edit", t, d, l
		}
	}
	return "other", "", nil, 0
}
