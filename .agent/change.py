from pathlib import Path


def replace_once(path, old, new):
    p = Path(path)
    text = p.read_text()
    if old not in text:
        raise SystemExit(f"pattern not found in {path}: {old[:160]!r}")
    p.write_text(text.replace(old, new, 1))


# --- control-store integration modes -------------------------------------------------
replace_once(
    "internal/controlstore/connection.go",
    '''const (\n\tTelegramIntegrationModeBot     = "bot"\n\tTelegramIntegrationModeMTProto = "mtproto"\n)\n\nfunc NormalizeIntegrationMode(transportName, mode string) string {\n\ttransportName = strings.ToLower(strings.TrimSpace(transportName))\n\tmode = strings.ToLower(strings.TrimSpace(mode))\n\tif transportName == "telegram" && mode == "" {\n\t\treturn TelegramIntegrationModeBot\n\t}\n\tif transportName != "telegram" {\n\t\treturn ""\n\t}\n\treturn mode\n}\n\nfunc ValidateIntegrationMode(transportName, mode string) error {\n\ttransportName = strings.ToLower(strings.TrimSpace(transportName))\n\tif transportName != "telegram" {\n\t\tif strings.TrimSpace(mode) != "" {\n\t\t\treturn fmt.Errorf("integration mode is only supported for telegram connections")\n\t\t}\n\t\treturn nil\n\t}\n\tswitch NormalizeIntegrationMode(transportName, mode) {\n\tcase TelegramIntegrationModeBot, TelegramIntegrationModeMTProto:\n\t\treturn nil\n\tdefault:\n\t\treturn fmt.Errorf("invalid telegram integration mode %q: must be bot or mtproto", strings.TrimSpace(mode))\n\t}\n}\n''',
    '''const (\n\tTelegramIntegrationModeBot     = "bot"\n\tTelegramIntegrationModeMTProto = "mtproto"\n\tDiscordIntegrationModeManaged  = "managed"\n\tDiscordIntegrationModeWebhook  = "webhook"\n)\n\nfunc NormalizeIntegrationMode(transportName, mode string) string {\n\ttransportName = strings.ToLower(strings.TrimSpace(transportName))\n\tmode = strings.ToLower(strings.TrimSpace(mode))\n\tswitch transportName {\n\tcase "telegram":\n\t\tif mode == "" {\n\t\t\treturn TelegramIntegrationModeBot\n\t\t}\n\t\treturn mode\n\tcase "discord":\n\t\tif mode == "" {\n\t\t\treturn DiscordIntegrationModeManaged\n\t\t}\n\t\treturn mode\n\tdefault:\n\t\treturn ""\n\t}\n}\n\nfunc ValidateIntegrationMode(transportName, mode string) error {\n\ttransportName = strings.ToLower(strings.TrimSpace(transportName))\n\tswitch transportName {\n\tcase "telegram":\n\t\tswitch NormalizeIntegrationMode(transportName, mode) {\n\t\tcase TelegramIntegrationModeBot, TelegramIntegrationModeMTProto:\n\t\t\treturn nil\n\t\tdefault:\n\t\t\treturn fmt.Errorf("invalid telegram integration mode %q: must be bot or mtproto", strings.TrimSpace(mode))\n\t\t}\n\tcase "discord":\n\t\tswitch NormalizeIntegrationMode(transportName, mode) {\n\t\tcase DiscordIntegrationModeManaged, DiscordIntegrationModeWebhook:\n\t\t\treturn nil\n\t\tdefault:\n\t\t\treturn fmt.Errorf("invalid discord integration mode %q: must be managed or webhook", strings.TrimSpace(mode))\n\t\t}\n\tdefault:\n\t\tif strings.TrimSpace(mode) != "" {\n\t\t\treturn fmt.Errorf("integration mode is only supported for discord and telegram connections")\n\t\t}\n\t\treturn nil\n\t}\n}\n''',
)

replace_once(
    "internal/controlstore/connection_mode_test.go",
    '''\t\t{"discord", "", false},\n\t\t{"discord", "bot", true},\n''',
    '''\t\t{"discord", "", false},\n\t\t{"discord", "managed", false},\n\t\t{"discord", "webhook", false},\n\t\t{"discord", "bot", true},\n''',
)

# --- encrypted Discord credential format ---------------------------------------------
Path("internal/transport/discord/credential.go").write_text(r'''package discord

import (
    "encoding/json"
    "errors"
    "net/url"
    "strings"
)

const storedCredentialVersion = 1

type StoredCredential struct {
    Version    int    `json:"version"`
    BotToken   string `json:"botToken"`
    WebhookURL string `json:"webhookUrl,omitempty"`
    ChannelID  string `json:"channelId,omitempty"`
}

func EncodeStoredCredential(mode, botToken, webhookURL, channelID string) ([]byte, error) {
    mode = strings.ToLower(strings.TrimSpace(mode))
    botToken = strings.TrimSpace(botToken)
    webhookURL = strings.TrimSpace(webhookURL)
    channelID = strings.TrimSpace(channelID)
    if botToken == "" {
        return nil, errors.New("Discord bot token is required")
    }
    if mode == "" || mode == "managed" {
        if webhookURL != "" || channelID != "" {
            return nil, errors.New("managed Discord webhook mode does not accept an explicit webhook URL or channel ID")
        }
        // Preserve the legacy on-disk representation for existing/default managed-webhook connections.
        return []byte(botToken), nil
    }
    if mode != "webhook" {
        return nil, errors.New("unsupported Discord integration mode")
    }
    if channelID == "" {
        return nil, errors.New("Discord explicit webhook mode requires a channel ID")
    }
    if _, _, err := ParseWebhookURL(webhookURL); err != nil {
        return nil, err
    }
    return json.Marshal(StoredCredential{
        Version: storedCredentialVersion,
        BotToken: botToken,
        WebhookURL: webhookURL,
        ChannelID: channelID,
    })
}

func DecodeStoredCredential(mode string, raw []byte) (StoredCredential, error) {
    mode = strings.ToLower(strings.TrimSpace(mode))
    if mode == "" || mode == "managed" {
        token := strings.TrimSpace(string(raw))
        if token == "" {
            return StoredCredential{}, errors.New("Discord bot token is required")
        }
        return StoredCredential{Version: storedCredentialVersion, BotToken: token}, nil
    }
    if mode != "webhook" {
        return StoredCredential{}, errors.New("unsupported Discord integration mode")
    }
    var credential StoredCredential
    if err := json.Unmarshal(raw, &credential); err != nil {
        return StoredCredential{}, errors.New("decode Discord webhook credential")
    }
    credential.BotToken = strings.TrimSpace(credential.BotToken)
    credential.WebhookURL = strings.TrimSpace(credential.WebhookURL)
    credential.ChannelID = strings.TrimSpace(credential.ChannelID)
    if credential.BotToken == "" || credential.ChannelID == "" {
        return StoredCredential{}, errors.New("Discord webhook credential is incomplete")
    }
    if _, _, err := ParseWebhookURL(credential.WebhookURL); err != nil {
        return StoredCredential{}, err
    }
    return credential, nil
}

func ParseWebhookURL(raw string) (string, string, error) {
    u, err := url.Parse(strings.TrimSpace(raw))
    if err != nil || u == nil {
        return "", "", errors.New("invalid Discord webhook URL")
    }
    if !strings.EqualFold(u.Scheme, "https") {
        return "", "", errors.New("Discord webhook URL must use HTTPS")
    }
    host := strings.ToLower(strings.TrimSpace(u.Hostname()))
    switch host {
    case "discord.com", "www.discord.com", "discordapp.com", "www.discordapp.com", "canary.discord.com", "ptb.discord.com":
    default:
        return "", "", errors.New("Discord webhook URL must use an official Discord host")
    }
    parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
    for i, part := range parts {
        if part != "webhooks" || i+2 >= len(parts) {
            continue
        }
        id, idErr := url.PathUnescape(parts[i+1])
        token, tokenErr := url.PathUnescape(parts[i+2])
        id = strings.TrimSpace(id)
        token = strings.TrimSpace(token)
        if idErr == nil && tokenErr == nil && id != "" && token != "" {
            return id, token, nil
        }
    }
    return "", "", errors.New("invalid Discord webhook URL")
}
''')

Path("internal/transport/discord/credential_test.go").write_text(r'''package discord

import "testing"

func TestStoredCredentialManagedAndExplicitWebhook(t *testing.T) {
    managed, err := EncodeStoredCredential("managed", "bot-secret", "", "")
    if err != nil {
        t.Fatal(err)
    }
    decoded, err := DecodeStoredCredential("managed", managed)
    if err != nil {
        t.Fatal(err)
    }
    if decoded.BotToken != "bot-secret" || decoded.WebhookURL != "" || decoded.ChannelID != "" {
        t.Fatalf("unexpected managed credential: %#v", decoded)
    }

    webhookURL := "https://discord.com/api/webhooks/123456789/token-value"
    raw, err := EncodeStoredCredential("webhook", "bot-secret", webhookURL, "987654321")
    if err != nil {
        t.Fatal(err)
    }
    decoded, err = DecodeStoredCredential("webhook", raw)
    if err != nil {
        t.Fatal(err)
    }
    if decoded.BotToken != "bot-secret" || decoded.WebhookURL != webhookURL || decoded.ChannelID != "987654321" {
        t.Fatalf("unexpected webhook credential: %#v", decoded)
    }
}

func TestParseWebhookURLRejectsNonDiscordHost(t *testing.T) {
    if _, _, err := ParseWebhookURL("https://example.com/api/webhooks/123/token"); err == nil {
        t.Fatal("expected non-Discord webhook host to be rejected")
    }
}
''')

# --- explicit webhook client ----------------------------------------------------------
Path("internal/transport/discord/explicit_webhook.go").write_text(r'''package discord

import (
    "bytes"
    "context"
    "encoding/json"
    "errors"
    "net/http"
    "net/url"
    "strings"

    "github.com/bwmarrin/discordgo"
)

type explicitWebhookClient struct {
    api webhookAPI
    channelID string
    id string
    token string
}

func newExplicitWebhookClient(api webhookAPI, channelID, webhookURL string) (*explicitWebhookClient, error) {
    if api == nil {
        return nil, errors.New("Discord webhook API is required")
    }
    channelID = strings.TrimSpace(channelID)
    if channelID == "" {
        return nil, errors.New("Discord explicit webhook channel is required")
    }
    id, token, err := ParseWebhookURL(webhookURL)
    if err != nil {
        return nil, err
    }
    return &explicitWebhookClient{api: api, channelID: channelID, id: id, token: token}, nil
}

func (c *explicitWebhookClient) Prepare(_ context.Context, channelIDs []string) error {
    for _, channelID := range channelIDs {
        if strings.TrimSpace(channelID) != c.channelID {
            return errors.New("Discord explicit webhook connection is bound to one configured channel")
        }
    }
    return nil
}

func (c *explicitWebhookClient) Readiness(channelID string) WebhookStatus {
    if c == nil || strings.TrimSpace(channelID) != c.channelID {
        return WebhookStatusUnavailable
    }
    return WebhookStatusReady
}

func (c *explicitWebhookClient) IsManagedWebhook(channelID, webhookID string) bool {
    return c != nil && strings.TrimSpace(channelID) == c.channelID && strings.TrimSpace(webhookID) == c.id
}

func (c *explicitWebhookClient) Execute(ctx context.Context, channelID string, message WebhookMessage) (string, error) {
    if err := c.requireChannel(channelID); err != nil {
        return "", err
    }
    created, err := c.api.WebhookExecute(c.id, c.token, true, explicitWebhookParams(message), discordgo.WithContext(ctx), discordgo.WithRetryOnRatelimit(true))
    if err != nil {
        return "", classifyDiscordFailure(err)
    }
    if created == nil || strings.TrimSpace(created.ID) == "" {
        return "", errors.New("Discord explicit webhook did not return a message id")
    }
    return strings.TrimSpace(created.ID), nil
}

func (c *explicitWebhookClient) ExecuteInThread(ctx context.Context, channelID, threadID string, message WebhookMessage) (string, error) {
    if err := c.requireChannel(channelID); err != nil {
        return "", err
    }
    threadID = strings.TrimSpace(threadID)
    if threadID == "" {
        return "", errors.New("Discord explicit webhook thread is required")
    }
    params := explicitWebhookParams(message)
    if executor, ok := c.api.(interface {
        WebhookThreadExecute(string, string, bool, string, *discordgo.WebhookParams, ...discordgo.RequestOption) (*discordgo.Message, error)
    }); ok {
        created, err := executor.WebhookThreadExecute(c.id, c.token, true, threadID, params, discordgo.WithContext(ctx), discordgo.WithRetryOnRatelimit(true))
        if err != nil {
            return "", classifyDiscordFailure(err)
        }
        if created == nil || strings.TrimSpace(created.ID) == "" {
            return "", errors.New("Discord threaded explicit webhook returned an incomplete message")
        }
        return strings.TrimSpace(created.ID), nil
    }
    requester, ok := c.api.(interface {
        RequestWithBucketID(string, string, interface{}, string, ...discordgo.RequestOption) ([]byte, error)
    })
    if !ok {
        return "", errors.New("Discord client does not support threaded webhook execution")
    }
    endpoint := discordgo.EndpointWebhookToken(c.id, c.token) + "?wait=true&thread_id=" + url.QueryEscape(threadID)
    response, err := requester.RequestWithBucketID(http.MethodPost, endpoint, params, endpoint, discordgo.WithContext(ctx), discordgo.WithRetryOnRatelimit(true))
    if err != nil {
        return "", classifyDiscordFailure(err)
    }
    var created discordgo.Message
    if err := json.Unmarshal(response, &created); err != nil || strings.TrimSpace(created.ID) == "" {
        return "", errors.New("Discord threaded explicit webhook returned an incomplete message")
    }
    return strings.TrimSpace(created.ID), nil
}

func (c *explicitWebhookClient) Edit(ctx context.Context, channelID, messageID, content string) error {
    if err := c.requireChannel(channelID); err != nil {
        return err
    }
    messageID = strings.TrimSpace(messageID)
    if messageID == "" {
        return errors.New("Discord message id is required")
    }
    _, err := c.api.WebhookMessageEdit(c.id, c.token, messageID, &discordgo.WebhookEdit{Content: &content, AllowedMentions: &discordgo.MessageAllowedMentions{}}, discordgo.WithContext(ctx), discordgo.WithRetryOnRatelimit(true))
    if isDiscordUnknownMessage(err) {
        return nil
    }
    if err != nil {
        return classifyDiscordFailure(err)
    }
    return nil
}

func (c *explicitWebhookClient) Delete(ctx context.Context, channelID, messageID string) error {
    if err := c.requireChannel(channelID); err != nil {
        return err
    }
    messageID = strings.TrimSpace(messageID)
    if messageID == "" {
        return errors.New("Discord message id is required")
    }
    err := c.api.WebhookMessageDelete(c.id, c.token, messageID, discordgo.WithContext(ctx), discordgo.WithRetryOnRatelimit(true))
    if isDiscordUnknownMessage(err) {
        return nil
    }
    if err != nil {
        return classifyDiscordFailure(err)
    }
    return nil
}

func (c *explicitWebhookClient) EditInThread(ctx context.Context, channelID, threadID, messageID, content string) error {
    return c.threadMessageRequest(ctx, http.MethodPatch, channelID, threadID, messageID, &discordgo.WebhookEdit{Content: &content, AllowedMentions: &discordgo.MessageAllowedMentions{}})
}

func (c *explicitWebhookClient) DeleteInThread(ctx context.Context, channelID, threadID, messageID string) error {
    return c.threadMessageRequest(ctx, http.MethodDelete, channelID, threadID, messageID, nil)
}

func (c *explicitWebhookClient) threadMessageRequest(ctx context.Context, method, channelID, threadID, messageID string, payload *discordgo.WebhookEdit) error {
    if err := c.requireChannel(channelID); err != nil {
        return err
    }
    threadID = strings.TrimSpace(threadID)
    messageID = strings.TrimSpace(messageID)
    if threadID == "" || messageID == "" {
        return errors.New("Discord explicit webhook thread message is required")
    }
    requester, ok := c.api.(interface {
        RequestWithBucketID(string, string, interface{}, string, ...discordgo.RequestOption) ([]byte, error)
    })
    if !ok {
        return errors.New("Discord client does not support threaded webhook lifecycle")
    }
    endpoint := discordgo.EndpointWebhookToken(c.id, c.token) + "/messages/" + url.PathEscape(messageID) + "?thread_id=" + url.QueryEscape(threadID)
    var body interface{}
    if payload != nil {
        body = payload
    }
    _, err := requester.RequestWithBucketID(method, endpoint, body, endpoint, discordgo.WithContext(ctx), discordgo.WithRetryOnRatelimit(true))
    if isDiscordUnknownMessage(err) {
        return nil
    }
    if err != nil {
        return classifyDiscordFailure(err)
    }
    return nil
}

func (c *explicitWebhookClient) requireChannel(channelID string) error {
    if c == nil || strings.TrimSpace(channelID) != c.channelID {
        return errors.New("Discord explicit webhook is not configured for this channel")
    }
    return nil
}

func explicitWebhookParams(message WebhookMessage) *discordgo.WebhookParams {
    params := &discordgo.WebhookParams{Content: message.Content, Username: message.Username, AllowedMentions: &discordgo.MessageAllowedMentions{}}
    if message.File != nil {
        params.Files = []*discordgo.File{{Name: message.File.Name, ContentType: message.File.ContentType, Reader: bytes.NewReader(message.File.Data)}}
    }
    return params
}
''')

# --- Discord adapter wiring -----------------------------------------------------------
replace_once("internal/transport/discord/adapter.go", "\tWebhook       ChannelWebhook\n\tMediaEnabled  bool\n", "\tWebhook                  ChannelWebhook\n\tExplicitWebhookURL       string\n\tExplicitWebhookChannelID string\n\tMediaEnabled             bool\n")
replace_once("internal/transport/discord/adapter.go", "\twebhook      ChannelWebhook\n\ttargets      map[transport.EndpointID]string\n", "\twebhook               ChannelWebhook\n\tfixedWebhookChannelID string\n\ttargets               map[transport.EndpointID]string\n")
replace_once(
    "internal/transport/discord/adapter.go",
    '''\tif err := config.ValidateConnectionID(opts.ConnectionID); err != nil {\n\t\treturn nil, err\n\t}\n\tselect {\n''',
    '''\tif err := config.ValidateConnectionID(opts.ConnectionID); err != nil {\n\t\treturn nil, err\n\t}\n\texplicitWebhookURL := strings.TrimSpace(opts.ExplicitWebhookURL)\n\texplicitWebhookChannelID := strings.TrimSpace(opts.ExplicitWebhookChannelID)\n\tif (explicitWebhookURL == "") != (explicitWebhookChannelID == "") {\n\t\treturn nil, errors.New("Discord explicit webhook URL and channel ID must be configured together")\n\t}\n\tif explicitWebhookChannelID != "" {\n\t\tfor _, channelID := range opts.ChannelIDs {\n\t\t\tif strings.TrimSpace(channelID) != explicitWebhookChannelID {\n\t\t\t\treturn nil, errors.New("Discord explicit webhook connection is bound to its configured channel ID")\n\t\t\t}\n\t\t}\n\t}\n\tselect {\n''',
)
replace_once(
    "internal/transport/discord/adapter.go",
    '''\twebhook := opts.Webhook\n\tif webhook == nil {\n\t\twebhook = newManagedWebhookClient(session)\n\t}\n\tadapter := &Adapter{\n''',
    '''\twebhook := opts.Webhook\n\tif webhook == nil {\n\t\tif explicitWebhookURL != "" {\n\t\t\twebhook, err = newExplicitWebhookClient(session, explicitWebhookChannelID, explicitWebhookURL)\n\t\t\tif err != nil {\n\t\t\t\t_ = session.Close()\n\t\t\t\treturn nil, err\n\t\t\t}\n\t\t} else {\n\t\t\twebhook = newManagedWebhookClient(session)\n\t\t}\n\t}\n\tadapter := &Adapter{\n''',
)
replace_once("internal/transport/discord/adapter.go", "\t\twebhook:           webhook,\n\t\ttargets:           targets,\n", "\t\twebhook:               webhook,\n\t\tfixedWebhookChannelID: explicitWebhookChannelID,\n\t\ttargets:               targets,\n")
replace_once(
    "internal/transport/discord/adapter.go",
    '''\tfor alias, endpoint := range cfg.Endpoints {\n\t\tif endpoint.Transport == config.TransportDiscord {\n\t\t\tif endpoint.ConnectionID != a.connectionID {\n\t\t\t\tcontinue\n\t\t\t}\n\t\t\tchannelIDs[alias] = endpoint.RemoteID\n\t\t}\n\t}\n\tnormalizer, err := NewNormalizer(channelIDs, a.hasher, cfg.Identity.UsernameMode)\n''',
    '''\tfor alias, endpoint := range cfg.Endpoints {\n\t\tif endpoint.Transport == config.TransportDiscord {\n\t\t\tif endpoint.ConnectionID != a.connectionID {\n\t\t\t\tcontinue\n\t\t\t}\n\t\t\tchannelIDs[alias] = endpoint.RemoteID\n\t\t}\n\t}\n\ta.mu.RLock()\n\tfixedWebhookChannelID := a.fixedWebhookChannelID\n\ta.mu.RUnlock()\n\tif fixedWebhookChannelID != "" {\n\t\tfor _, channelID := range channelIDs {\n\t\t\tif strings.TrimSpace(channelID) != fixedWebhookChannelID {\n\t\t\t\treturn errors.New("Discord explicit webhook connection is bound to its configured channel ID")\n\t\t\t}\n\t\t}\n\t}\n\tnormalizer, err := NewNormalizer(channelIDs, a.hasher, cfg.Identity.UsernameMode)\n''',
)

# Limit discovery for explicit-webhook connections to their configured channel.
replace_once("internal/transport/discord/admin.go", "\tconnected := a.connected\n\tapi := a.adminAPI\n\ta.mu.RUnlock()\n", "\tconnected := a.connected\n\tapi := a.adminAPI\n\tfixedWebhookChannelID := a.fixedWebhookChannelID\n\ta.mu.RUnlock()\n")
replace_once(
    "internal/transport/discord/admin.go",
    '''\t\t\tchannelID := strings.TrimSpace(channel.ID)\n\t\t\tif channelID == "" {\n\t\t\t\tcontinue\n\t\t\t}\n\t\t\tchannels = append(channels, DiscoveredChannel{\n''',
    '''\t\t\tchannelID := strings.TrimSpace(channel.ID)\n\t\t\tif channelID == "" {\n\t\t\t\tcontinue\n\t\t\t}\n\t\t\tif fixedWebhookChannelID != "" && channelID != fixedWebhookChannelID {\n\t\t\t\tcontinue\n\t\t\t}\n\t\t\tchannels = append(channels, DiscoveredChannel{\n''',
)

# --- API -----------------------------------------------------------------------------
replace_once(
    "internal/api/connection_capabilities.go",
    '''\tdto.IntegrationMode = controlstore.NormalizeIntegrationMode(dto.Transport, dto.IntegrationMode)\n\tif dto.Transport != "telegram" {\n\t\tdto.IntegrationMode = ""\n\t\tdto.Capabilities = nil\n\t\treturn\n\t}\n\tcaps := telegram.CapabilitiesForIntegrationMode(dto.IntegrationMode)\n\tdto.Capabilities = &caps\n''',
    '''\tdto.IntegrationMode = controlstore.NormalizeIntegrationMode(dto.Transport, dto.IntegrationMode)\n\tif dto.Transport == "telegram" {\n\t\tcaps := telegram.CapabilitiesForIntegrationMode(dto.IntegrationMode)\n\t\tdto.Capabilities = &caps\n\t\treturn\n\t}\n\tif dto.Transport != "discord" {\n\t\tdto.IntegrationMode = ""\n\t}\n\tdto.Capabilities = nil\n''',
)
replace_once("internal/api/connections.go", '\t"github.com/vm75/message-sync/internal/safelog"\n\ttelegram "github.com/vm75/message-sync/internal/transport/telegram"\n', '\t"github.com/vm75/message-sync/internal/safelog"\n\tdiscord "github.com/vm75/message-sync/internal/transport/discord"\n\ttelegram "github.com/vm75/message-sync/internal/transport/telegram"\n')
replace_once("internal/api/connections.go", '\tToken           string `json:"token,omitempty"`\n\tIntegrationMode string `json:"integrationMode,omitempty"`\n', '\tToken           string `json:"token,omitempty"`\n\tIntegrationMode string `json:"integrationMode,omitempty"`\n\tWebhookURL      string `json:"webhookUrl,omitempty"`\n\tChannelID       string `json:"channelId,omitempty"`\n')
replace_once("internal/api/connections.go", "\treq.Token = strings.TrimSpace(req.Token)\n\treq.IntegrationMode = strings.TrimSpace(req.IntegrationMode)\n", "\treq.Token = strings.TrimSpace(req.Token)\n\treq.IntegrationMode = strings.TrimSpace(req.IntegrationMode)\n\treq.WebhookURL = strings.TrimSpace(req.WebhookURL)\n\treq.ChannelID = strings.TrimSpace(req.ChannelID)\n")
replace_once(
    "internal/api/connections.go",
    '''\tcase req.Transport == "discord":\n\t\tif req.Token == "" {\n\t\t\tWriteError(w, http.StatusBadRequest, "discord connections require a bot token")\n\t\t\treturn\n\t\t}\n\t\tcredential = []byte(req.Token)\n''',
    '''\tcase req.Transport == "discord":\n\t\tvar credentialErr error\n\t\tcredential, credentialErr = discord.EncodeStoredCredential(req.IntegrationMode, req.Token, req.WebhookURL, req.ChannelID)\n\t\tif credentialErr != nil {\n\t\t\tWriteError(w, http.StatusBadRequest, credentialErr.Error())\n\t\t\treturn\n\t\t}\n''',
)
replace_once(
    "internal/api/connections.go",
    '''\t\tnewEnc, newNonce, encErr := s.credentialCipher.Encrypt([]byte(tok))\n\t\tif encErr != nil {\n''',
    '''\t\treplacement := []byte(tok)\n\t\tif conn.Transport == "discord" {\n\t\t\tcurrentRaw, decryptErr := s.credentialCipher.Decrypt(conn.EncryptedCredential, conn.CredentialNonce)\n\t\t\tif decryptErr != nil {\n\t\t\t\tWriteError(w, http.StatusInternalServerError, "failed to read existing Discord credentials")\n\t\t\t\treturn\n\t\t\t}\n\t\t\tcurrentCredential, decodeErr := discord.DecodeStoredCredential(conn.IntegrationMode, currentRaw)\n\t\t\tif decodeErr != nil {\n\t\t\t\tWriteError(w, http.StatusInternalServerError, "failed to read existing Discord credentials")\n\t\t\t\treturn\n\t\t\t}\n\t\t\treplacement, decodeErr = discord.EncodeStoredCredential(conn.IntegrationMode, tok, currentCredential.WebhookURL, currentCredential.ChannelID)\n\t\t\tif decodeErr != nil {\n\t\t\t\tWriteError(w, http.StatusBadRequest, decodeErr.Error())\n\t\t\t\treturn\n\t\t\t}\n\t\t}\n\t\tnewEnc, newNonce, encErr := s.credentialCipher.Encrypt(replacement)\n\t\tif encErr != nil {\n''',
)

# --- runtime -------------------------------------------------------------------------
replace_once(
    "internal/app/app.go",
    '''\t\t\t\tif c.Transport == "discord" && c.Enabled {\n\t\t\t\t\ttokenBytes, err := credentialCipher.Decrypt(c.EncryptedCredential, c.CredentialNonce)\n\t\t\t\t\tif err != nil {\n\t\t\t\t\t\tsafelog.Error(logger, "decrypt discord credential failed", "discord_decrypt", err)\n\t\t\t\t\t\tcontinue\n\t\t\t\t\t}\n''',
    '''\t\t\t\tif c.Transport == "discord" && c.Enabled {\n\t\t\t\t\tcredentialBytes, err := credentialCipher.Decrypt(c.EncryptedCredential, c.CredentialNonce)\n\t\t\t\t\tif err != nil {\n\t\t\t\t\t\tsafelog.Error(logger, "decrypt discord credential failed", "discord_decrypt", err)\n\t\t\t\t\t\tcontinue\n\t\t\t\t\t}\n\t\t\t\t\tdiscordCredential, err := discord.DecodeStoredCredential(c.IntegrationMode, credentialBytes)\n\t\t\t\t\tif err != nil {\n\t\t\t\t\t\tsafelog.Error(logger, "decode discord credential failed", "discord_credential", err)\n\t\t\t\t\t\tcontinue\n\t\t\t\t\t}\n''',
)
replace_once(
    "internal/app/app.go",
    '''\t\t\t\t\tdcInst, err := openDiscord(ctx, discord.Options{\n\t\t\t\t\t\tConnectionID:  c.ID,\n\t\t\t\t\t\tToken:         string(tokenBytes),\n\t\t\t\t\t\tChannelIDs:    connChannelIDs,\n\t\t\t\t\t\tHasher:        hasher,\n''',
    '''\t\t\t\t\tdcInst, err := openDiscord(ctx, discord.Options{\n\t\t\t\t\t\tConnectionID:             c.ID,\n\t\t\t\t\t\tToken:                    discordCredential.BotToken,\n\t\t\t\t\t\tChannelIDs:               connChannelIDs,\n\t\t\t\t\t\tExplicitWebhookURL:       discordCredential.WebhookURL,\n\t\t\t\t\t\tExplicitWebhookChannelID: discordCredential.ChannelID,\n\t\t\t\t\t\tHasher:                   hasher,\n''',
)
replace_once(
    "internal/app/app.go",
    '''\t\t\t\t\t\ttokenBytes, err := credentialCipher.Decrypt(c.EncryptedCredential, c.CredentialNonce)\n\t\t\t\t\t\tif err != nil {\n\t\t\t\t\t\t\tsafelog.Error(logger, "decrypt discord credential failed", "discord_decrypt", err)\n\t\t\t\t\t\t\tif exists && credentialChanged[connID] {\n\t\t\t\t\t\t\t\treloadErr = errors.Join(reloadErr, errors.New("decrypt Discord credential failed"))\n\t\t\t\t\t\t\t}\n\t\t\t\t\t\t\tcontinue\n\t\t\t\t\t\t}\n''',
    '''\t\t\t\t\t\tcredentialBytes, err := credentialCipher.Decrypt(c.EncryptedCredential, c.CredentialNonce)\n\t\t\t\t\t\tif err != nil {\n\t\t\t\t\t\t\tsafelog.Error(logger, "decrypt discord credential failed", "discord_decrypt", err)\n\t\t\t\t\t\t\tif exists && credentialChanged[connID] {\n\t\t\t\t\t\t\t\treloadErr = errors.Join(reloadErr, errors.New("decrypt Discord credential failed"))\n\t\t\t\t\t\t\t}\n\t\t\t\t\t\t\tcontinue\n\t\t\t\t\t\t}\n\t\t\t\t\t\tdiscordCredential, err := discord.DecodeStoredCredential(c.IntegrationMode, credentialBytes)\n\t\t\t\t\t\tif err != nil {\n\t\t\t\t\t\t\tsafelog.Error(logger, "decode discord credential failed", "discord_credential", err)\n\t\t\t\t\t\t\tcontinue\n\t\t\t\t\t\t}\n''',
)
replace_once(
    "internal/app/app.go",
    '''\t\t\t\t\t\tdcInst, err := openDiscord(ctx, discord.Options{\n\t\t\t\t\t\t\tConnectionID:  c.ID,\n\t\t\t\t\t\t\tToken:         string(tokenBytes),\n\t\t\t\t\t\t\tChannelIDs:    connChannelIDs,\n\t\t\t\t\t\t\tHasher:        hasher,\n''',
    '''\t\t\t\t\t\tdcInst, err := openDiscord(ctx, discord.Options{\n\t\t\t\t\t\t\tConnectionID:             c.ID,\n\t\t\t\t\t\t\tToken:                    discordCredential.BotToken,\n\t\t\t\t\t\t\tChannelIDs:               connChannelIDs,\n\t\t\t\t\t\t\tExplicitWebhookURL:       discordCredential.WebhookURL,\n\t\t\t\t\t\t\tExplicitWebhookChannelID: discordCredential.ChannelID,\n\t\t\t\t\t\t\tHasher:                   hasher,\n''',
)

# --- Admin UI ------------------------------------------------------------------------
replace_once(
    "internal/api/web/index.html",
    '''          <div id="add-conn-telegram-mode-group" class="form-group hidden">\n''',
    '''          <div id="add-conn-discord-mode-group" class="form-group hidden">\n            <label for="add-conn-discord-mode" class="form-label">Discord Outbound Webhook</label>\n            <select id="add-conn-discord-mode" class="form-select">\n              <option value="managed">Managed by message-sync</option>\n              <option value="webhook">Use existing webhook + channel ID</option>\n            </select>\n            <p class="form-hint">Both modes still use the bot token for Discord Gateway ingress. Existing-webhook mode avoids granting the bot Manage Webhooks and binds this connection to one Discord channel.</p>\n          </div>\n\n          <div id="add-conn-telegram-mode-group" class="form-group hidden">\n''',
)
replace_once(
    "internal/api/web/index.html",
    '''          <div id="add-conn-mtproto-group" class="hidden" style="display:flex;flex-direction:column;gap:12px;">\n''',
    '''          <div id="add-conn-discord-webhook-group" class="hidden" style="display:flex;flex-direction:column;gap:12px;">\n            <div class="form-group">\n              <label for="add-conn-discord-webhook-url" class="form-label">Existing Webhook URL</label>\n              <input type="password" id="add-conn-discord-webhook-url" class="form-input" autocomplete="new-password" placeholder="https://discord.com/api/webhooks/...">\n              <p class="form-hint">Stored encrypted with the bot token and never returned by read APIs.</p>\n            </div>\n            <div class="form-group">\n              <label for="add-conn-discord-channel-id" class="form-label">Discord Channel ID</label>\n              <input type="text" id="add-conn-discord-channel-id" class="form-input font-mono" inputmode="numeric" placeholder="123456789012345678">\n              <p class="form-hint">The bot listens to this channel; the existing webhook posts forwarded messages into the same channel.</p>\n            </div>\n          </div>\n\n          <div id="add-conn-mtproto-group" class="hidden" style="display:flex;flex-direction:column;gap:12px;">\n''',
)
replace_once(
    "internal/api/web/index.html",
    '''          <div id="add-conn-help-discord" class="form-hint hidden">\n            <strong>Discord setup</strong><br>\n            In the <a href="https://discord.com/developers/applications" target="_blank" rel="noopener noreferrer">official Discord Developer Portal</a>, create/select an application, open its Bot page, enable Message Content Intent, and copy/reset the bot token here. Install the bot in the target server with View Channel, Read Message History, Send Messages, Add Reactions, Manage Webhooks, and thread/poll access as needed. Then return here and use connection-scoped Discover to add channels.\n          </div>\n''',
    '''          <div id="add-conn-help-discord" class="form-hint hidden">\n            <strong>Discord setup</strong><br>\n            Create/select an application in the <a href="https://discord.com/developers/applications" target="_blank" rel="noopener noreferrer">official Discord Developer Portal</a>, enable Message Content Intent, and paste the bot token above. The bot is always used for inbound Gateway events, reactions, polls, threads, history, and other native operations.<br><br>\n            <strong>Managed webhook:</strong> grant Manage Webhooks; message-sync creates/reuses one bridge webhook for each configured channel.<br>\n            <strong>Existing webhook + channel ID:</strong> create the webhook yourself in Channel Settings → Integrations → Webhooks, paste its URL and the matching channel ID here, and Manage Webhooks is not required. This mode is intentionally bound to that one channel.\n          </div>\n''',
)

replace_once("internal/api/web/js/app-base.js", "  const addConnTelegramModeGroup = document.getElementById('add-conn-telegram-mode-group');\n", "  const addConnDiscordModeGroup = document.getElementById('add-conn-discord-mode-group');\n  const addConnDiscordMode = document.getElementById('add-conn-discord-mode');\n  const addConnDiscordWebhookGroup = document.getElementById('add-conn-discord-webhook-group');\n  const addConnDiscordWebhookURL = document.getElementById('add-conn-discord-webhook-url');\n  const addConnDiscordChannelID = document.getElementById('add-conn-discord-channel-id');\n  const addConnTelegramModeGroup = document.getElementById('add-conn-telegram-mode-group');\n")
replace_once("internal/api/web/js/app-base.js", "    if (addConnTelegramMode) addConnTelegramMode.value = 'bot';\n", "    if (addConnDiscordMode) addConnDiscordMode.value = 'managed';\n    if (addConnDiscordWebhookURL) addConnDiscordWebhookURL.value = '';\n    if (addConnDiscordChannelID) addConnDiscordChannelID.value = '';\n    if (addConnTelegramMode) addConnTelegramMode.value = 'bot';\n")
replace_once(
    "internal/api/web/js/app-base.js",
    '''  function selectAddTelegramMode(mode) {\n    const isMTProto = addConnTransport === 'telegram' && mode === 'mtproto';\n    if (addConnTokenGroup) addConnTokenGroup.classList.toggle('hidden', addConnTransport === 'whatsapp' || isMTProto);\n''',
    '''  function selectAddDiscordMode(mode) {\n    const isExplicitWebhook = addConnTransport === 'discord' && mode === 'webhook';\n    if (addConnDiscordWebhookGroup) addConnDiscordWebhookGroup.classList.toggle('hidden', !isExplicitWebhook);\n    if (addConnDiscordWebhookURL) addConnDiscordWebhookURL.required = isExplicitWebhook;\n    if (addConnDiscordChannelID) addConnDiscordChannelID.required = isExplicitWebhook;\n  }\n\n  function selectAddTelegramMode(mode) {\n    const isMTProto = addConnTransport === 'telegram' && mode === 'mtproto';\n    const needsToken = addConnTransport === 'discord' || (addConnTransport === 'telegram' && !isMTProto);\n    if (addConnTokenGroup) addConnTokenGroup.classList.toggle('hidden', !needsToken);\n''',
)
replace_once("internal/api/web/js/app-base.js", "    if (addConnToken) addConnToken.required = addConnTransport !== 'whatsapp' && !isMTProto;\n", "    if (addConnToken) addConnToken.required = needsToken;\n")
replace_once("internal/api/web/js/app-base.js", "    if (addConnTelegramModeGroup) addConnTelegramModeGroup.classList.toggle('hidden', transport !== 'telegram');\n", "    if (addConnDiscordModeGroup) addConnDiscordModeGroup.classList.toggle('hidden', transport !== 'discord');\n    if (addConnTelegramModeGroup) addConnTelegramModeGroup.classList.toggle('hidden', transport !== 'telegram');\n")
replace_once(
    "internal/api/web/js/app-base.js",
    '''    if (addConnToken) addConnToken.value = '';\n    selectAddTelegramMode(addConnTelegramMode ? addConnTelegramMode.value : 'bot');\n''',
    '''    if (addConnToken) addConnToken.value = '';\n    if (transport !== 'discord') {\n      if (addConnDiscordWebhookURL) addConnDiscordWebhookURL.value = '';\n      if (addConnDiscordChannelID) addConnDiscordChannelID.value = '';\n    }\n    selectAddDiscordMode(addConnDiscordMode ? addConnDiscordMode.value : 'managed');\n    selectAddTelegramMode(addConnTelegramMode ? addConnTelegramMode.value : 'bot');\n''',
)
replace_once(
    "internal/api/web/js/app-base.js",
    '''    const telegramMode = addConnTransport === 'telegram' && addConnTelegramMode ? addConnTelegramMode.value : 'bot';\n    const isMTProto = addConnTransport === 'telegram' && telegramMode === 'mtproto';\n''',
    '''    const discordMode = addConnTransport === 'discord' && addConnDiscordMode ? addConnDiscordMode.value : 'managed';\n    const isDiscordWebhook = addConnTransport === 'discord' && discordMode === 'webhook';\n    const webhookUrl = isDiscordWebhook && addConnDiscordWebhookURL ? addConnDiscordWebhookURL.value.trim() : '';\n    const discordChannelId = isDiscordWebhook && addConnDiscordChannelID ? addConnDiscordChannelID.value.trim() : '';\n    const telegramMode = addConnTransport === 'telegram' && addConnTelegramMode ? addConnTelegramMode.value : 'bot';\n    const isMTProto = addConnTransport === 'telegram' && telegramMode === 'mtproto';\n''',
)
replace_once("internal/api/web/js/app-base.js", "    if (addConnToken) addConnToken.value = '';\n    if (addConnMTProtoAPIHash) addConnMTProtoAPIHash.value = '';\n", "    if (addConnToken) addConnToken.value = '';\n    if (addConnDiscordWebhookURL) addConnDiscordWebhookURL.value = '';\n    if (addConnMTProtoAPIHash) addConnMTProtoAPIHash.value = '';\n")
replace_once(
    "internal/api/web/js/app-base.js",
    '''    if (isMTProto && (!Number.isInteger(apiId) || apiId <= 0 || !apiHash || !phone)) {\n''',
    '''    if (isDiscordWebhook && (!webhookUrl || !DISCORD_CHANNEL_REGEX.test(discordChannelId))) {\n      if (addConnAlert) { addConnAlert.textContent = 'A valid existing webhook URL and numeric channel ID are required.'; addConnAlert.classList.remove('hidden'); }\n      return;\n    }\n    if (isMTProto && (!Number.isInteger(apiId) || apiId <= 0 || !apiHash || !phone)) {\n''',
)
replace_once(
    "internal/api/web/js/app-base.js",
    '''      if (addConnTransport === 'telegram') payload.integrationMode = telegramMode;\n      if (!isMTProto && addConnTransport !== 'whatsapp') payload.token = token;\n''',
    '''      if (addConnTransport === 'telegram') payload.integrationMode = telegramMode;\n      if (addConnTransport === 'discord') payload.integrationMode = discordMode;\n      if (!isMTProto && addConnTransport !== 'whatsapp') payload.token = token;\n      if (isDiscordWebhook) {\n        payload.webhookUrl = webhookUrl;\n        payload.channelId = discordChannelId;\n      }\n''',
)
replace_once("internal/api/web/js/app-base.js", "      if (addConnToken) addConnToken.value = '';\n      if (addConnMTProtoAPIHash) addConnMTProtoAPIHash.value = '';\n", "      if (addConnToken) addConnToken.value = '';\n      if (addConnDiscordWebhookURL) addConnDiscordWebhookURL.value = '';\n      if (addConnMTProtoAPIHash) addConnMTProtoAPIHash.value = '';\n")
replace_once("internal/api/web/js/app-base.js", "  if (addConnTelegramMode) addConnTelegramMode.addEventListener('change', () => selectAddTelegramMode(addConnTelegramMode.value));\n", "  if (addConnDiscordMode) addConnDiscordMode.addEventListener('change', () => selectAddDiscordMode(addConnDiscordMode.value));\n  if (addConnTelegramMode) addConnTelegramMode.addEventListener('change', () => selectAddTelegramMode(addConnTelegramMode.value));\n")
replace_once("internal/api/web/js/app-base.js", "        { key: 'discord', label: 'Discord Bots', icon:", "        { key: 'discord', label: 'Discord Connections', icon:")
replace_once(
    "internal/api/web/js/app-base.js",
    '''        } else if (conn.transport === 'discord') {\n          if (st.connected) {\n            const hasPermIssue = Array.isArray(st.webhooks) && st.webhooks.some(w => w.status === 'missing_permission');\n''',
    '''        } else if (conn.transport === 'discord') {\n          const explicitWebhook = conn.integrationMode === 'webhook';\n          if (st.connected) {\n            const hasPermIssue = !explicitWebhook && Array.isArray(st.webhooks) && st.webhooks.some(w => w.status === 'missing_permission');\n''',
)
replace_once(
    "internal/api/web/js/app-base.js",
    '''            nextAction = hasPermIssue ? 'Grant Manage Webhooks permission on bridged Discord channels.' : (connEps.length === 0 ? 'Discover & add channels as endpoints.' : 'Ready to sync.');\n            detailHtml = hasPermIssue ? 'Gateway active · Webhook permission degraded.' : `Gateway active (${connEps.length} endpoint${connEps.length === 1 ? '' : 's'}).`;\n''',
    '''            nextAction = hasPermIssue ? 'Grant Manage Webhooks permission on bridged Discord channels.' : (connEps.length === 0 ? (explicitWebhook ? 'Discover the configured webhook channel and add it as an endpoint.' : 'Discover & add channels as endpoints.') : 'Ready to sync.');\n            detailHtml = hasPermIssue ? 'Gateway active · Webhook permission degraded.' : (explicitWebhook ? `Gateway active · Existing webhook outbound (${connEps.length} endpoint${connEps.length === 1 ? '' : 's'}).` : `Gateway active · Managed webhook outbound (${connEps.length} endpoint${connEps.length === 1 ? '' : 's'}).`);\n''',
)
replace_once(
    "internal/api/web/js/app-base.js",
    '''              ${conn.transport === 'telegram' ? `<span class="badge badge-neutral">${escapeHtml(conn.integrationMode === 'mtproto' ? 'Phone / MTProto' : 'Bot API')}</span>` : ''}\n''',
    '''              ${conn.transport === 'discord' ? `<span class="badge badge-neutral">${escapeHtml(conn.integrationMode === 'webhook' ? 'Existing Webhook' : 'Managed Webhook')}</span>` : ''}\n              ${conn.transport === 'telegram' ? `<span class="badge badge-neutral">${escapeHtml(conn.integrationMode === 'mtproto' ? 'Phone / MTProto' : 'Bot API')}</span>` : ''}\n''',
)

# --- README --------------------------------------------------------------------------
replace_once(
    "README.md",
    '''- `/data/control.db` is the explicit sensitive exception for accounts, sessions, audit records, encrypted Discord/Telegram Bot API credentials, encrypted Telegram MTProto API/session/peer state, and experimental membership verification. OTPs and Telegram 2FA passwords are never persisted.\n''',
    '''- `/data/control.db` is the explicit sensitive exception for accounts, sessions, audit records, encrypted Discord bot/webhook credentials, encrypted Telegram Bot API credentials, encrypted Telegram MTProto API/session/peer state, and experimental membership verification. Discord webhook URLs and bot tokens are never returned through read APIs; Telegram OTPs and 2FA passwords are never persisted.\n''',
)
replace_once(
    "README.md",
    '''### Discord\n\nCreate a Discord bot, enable the privileged **Message Content** intent, and grant **View Channel**, **Read Message History**, **Send Messages**, **Add Reactions**, and **Manage Webhooks** in destination channels. Paste the bot token once when creating the connection; the service encrypts it in `control.db` and never returns it through read APIs.\n''',
    '''### Discord\n\nDiscord always uses a **bot token** for inbound Gateway events and native Discord operations. Enable the privileged **Message Content** intent in the Discord Developer Portal, install the bot in the target server, and grant the permissions required by the features you use. At minimum for normal bidirectional message sync, grant **View Channel**, **Read Message History**, and **Send Messages**; grant **Add Reactions** for reaction sync and the appropriate thread/poll permissions for those features.\n\n`message-sync` supports two outbound webhook setups. Choose one when creating the Discord connection.\n\n#### Option A — managed webhook (default)\n\nUse this when you want the simplest setup or one Discord bot connection to serve multiple channels.\n\n1. Create the Discord application and bot, enable **Message Content** intent, and copy the bot token.\n2. Install the bot in the server with **View Channel**, **Read Message History**, **Send Messages**, **Manage Webhooks**, plus any reaction/thread/poll permissions you need.\n3. In `message-sync`, create a Discord connection and choose **Managed by message-sync**.\n4. Paste the bot token once.\n5. Use **Discover** to add one or more Discord channels as endpoints.\n\nFor every configured channel, `message-sync` finds or creates one bridge-owned webhook and keeps the webhook credential only inside the Discord adapter process. The bot needs **Manage Webhooks** because it provisions and repairs those webhooks.\n\n#### Option B — existing webhook + channel ID\n\nUse this when you prefer to create the webhook yourself and do **not** want to grant the bot **Manage Webhooks**.\n\n1. Create the Discord application and bot, enable **Message Content** intent, and copy the bot token.\n2. Install the bot in the server with **View Channel**, **Read Message History**, **Send Messages**, plus any reaction/thread/poll permissions you need. **Manage Webhooks is not required.**\n3. In Discord, open the target channel's **Edit Channel → Integrations → Webhooks**, create a webhook, and copy its webhook URL.\n4. Enable Discord Developer Mode if necessary, then copy the same channel's numeric **Channel ID**.\n5. In `message-sync`, create a Discord connection and choose **Use existing webhook + channel ID**.\n6. Paste the **bot token**, **webhook URL**, and matching **channel ID**.\n7. Use **Discover**; this connection exposes only the configured channel, which you can then add as an endpoint.\n\nThe bot token is still required in this mode. The bot listens for Discord → WhatsApp/Telegram messages, edits, reactions, polls, threads, and history events; the supplied webhook handles WhatsApp/Telegram → Discord message delivery and webhook-owned edits/deletes. Messages sent by that webhook are recognized as bridge-owned and ignored on ingress to prevent loops.\n\nAn explicit-webhook Discord connection is intentionally bound to **one Discord channel**. Use managed-webhook mode when one connection must serve multiple Discord channels. The bot token and explicit webhook URL are encrypted in `control.db` and are never returned by connection read APIs.\n\n| Discord requirement | Managed webhook | Existing webhook + channel ID |\n|---|---:|---:|\n| Bot application + bot token | Required | Required |\n| Message Content intent | Required for ordinary message ingress | Required for ordinary message ingress |\n| View Channel / Read Message History | Required | Required |\n| Send Messages | Required for native bot operations | Required for native bot operations |\n| Add Reactions | Required only for reaction sync | Required only for reaction sync |\n| Manage Webhooks | **Required** | **Not required** |\n| Webhook creation | Automatic | Manual |\n| Channel ID entry | Discovered | Entered during connection setup |\n| Multiple Discord channels per connection | Yes | No — one configured channel |\n''',
)
replace_once(
    "README.md",
    '''Discord/Telegram Bot API tokens and Telegram MTProto application/session state are configured dynamically and encrypted with AES-256-GCM in `control.db`; they never belong in `.env`.\n''',
    '''Discord bot tokens, optional explicit Discord webhook URLs, Telegram Bot API tokens, and Telegram MTProto application/session state are configured dynamically and encrypted with AES-256-GCM in `control.db`; they never belong in `.env`.\n''',
)

# --- static UI tests -----------------------------------------------------------------
replace_once("internal/api/static_test.go", '\t\t\t`id="add-conn-help-discord"`,\n\t\t\t`id="add-conn-help-telegram"`,\n', '\t\t\t`id="add-conn-help-discord"`,\n\t\t\t`id="add-conn-discord-mode"`,\n\t\t\t`id="add-conn-discord-webhook-url"`,\n\t\t\t`id="add-conn-discord-channel-id"`,\n\t\t\t`id="add-conn-help-telegram"`,\n')
replace_once("internal/api/static_test.go", '\t\t\t`type="password" id="conn-bot-token" class="form-input" autocomplete="new-password"`,\n\t\t\t`type="password" id="replace-conn-bot-token" class="form-input" autocomplete="new-password"`,\n', '\t\t\t`type="password" id="conn-bot-token" class="form-input" autocomplete="new-password"`,\n\t\t\t`type="password" id="add-conn-discord-webhook-url" class="form-input" autocomplete="new-password"`,\n\t\t\t`type="password" id="replace-conn-bot-token" class="form-input" autocomplete="new-password"`,\n')
replace_once("internal/api/static_test.go", 'for _, expected := range []string{"selectAddTelegramMode", "openTelegramMTProto",', 'for _, expected := range []string{"selectAddDiscordMode", "selectAddTelegramMode", "openTelegramMTProto",')
replace_once("internal/api/static_test.go", 'for _, forbidden := range []string{"localStorage.setItem(\'apiHash\'",', 'for _, forbidden := range []string{"localStorage.setItem(\'webhookUrl\'", "sessionStorage.setItem(\'webhookUrl\'", "localStorage.setItem(\'apiHash\'",')

# --- API tests for encrypted explicit webhook credentials -----------------------------
with Path("internal/api/connections_test.go").open("a") as f:
    f.write(r'''

func TestConnections_DiscordExplicitWebhookCredential(t *testing.T) {
    srv, _, controlDB, adminToken, _ := setupConnectionsTestEnv(t)
    body := `{"id":"conn-dc-hook","transport":"discord","integrationMode":"webhook","label":"Webhook Discord","token":"bot-secret","webhookUrl":"https://discord.com/api/webhooks/123456789/webhook-secret","channelId":"987654321012345678"}`
    req := httptest.NewRequest(http.MethodPost, "/api/connections", strings.NewReader(body))
    req.Header.Set("Authorization", "Bearer "+adminToken)
    rec := httptest.NewRecorder()
    srv.Handler().ServeHTTP(rec, req)
    if rec.Code != http.StatusCreated {
        t.Fatalf("create explicit webhook connection got %d: %s", rec.Code, rec.Body.String())
    }
    response := rec.Body.String()
    for _, forbidden := range []string{"bot-secret", "webhook-secret", "webhookUrl", "channelId"} {
        if strings.Contains(response, forbidden) {
            t.Fatalf("create response leaked Discord credential field %q: %s", forbidden, response)
        }
    }
    if !strings.Contains(response, `"integrationMode":"webhook"`) {
        t.Fatalf("response missing explicit webhook integration mode: %s", response)
    }

    var encrypted, nonce []byte
    if err := controlDB.QueryRow(`SELECT encrypted_credential, credential_nonce FROM transport_connections WHERE id = 'conn-dc-hook'`).Scan(&encrypted, &nonce); err != nil {
        t.Fatal(err)
    }
    raw, err := srv.credentialCipher.Decrypt(encrypted, nonce)
    if err != nil {
        t.Fatal(err)
    }
    credential, err := discord.DecodeStoredCredential("webhook", raw)
    if err != nil {
        t.Fatal(err)
    }
    if credential.BotToken != "bot-secret" || credential.ChannelID != "987654321012345678" || !strings.Contains(credential.WebhookURL, "webhook-secret") {
        t.Fatalf("unexpected stored Discord webhook credential: %#v", credential)
    }

    patch := httptest.NewRequest(http.MethodPatch, "/api/connections/conn-dc-hook", strings.NewReader(`{"token":"replacement-bot-secret"}`))
    patch.Header.Set("Authorization", "Bearer "+adminToken)
    rec = httptest.NewRecorder()
    srv.Handler().ServeHTTP(rec, patch)
    if rec.Code != http.StatusOK {
        t.Fatalf("replace bot token got %d: %s", rec.Code, rec.Body.String())
    }
    if err := controlDB.QueryRow(`SELECT encrypted_credential, credential_nonce FROM transport_connections WHERE id = 'conn-dc-hook'`).Scan(&encrypted, &nonce); err != nil {
        t.Fatal(err)
    }
    raw, err = srv.credentialCipher.Decrypt(encrypted, nonce)
    if err != nil {
        t.Fatal(err)
    }
    credential, err = discord.DecodeStoredCredential("webhook", raw)
    if err != nil {
        t.Fatal(err)
    }
    if credential.BotToken != "replacement-bot-secret" || credential.ChannelID != "987654321012345678" || !strings.Contains(credential.WebhookURL, "webhook-secret") {
        t.Fatalf("bot token replacement did not preserve explicit webhook binding: %#v", credential)
    }
}

func TestConnections_DiscordExplicitWebhookRequiresURLAndChannel(t *testing.T) {
    srv, _, _, adminToken, _ := setupConnectionsTestEnv(t)
    for _, body := range []string{
        `{"transport":"discord","integrationMode":"webhook","label":"bad","token":"bot-secret","channelId":"123456789"}`,
        `{"transport":"discord","integrationMode":"webhook","label":"bad","token":"bot-secret","webhookUrl":"https://discord.com/api/webhooks/123/token"}`,
        `{"transport":"discord","integrationMode":"webhook","label":"bad","token":"bot-secret","webhookUrl":"https://example.com/api/webhooks/123/token","channelId":"123456789"}`,
    } {
        req := httptest.NewRequest(http.MethodPost, "/api/connections", strings.NewReader(body))
        req.Header.Set("Authorization", "Bearer "+adminToken)
        rec := httptest.NewRecorder()
        srv.Handler().ServeHTTP(rec, req)
        if rec.Code != http.StatusBadRequest {
            t.Fatalf("invalid explicit webhook create got %d, want 400: %s", rec.Code, rec.Body.String())
        }
    }
}
''')

print("Discord explicit webhook support patch applied")
