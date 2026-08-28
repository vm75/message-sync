package discord

import "context"

// WebhookFile is transient outbound media. Names are bridge-generated safe
// placeholders; source filenames and URLs must never be copied here.
type WebhookFile struct {
	Name        string
	ContentType string
	Data        []byte
}

// WebhookMessage is transient outbound rendering data. Username and Content may
// contain source-platform user/message data and must never be persisted or logged.
type WebhookMessage struct {
	Username string
	Content  string
	File     *WebhookFile
}

// ChannelWebhook is the narrow boundary for one bridge-managed webhook per
// configured Discord channel. Implementations own webhook IDs/tokens only in
// memory and return only created remote Discord message IDs.
type ChannelWebhook interface {
	ManagedWebhookChecker
	Execute(context.Context, string, WebhookMessage) (string, error)
	Edit(context.Context, string, string, string) error
	Delete(context.Context, string, string) error
}

type webhookPreparer interface {
	Prepare(context.Context, []string) error
}
