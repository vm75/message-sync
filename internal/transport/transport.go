package transport

import (
	"context"
	"time"
)

type EndpointID string

type MessageRef struct {
	Endpoint        EndpointID
	RemoteMessageID string
	IsTargetFromMe  bool
}

type Sender struct {
	DisplayName string // transient only; never persist
	PhoneNumber string // transient only; never persist
	OpaqueID    string // HMAC-derived application identity
}

type Mention struct {
	RemoteID string
	Name     string
}

type Incoming struct {
	Endpoint            EndpointID
	RemoteID            string
	Sender              Sender
	FromSelf            bool
	Kind                string
	Text                string // transient only; never persist
	Mentions            []Mention
	ReplyTo             *MessageRef
	QuotedText          string
	Timestamp           time.Time
	MediaLoader         func(context.Context) ([]byte, error)
	PollOptions         []string // transient only; never persist
	PollSelectableCount int
	PollOptionHashes    []string // SHA-256 hex hashes of selected options for poll_vote
}

type Outgoing struct {
	Endpoint            EndpointID
	OriginEndpoint      EndpointID
	Text                string
	Mentions            []Mention
	ReplyTo             *MessageRef
	QuotedText          string
	MediaBytes          []byte
	Kind                string
	PollOptions         []string
	PollSelectableCount int
}

type Reaction struct {
	Endpoint       EndpointID
	TargetRemoteID string
	IsTargetFromMe bool
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
