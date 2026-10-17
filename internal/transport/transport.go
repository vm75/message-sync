package transport

import (
	"context"
	"time"
)

type EndpointID string

// Checkpoint identifies an ordered provider stream without carrying provider
// payloads or identities. Position is meaningful only within StreamKey.
type Checkpoint struct {
	StreamKey      string
	Position       int64
	EventTimestamp time.Time
	Valid          bool
}

// RecoveryRequest contains fixed, coordinator-owned recovery bounds.
type RecoveryRequest struct {
	Cursor    Checkpoint
	MaxEvents int
	MaxAge    time.Duration
}

// RecoverySource is an optional adapter capability. Adapters normalize
// recovered provider items before emitting them through the supplied callback.
type RecoverySource interface {
	RecoveryStreams() []string
	Recover(context.Context, RecoveryRequest, func(context.Context, Incoming) error) error
	RecoverySignals() <-chan struct{}
}

type MessageRef struct {
	Endpoint          EndpointID
	RemoteMessageID   string
	IsTargetFromMe    bool
	Provider          string
	ProviderReference string
	ChildScope        *ChildScope
}

// ChildScope identifies a provider-native conversation below a configured
// endpoint (for example a Discord thread or Telegram topic). Scope IDs are
// opaque operational values; labels are transient presentation data.
type ScopeKind string

const (
	ScopeKindDiscordThread ScopeKind = "discord_thread"
	ScopeKindTelegramTopic ScopeKind = "telegram_topic"
)

type ChildScope struct {
	Kind     ScopeKind
	RemoteID string
	Label    string
}

// ChildScopeLabelObserver receives transient provider metadata for optional
// presentation persistence. It must never be used for routing or identity.
type ChildScopeLabelObserver func(context.Context, EndpointID, ChildScope)

type Sender struct {
	Discriminator string // derived presentation suffix; empty when hidden
	DisplayName   string // transient only; never persist
	PhoneNumber   string // transient only; never persist
	OpaqueID      string // HMAC-derived application identity
}

// NameWithDiscriminator formats a presentation name without changing identity.
func (s Sender) NameWithDiscriminator(name string) string {
	if name == "" || s.Discriminator == "" {
		return name
	}
	return name + " " + s.Discriminator
}

type Mention struct {
	RemoteID string
	Name     string
}

// CrossReference is a resolved bridge-only address. ActorID is a HMAC actor
// identity; no native provider ID is persisted or passed through the router.
// Each destination independently verifies membership before notifying.
type CrossReference struct {
	Token     string
	Endpoint  EndpointID
	ActorID   string
	Handle    string // stable endpoint-local handle, e.g. P1
	Name      string // transient observed display name; never persist
	Pseudonym string // derived from the HMAC actor ID; no provider identity
}

// ParticipantResolver resolves a name in one configured source group against
// already registered HMAC actors. Provider IDs and names remain adapter-owned
// transient data; the returned identity must be one of the supplied actors.
type ParticipantResolver interface {
	ResolveParticipant(context.Context, EndpointID, string, []string) (string, bool)
}

type Incoming struct {
	Endpoint              EndpointID
	Checkpoint            Checkpoint
	RemoteID              string
	Sender                Sender
	FromSelf              bool
	Kind                  string
	Text                  string // transient only; never persist
	Mentions              []Mention
	ReplyTo               *MessageRef
	ChildScope            *ChildScope
	QuotedText            string
	Timestamp             time.Time
	MediaLoader           func(context.Context) ([]byte, error)
	PollOptions           []string // transient only; never persist
	PollSelectableCount   int
	PollDurationHours     int
	PollOptionHashes      []string // SHA-256 hex hashes of selected options for poll_vote
	PollOptionIndexes     []int    // canonical zero-based selected option indexes for poll_vote
	PollSnapshot          map[int]int
	PollProvider          string
	PollProviderReference string
	PollSourceUnavailable bool
}

type Outgoing struct {
	Endpoint       EndpointID
	OriginEndpoint EndpointID
	Sender         Sender // transient only; never persist
	// SenderLabel is the complete transient source/sender attribution used by
	// presentation-capable transports. It must never be persisted or parsed for
	// routing.
	SenderLabel     string
	SourceText      string // transient un-attributed source text; never persist
	AttributionOnly bool   // protocol compatibility companion; never canonical
	ReplyFallback   bool   // source was a reply but no destination copy exists
	Text            string
	// RenderedText is the complete client-facing attribution/body rendering for
	// optional friendly presentation. It is transient and must never be parsed
	// for routing.
	RenderedText string
	// PollAttribution is the transient friendly attribution shown alongside a
	// native poll. It is separate from RenderedText because poll providers keep
	// the question/options as structured fields.
	PollAttribution     string
	Mentions            []Mention
	References          []CrossReference
	ReferenceAnonymized bool // suppress display-name references on anonymized routes
	ReplyTo             *MessageRef
	ChildScope          *ChildScope
	QuotedText          string
	MediaBytes          []byte
	Kind                string
	PollOptions         []string
	PollSelectableCount int
	PollDurationHours   int
}

type Reaction struct {
	Endpoint       EndpointID
	TargetRemoteID string
	IsTargetFromMe bool
	ChildScope     *ChildScope
	Emoji          string
	FallbackText   string
}

type Adapter interface {
	Name() string
	Events() <-chan Incoming
	Send(context.Context, Outgoing) (MessageRef, error)
	React(context.Context, Reaction) error
	Edit(context.Context, MessageRef, string) error
	Delete(context.Context, MessageRef) error
	Close() error
}

// PrivateInbox is an opaque, transient provider address for one private chat.
// Token may contain provider-native IDs and must be encrypted before storage,
// and must never be logged or returned through the API.
type PrivateInbox struct {
	ConnectionID string
	Transport    string
	OwnerID      string // HMAC proof of the authenticated account behind this connection
	ChatID       string // sensitive canonical private-chat identity, encrypted before storage
	Token        []byte
}

// PrivateCopy identifies one message in the sync account's own private-chat
// view. Its token may contain provider-native IDs and is sensitive.
type PrivateCopy struct {
	ConnectionID string
	Transport    string
	OwnerID      string
	Token        []byte
}

type PrivateSendResult struct {
	RemoteMessageID string
	LocalCopy       PrivateCopy
}

// PrivateIncoming is deliberately separate from Incoming: it bypasses the
// canonical group router and carries sensitive provider-private addresses
// only in memory. Text must never be persisted. Native IDs may be stored only
// as encrypted operational tokens or keyed digests, and must never be logged.
type PrivateIncoming struct {
	ConnectionID     string
	Transport        string
	Inbox            PrivateInbox
	LocalCopy        PrivateCopy
	ActorID          string   // HMAC actor identity, never the provider-native ID
	ActorAliases     []string // authenticated PN/LID-equivalent HMAC identities, transient only
	RemoteMessageID  string   // provider-private message ID, transient only
	ReplyToMessageID string   // structured native reply ID, never inferred from text
	Text             string   // transient only
	Kind             string   // text or unsupported
	Timestamp        time.Time
	FromSelf         bool
}

type PrivateEventsSource interface {
	PrivateEvents() <-chan PrivateIncoming
}

// PrivateMessenger is an optional adapter capability for direct, non-routed
// messages. Implementations must verify endpoint membership on each call.
type PrivateMessenger interface {
	VerifyPrivateMember(context.Context, EndpointID, string) bool
	VerifyPrivateInboxMember(context.Context, EndpointID, string, PrivateInbox) bool
	ResolvePrivateInbox(context.Context, EndpointID, string) (PrivateInbox, bool)
	SendPrivate(context.Context, PrivateInbox, string) (PrivateSendResult, error)
	DeletePrivateCopyForMe(context.Context, PrivateCopy) error
}

// RecoveryPolicyProvider supplies the policy owned by a recovery stream's sync set.
type RecoveryPolicyProvider interface {
	RecoveryPolicy(string) (bool, int, time.Duration)
}
