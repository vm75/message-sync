// Package privatedm owns the opt-in private relay outside canonical routing.
package privatedm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/controlstore"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/store"
	"github.com/vm75/message-sync/internal/transport"
)

const MaxBodyBytes = 1600

const Help = "message-sync private messaging\n/dm @{Amber Badger} <message> — send a private message\n/help — show this help\nTo continue a conversation, use Reply on the bot's relayed message.\nOrdinary messages without a valid reply do not get forwarded. Text only, at most 1600 UTF-8 bytes.\nDM edits and deletes are not synchronized; the recipient keeps the original relayed message.\nReplies expire after 24 hours. The relay may remove only its own local chat copies, never the recipient's messages. Discord and Telegram Bot API cannot remove bot-local copies separately, so their messages remain. Delivery requires the recipient to be reachable via this sync account; Telegram recipients must first /start this bot."

type Runtime interface {
	VerifyPrivateMember(context.Context, transport.EndpointID, string) bool
	VerifyPrivateInboxMember(context.Context, transport.EndpointID, string, transport.PrivateInbox) bool
	ResolvePrivateInbox(context.Context, transport.EndpointID, string) (transport.PrivateInbox, bool)
	SendPrivate(context.Context, transport.PrivateInbox, string) (transport.PrivateSendResult, error)
	DeletePrivateCopyForMe(context.Context, transport.PrivateCopy) error
}

type Resolver interface {
	ResolveParticipantReference(context.Context, string) (store.AliasRecord, error)
}

type window struct {
	minute int64
	count  int
}

// Relay is called by the application's single ingress worker. It never queues
// message content, retries provider creates, or calls group router operations.
type Relay struct {
	Sync       *store.Store
	State      *controlstore.PrivateDMStore
	Runtime    Runtime
	Resolver   Resolver
	LoadConfig func(context.Context) (*config.Config, error)
	Hasher     *identity.Hasher
	Now        func() time.Time
	limits     map[string]window
}

var command = regexp.MustCompile(`(?s)^/dm[ \t]+(` + identity.AliasTokenPattern + `)[ \t\r\n]+(.+)$`)
var references = regexp.MustCompile(`@\{[^{}]*\}`)
var nativeMentions = regexp.MustCompile(`<[@#][^>]*>|@[A-Za-z0-9_+.-]+`)
var identityAddresses = regexp.MustCompile(`(?i)\b[0-9]+@(lid|s\.whatsapp\.net|hosted\.lid)\b`)
var markdownLinks = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
var identityLinks = regexp.MustCompile(`(?i)(?:https?://(?:wa\.me/|(?:api\.)?whatsapp\.com/send[^\s]*|t\.me/|discord(?:app)?\.com/users/)[^\s]*|tg://user[^\s]*)`)

func sanitize(body string) string {
	body = identityAddresses.ReplaceAllString(body, "[identity]")
	body = identityLinks.ReplaceAllString(body, "[identity link]")
	body = references.ReplaceAllString(body, "[participant reference]")
	body = nativeMentions.ReplaceAllString(body, "[mention]")
	body = markdownLinks.ReplaceAllString(body, "$1")
	body = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || (!unicode.IsControl(r) && !(r >= '\u202a' && r <= '\u202e') && !(r >= '\u2066' && r <= '\u2069')) {
			return r
		}
		return -1
	}, body)
	return body
}

func (r *Relay) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *Relay) permit(key string, now time.Time, max int) bool {
	minute := now.Unix() / 60
	if r.limits == nil {
		r.limits = make(map[string]window)
	}
	for k, w := range r.limits {
		if w.minute != minute {
			delete(r.limits, k)
		}
	}
	w := r.limits[key]
	if w.minute != minute {
		w = window{minute: minute}
	}
	if w.count >= max || len(r.limits) >= 10000 {
		return false
	}
	w.count++
	r.limits[key] = w
	return true
}

func (r *Relay) Handle(ctx context.Context, in transport.PrivateIncoming) (handleErr error) {
	if in.FromSelf || (in.Kind != "text" && in.Kind != "unsupported") {
		return nil
	}
	if in.ConnectionID == "" || in.ConnectionID != in.Inbox.ConnectionID || in.Transport != in.Inbox.Transport || in.Inbox.OwnerID == "" || in.Inbox.ChatID == "" || len(in.Inbox.Token) == 0 || in.ActorID == "" || in.RemoteMessageID == "" {
		return nil
	}
	now := r.now()
	if !in.Timestamp.IsZero() && (now.Sub(in.Timestamp) >= controlstore.PrivateDMReplyTTL || in.Timestamp.After(now.Add(5*time.Minute))) {
		return nil
	}
	cfg, err := r.LoadConfig(ctx)
	if err != nil {
		return errors.New("load private DM configuration")
	}
	if !cfg.PrivateDMConnectionEnabled(in.ConnectionID) {
		return nil
	}
	if in.Transport == "whatsapp" && !strings.HasPrefix(strings.TrimSpace(in.Text), "/") {
		known := false
		if in.ReplyToMessageID != "" {
			previous, ok, lookupErr := r.State.Reply(ctx, in.Inbox, in.ReplyToMessageID, now)
			if lookupErr != nil {
				return lookupErr
			}
			known = ok && incomingActor(in, previous.Recipient.ActorID) && sameInbox(previous.Recipient.Inbox, in.Inbox)
		}
		if !known {
			for alias, ep := range cfg.Endpoints {
				if ep.ConnectionID != in.ConnectionID || string(ep.Transport) != in.Transport || !cfg.SettingsForEndpoint(alias).PrivateDMRelayEnabled {
					continue
				}
				if r.observedSourceActor(ctx, alias, in) != "" {
					known = true
					break
				}
			}
		}
		if !known {
			return nil
		} // unrelated personal chat, not a feature-owned copy
	}
	if !r.permit("actor:"+in.ConnectionID+":"+in.ActorID, now, 10) || !r.permit("global", now, 50) {
		return nil
	}
	accepted, err := r.State.AcceptIncoming(ctx, in.Inbox, in.RemoteMessageID, now)
	if err != nil || !accepted {
		return err
	}
	defer func() {
		if err := r.State.QueueCleanup(ctx, in.LocalCopy, now, now); err != nil && handleErr == nil {
			handleErr = err
		}
	}()
	// Only an observed group participant with a freshly verified native inbox
	// may leave an operational address for subsequent Telegram bot delivery.
	if in.Transport == "telegram" {
		for alias, ep := range cfg.Endpoints {
			if ep.ConnectionID != in.ConnectionID || string(ep.Transport) != in.Transport || !cfg.SettingsForEndpoint(alias).PrivateDMRelayEnabled {
				continue
			}
			if _, err := r.Sync.ParticipantByActor(ctx, alias, in.ActorID); err == nil && r.Runtime.VerifyPrivateInboxMember(ctx, transport.EndpointID(alias), in.ActorID, in.Inbox) {
				if err := r.State.ObserveInbox(ctx, in.ActorID, in.Inbox, now); err != nil {
					return err
				}
				break
			}
		}
	}
	text := strings.TrimSpace(in.Text)
	if in.Kind != "text" || !utf8.ValidString(text) || len(text) > 4096 || text == "/help" || (in.Transport == "telegram" && text == "/start") {
		return r.help(ctx, in.Inbox, "")
	}
	var mapping controlstore.PrivateDMMapping
	if text == "/dm" || strings.HasPrefix(text, "/dm ") || strings.HasPrefix(text, "/dm\t") || strings.HasPrefix(text, "/dm\n") {
		parts := command.FindStringSubmatch(text)
		if len(parts) != 4 || strings.TrimSpace(parts[3]) == "" || references.MatchString(parts[3]) {
			return r.help(ctx, in.Inbox, "Invalid /dm command.")
		}
		ref, lookupErr := r.Resolver.ResolveParticipantReference(ctx, parts[1])
		if lookupErr != nil {
			return r.aliasFailure(ctx, in.Inbox, lookupErr)
		}
		set, ok := findSetByID(cfg, ref.SyncSetID)
		if !ok || !cfg.SettingsForSet(set).PrivateDMRelayEnabled {
			return r.help(ctx, in.Inbox, "Delivery unavailable. Check the address and your shared sync set.")
		}
		aliases := append([]string(nil), set.Endpoints...)
		sort.Strings(aliases)
		source := ""
		sourceActor := ""
		for _, alias := range aliases {
			ep := cfg.Endpoints[alias]
			if ep.ConnectionID != in.ConnectionID || string(ep.Transport) != in.Transport {
				continue
			}
			if actor := r.observedSourceActor(ctx, alias, in); actor != "" {
				source = alias
				sourceActor = actor
				break
			}
		}
		if source == "" || incomingActor(in, ref.ActorID) {
			return r.help(ctx, in.Inbox, "Delivery unavailable. Check the address and your shared sync set.")
		}
		if _, err := r.Sync.AliasByActor(ctx, set.ID, sourceActor); err != nil {
			return r.aliasFailure(ctx, in.Inbox, err)
		}
		recipientEndpoint, inbox, ok, err := r.recipientRoute(ctx, cfg, ref, now)
		if err != nil {
			return err
		}
		if !ok || sameInbox(inbox, in.Inbox) {
			return r.help(ctx, in.Inbox, "Delivery unavailable. Check the address and your shared sync set.")
		}

		id := make([]byte, 16)
		if _, err := rand.Read(id); err != nil {
			return errors.New("generate private conversation ID")
		}
		mapping = controlstore.PrivateDMMapping{ConversationID: hex.EncodeToString(id), SyncSetID: set.ID, Author: controlstore.PrivateDMSide{Endpoint: source, ActorID: sourceActor, Inbox: in.Inbox}, Recipient: controlstore.PrivateDMSide{Endpoint: recipientEndpoint, ActorID: ref.ActorID, Inbox: inbox}}
		mapping.Author.Binding = r.endpointBinding(cfg.Endpoints[source])
		mapping.Recipient.Binding = r.endpointBinding(cfg.Endpoints[recipientEndpoint])
		text = parts[3]
	} else {
		if strings.HasPrefix(text, "/") || text == "" || in.ReplyToMessageID == "" {
			return r.help(ctx, in.Inbox, "")
		}
		previous, ok, err := r.State.Reply(ctx, in.Inbox, in.ReplyToMessageID, now)
		if err != nil {
			return err
		}
		if !ok || !incomingActor(in, previous.Recipient.ActorID) || !sameInbox(previous.Recipient.Inbox, in.Inbox) {
			return r.help(ctx, in.Inbox, "")
		}
		mapping = controlstore.PrivateDMMapping{ConversationID: previous.ConversationID, SyncSetID: previous.SyncSetID, Author: previous.Recipient, Recipient: previous.Author}
	}
	if len(text) > MaxBodyBytes || !r.authorized(ctx, cfg, mapping) {
		return r.help(ctx, in.Inbox, "Delivery unavailable, authorization expired, or message exceeds 1600 UTF-8 bytes.")
	}
	if !r.permit("set:"+mapping.SyncSetID, now, 100) {
		return r.help(ctx, in.Inbox, "Private messaging rate limit reached. Try again later.")
	}
	if !r.State.ReplyCapacity(ctx) {
		return r.help(ctx, in.Inbox, "Private messaging capacity reached. Try again later.")
	}
	body := sanitize(text)
	if len(body) > MaxBodyBytes {
		return r.help(ctx, in.Inbox, "Sanitized message exceeds 1600 UTF-8 bytes.")
	}
	if strings.TrimSpace(body) == "" {
		return r.help(ctx, in.Inbox, "")
	}
	alias, err := r.Sync.AliasByActor(ctx, mapping.SyncSetID, mapping.Author.ActorID)
	if err != nil {
		return r.aliasFailure(ctx, in.Inbox, err)
	}
	header := "DM from " + alias + "\n"
	result, err := r.Runtime.SendPrivate(ctx, mapping.Recipient.Inbox, header+body+"\nReply to this message to respond privately.")
	if err != nil {
		message := "Private delivery unavailable. The recipient must allow DMs and Telegram recipients must first /start this bot."
		failure := transport.Classify(err)
		if failure.Certainty == transport.SendUnknown && failure.Class == transport.FailureTransient {
			message = "Delivery could not be confirmed. Do not automatically resend."
		}
		return r.help(ctx, in.Inbox, message)
	}
	if result.RemoteMessageID == "" {
		return r.help(ctx, in.Inbox, "Delivery could not be confirmed. Do not automatically resend.")
	}
	sentAt := r.now()
	if err := r.State.PutReply(ctx, result.RemoteMessageID, mapping, sentAt); err != nil {
		_ = r.help(ctx, in.Inbox, "The provider accepted the message, but replies are unavailable.")
		return err
	}
	return r.State.QueueCleanup(ctx, result.LocalCopy, sentAt.Add(controlstore.PrivateDMReplyTTL), sentAt)
}

func sameInbox(a, b transport.PrivateInbox) bool {
	return a.ConnectionID == b.ConnectionID && a.Transport == b.Transport && a.OwnerID == b.OwnerID && a.ChatID != "" && a.ChatID == b.ChatID
}

func incomingActor(in transport.PrivateIncoming, actor string) bool {
	if in.ActorID == actor {
		return true
	}
	for i, alias := range in.ActorAliases {
		if i >= 4 {
			break
		}
		if alias == actor {
			return true
		}
	}
	return false
}

func (r *Relay) observedSourceActor(ctx context.Context, endpoint string, in transport.PrivateIncoming) string {
	actors := append([]string{in.ActorID}, in.ActorAliases...)
	if len(actors) > 5 {
		actors = actors[:5]
	}
	for _, actor := range actors {
		if _, err := r.Sync.ParticipantByActor(ctx, endpoint, actor); err == nil && r.Runtime.VerifyPrivateInboxMember(ctx, transport.EndpointID(endpoint), actor, in.Inbox) {
			return actor
		}
	}
	return ""
}

func findSet(cfg *config.Config, alias string) (config.SyncSet, bool) {
	for _, set := range cfg.SyncSets {
		for _, ep := range set.Endpoints {
			if ep == alias {
				return set, true
			}
		}
	}
	return config.SyncSet{}, false
}

func (r *Relay) endpointBinding(ep config.Endpoint) string {
	return r.Hasher.ActorID("private-dm-endpoint", string(ep.Transport)+"\x00"+ep.ConnectionID+"\x00"+ep.RemoteID)
}

func (r *Relay) authorized(ctx context.Context, cfg *config.Config, m controlstore.PrivateDMMapping) bool {
	if !cfg.PrivateDMRelayEnabled {
		return false
	}
	for _, side := range []controlstore.PrivateDMSide{m.Author, m.Recipient} {
		set, ok := findSet(cfg, side.Endpoint)
		ep, present := cfg.Endpoints[side.Endpoint]
		if !ok || !present || set.ID != m.SyncSetID || !cfg.SettingsForSet(set).PrivateDMRelayEnabled || ep.ConnectionID != side.Inbox.ConnectionID || string(ep.Transport) != side.Inbox.Transport || side.Binding != r.endpointBinding(ep) {
			return false
		}
		if _, err := r.Sync.ParticipantByActor(ctx, side.Endpoint, side.ActorID); err != nil {
			return false
		}
		if !r.Runtime.VerifyPrivateInboxMember(ctx, transport.EndpointID(side.Endpoint), side.ActorID, side.Inbox) {
			return false
		}
	}
	return true
}

func (r *Relay) help(ctx context.Context, inbox transport.PrivateInbox, prefix string) error {
	text := Help
	if prefix != "" {
		text = prefix + "\n\n" + text
	}
	result, err := r.Runtime.SendPrivate(ctx, inbox, text)
	if err != nil {
		return errors.New("send private DM help")
	}
	return r.State.QueueCleanup(ctx, result.LocalCopy, r.now(), r.now())
}

func (r *Relay) Sweep(ctx context.Context) error {
	now := r.now()
	if err := r.State.Prune(ctx, now, 100); err != nil {
		return err
	}
	candidates, err := r.State.DueCleanup(ctx, now, 100)
	if err != nil {
		return err
	}
	failed := false
	for _, candidate := range candidates {
		// The queue accepts only WhatsApp local-copy tokens. There is no group
		// deletion, Discord message deletion, or Telegram deleteMessage fallback.
		if candidate.Copy.Transport != "whatsapp" {
			return errors.New("invalid private cleanup transport")
		}
		claimed, err := r.State.BeginCleanup(ctx, candidate, now)
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
		err = r.Runtime.DeletePrivateCopyForMe(ctx, candidate.Copy)
		failed = failed || err != nil
		if finishErr := r.State.FinishCleanup(ctx, candidate, err == nil); finishErr != nil {
			return finishErr
		}
	}
	if failed {
		return errors.New("private local cleanup unavailable or failed")
	}
	return nil
}
