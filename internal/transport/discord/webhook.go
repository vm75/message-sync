package discord

import (
	"context"
)

// WebhookMessage is transient outbound rendering data. Username and Content may
// contain source-platform user/message data and must never be persisted or logged.
type WebhookMessage struct {
	Username string
	Content  string
}

// ChannelWebhook is the narrow boundary for a bridge-managed per-channel
// Discord webhook. Implementations own webhook discovery/credentials outside
// canonical routing state and return only the created remote Discord message ID.
//
// This ticket intentionally does not implement outbound delivery; the interface
// exists so later Send/Edit/Delete work can use webhook credentials without
// exposing them to the router or sync.db.
type ChannelWebhook interface {
	ManagedWebhookChecker
	Execute(context.Context, string, WebhookMessage) (string, error)
}
