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

## Do-not-regress checklist

Changes touching sender identity, WhatsApp PN/LID resolution, Discord threads/forum posts, Telegram topics, reactions, polls, edits, replies, or routing presentation must preserve these rules unless a product change explicitly says otherwise:

- ordinary attribution: `<group-alias>/<name>`;
- child attribution: `<group-alias>/<thread-or-topic-label>/<name>`;
- `/` separates the group alias, child label, and sender;
- display name wins over phone number in `push_name` mode;
- phone number is only a fallback when display name is absent;
- hash mode continues to use the opaque sender ID;
- rendered presentation is never parsed as routing identity.

Automated tests should assert the exact rendered forms whenever presentation logic is changed.