package transport

import (
	"context"
	"io"
	"time"
)

type EndpointID string

type MessageRef struct {
	Endpoint        EndpointID
	RemoteMessageID string
}

type Sender struct {
	DisplayName string // transient only; never persist
	OpaqueID    string // HMAC-derived application identity
}

type Incoming struct {
	Endpoint  EndpointID
	RemoteID  string
	Sender    Sender
	Kind      string
	Text      string // transient only; never persist
	ReplyTo   *MessageRef
	Timestamp time.Time
	Media     io.ReadCloser
}

type Outgoing struct {
	Endpoint EndpointID
	Text     string
	ReplyTo  *MessageRef
	Media    io.Reader
	Kind     string
}

type Adapter interface {
	Name() string
	Events() <-chan Incoming
	Send(context.Context, Outgoing) (MessageRef, error)
	React(context.Context, MessageRef, string) error
	Edit(context.Context, MessageRef, string) error
	Delete(context.Context, MessageRef) error
	Close() error
}
