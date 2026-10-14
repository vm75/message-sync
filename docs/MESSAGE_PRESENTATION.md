# Synchronized message presentation

This document defines the user-visible attribution syntax produced by the canonical router. These formats are compatibility invariants: coding agents must not casually normalize delimiters, expose additional identity fields, or change the precedence rules while refactoring routing, LID handling, threads/topics, reactions, polls, or recovery.

## Sender attribution

With the default `push_name` username mode, sender presentation uses this precedence:

1. normalized provider display name;
2. phone number only when no display name is available;
3. opaque sender ID only when neither is available.

A phone number must **not** be added alongside an available display name. In particular, do not render `phone (name)`.

With `hash` username mode, the opaque HMAC-derived sender ID is used instead of the display name/phone fallback chain.

## Message header syntax

For an ordinary message, the visible attribution header is:

```text
<group-alias>/<name>: <message>
```

The router currently wraps the attribution portion in bold-italic provider markup where supported, for example:

```text
*_<group-alias>/<name>_*: <message>
```

When `childContextDisplayMode=friendly` and a Discord thread/forum-post or Telegram topic label is available, the visible attribution header is:

```text
<group-alias>/<thread-or-topic-label>/<name>: <message>
```

For example:

```text
developers/Backend API/Alice: deployed the fix
community/Travel Plans/Bob: Saturday works for me
```

The `/` separators are intentional and part of the presentation contract. Do not add `thread:` or `topic:` prefixes unless the product contract is deliberately changed together with tests and documentation.

The child label itself is sufficient presentation context; routing still uses opaque child-scope identity and never parses this rendered header. Opaque child IDs or context tokens must never be shown in forwarded messages.

## Child-context storage mode

The current default is `childContextDisplayMode=opaque`. This setting controls **label persistence**, not the visible header shape.

- `opaque`: use a live thread/topic label transiently when available; otherwise show the generic `thread` or `topic` fallback. Do not persist the child name.
- `friendly`: use the same `<group>/<thread-or-topic>/<user>` header and additionally persist bounded observed labels so names can survive restart/replay.

Both modes therefore use the same human-facing syntax and neither mode emits an opaque context preamble. ChildScope IDs remain internal routing metadata only.

## One-way Discord Anonymization (`anonymizeToDiscord`)

When a sync set configures `anonymizeToDiscord=true`, messages forwarded to Discord endpoints are subject to route-aware one-way pseudonymization:

1. **Sender name**: In `push_name` mode, the `<name>` attribution component and Discord webhook display name are replaced with a deterministic pseudonym derived from the sender's HMAC actor ID (e.g. `Silent Falcon Q7M5K`).
2. **Phone numbers**: Fallback phone numbers are stripped completely for all Discord-bound senders regardless of `usernameMode`; phone numbers never appear in attribution headers or sender fields forwarded to Discord.
3. **Structured mentions**: Mentions in Discord-bound creates **and edits** have their display names replaced with deterministic pseudonyms (e.g. `@Amber Otter 2PF3R`). Telegram and Discord ingress keep user mentions as privacy-safe HMAC actor tokens plus transient display metadata so normal destinations can still render the usual name while anonymized Discord can substitute a pseudonym. Provider-native mention IDs that still require derivation are keyed under the source transport namespace. Anonymization-enabled routing requires the identity hasher.
4. **Reply quotes**: Quoted text headers produced by this bridge (the exact `*_<label>_*: <body>` format) are stripped before being forwarded to Discord; other message content that happens to contain `/` is left untouched.
5. **Reactions**: Reaction fallback text sent to Discord uses the sender's deterministic pseudonym rather than their real name or phone number.
6. **Multi-destination isolation**: The anonymization is applied strictly per-destination. Other destinations in the same sync set (such as WhatsApp groups or Telegram chats) continue to receive standard attribution and real push names in `push_name` mode.
7. **Hash mode**: If the service is configured with `usernameMode=hash`, opaque HMAC actor IDs (`u_...`) are used for message body attribution (as in non-anonymized hash mode), but `PhoneNumber` is still cleared before the message is forwarded to Discord.

## Do-not-regress checklist

Changes touching sender identity, WhatsApp PN/LID resolution, Discord threads/forum posts, Telegram topics, reactions, polls, edits, replies, or routing presentation must preserve these rules unless a product change explicitly says otherwise:

- ordinary attribution: `<group-alias>/<name>`;
- child attribution: `<group-alias>/<thread-or-topic-label>/<name>`;
- `/` separates the group alias, child label, and sender;
- display name wins over phone number in `push_name` mode;
- phone number is only a fallback when display name is absent;
- hash mode continues to use the opaque sender ID;
- `anonymizeToDiscord` pseudonyms apply only to Discord destinations and preserve normal push names for other transports;
- rendered presentation is never parsed as routing identity.

Automated tests should assert the exact rendered forms whenever presentation logic is changed.


## Cross-group participant references

The bridge recognizes explicit `@{endpoint-alias/label}` references in user-authored
message bodies, such as `Hi @{group2/P1}`, `Hi @{group2/Alice Smith}`, or
`Hi @{group2/Silent Falcon Q7M5K}`. The braced syntax, not a bare `@name`,
identifies bridge addressing without colliding with provider-native mentions. Prefix the token with a backslash to display it literally without a
notification attempt. Native `@someone` behavior is unchanged.

The address is made of a configured endpoint alias and a durable per-endpoint
participant handle, allocated when a participant's HMAC actor identity is first
observed on an accepted inbound event. It is **not** a username, a globally
unique person name, or a group-member list position. `group1/P1` and
`group2/P1` are distinct addresses. Group alias renames cascade through
the registry; the old textual address does not automatically redirect.

On Discord endpoints with `anonymizeToDiscord` enabled, senders retain the
existing deterministic, word-based aliases (e.g. `group/Rust Ocean Q7M5K`)
in `push_name` mode. Stable `Pn` handles remain available as optional
fallback addresses; they must not replace the visible pseudonym. Other
recipients retain normal sender-display presentation. In hash mode, the
existing opaque actor presentation remains unchanged.

A reference resolves only for an observed participant in an endpoint within
the sender's sync set. Stable `Pn` handles and full deterministic pseudonyms
both work across restarts because they use persisted HMAC actor records, not
personal data. A two-word alias stem (e.g. `Rust Ocean`) is also accepted
only when unique within that group. Real-name lookup uses transient observed
names and requires re-observation after restart. Ambiguous names or alias
stems never trigger a native tag. The router never parses forwarded
attribution headers or guesses a native identity.

For an authorized reference, only destinations that verify a suitable native
identity and membership may generate a provider mention: WhatsApp requires a
known LID to avoid leaking phone numbers; Discord uses a transient observed
user ID and a current guild-member lookup with an explicit allowed-user
allowlist, but never emits identity-revealing native pings in an
anonymized Discord destination; Telegram Bot API uses transient observed user
IDs with GetChatMember and a safe inline mention. Telegram MTProto does not yet emit native cross-group mentions, but renders
a non-notifying textual reference. **Every destination formats braced input**:
with anonymization enabled for that destination, `Hi @{group2/Alice Smith}`
becomes `Hi @group2/Rust Ocean Q7M5K` when resolved, using the participant's
existing deterministic pseudonym. Input `@{group2/P1}` produces the same
anonymized display; it does not overwrite sender pseudonyms. With
anonymization disabled the reference becomes `Hi @group2/Alice Smith` when
the observed real name is available, otherwise `Hi @group2/P1`. An
unrecognized `Pn` remains a textual `@group/Pn` without
any notification; unresolved names on anonymized destinations are masked as
`@group/unknown`. Textual references do not contain braces. A non-anonymized
WhatsApp destination may use a live contact name when the referenced group is
on the same connection. Native notification remains best effort and is never
guaranteed by the bridge.

Incoming native provider mentions still follow their existing separate
normalization/presentation path. Cross references are not persisted with
message content or native IDs; existing edit APIs forward reference text but
cannot yet add new native mentions on edit.

