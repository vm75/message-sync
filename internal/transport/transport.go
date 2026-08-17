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
	OpaqueID    string // HMAC-derived application identity
}

type Incoming struct {
	Endpoint    EndpointID
	RemoteID    string
	Sender      Sender
	FromSelf    bool
	Kind        string
	Text        string // transient only; never persist
	ReplyTo     *MessageRef
	QuotedText  string
	Timestamp   time.Time
	MediaLoader func(context.Context) ([]byte, error)
}

type Outgoing struct {
	Endpoint   EndpointID
	Text       string
	ReplyTo    *MessageRef
	QuotedText string
	MediaBytes []byte
	Kind       string
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
