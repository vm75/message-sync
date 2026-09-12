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
<group-alias>:<thread-or-topic-label>/<name>: <message>
```

For example:

```text
developers:Backend API/Alice: deployed the fix
community:Travel Plans/Bob: Saturday works for me
```

The colon between `<group-alias>` and `<thread-or-topic-label>` is **intentional**. It identifies the child-context boundary and is part of the presentation contract. Do not replace it with `/`, and do not add `thread:` or `topic:` prefixes unless the product contract is deliberately changed together with tests and documentation.

The child label itself is sufficient presentation context; routing still uses opaque child-scope identity and never parses this rendered header.

## Default child-context mode

The current default is `childContextDisplayMode=opaque`, not `friendly`.

In `opaque` mode, the sender header remains:

```text
<group-alias>/<name>: <message>
```

and flattened child lineage may be represented separately by the router's opaque `[contexts ...]` presentation header. Friendly labels are presentation-only and have no routing authority.

Therefore, the friendly thread/topic syntax above is the invariant **when friendly child-context presentation is enabled**; it is not the default child-context storage/presentation mode.

## Do-not-regress checklist

Changes touching sender identity, WhatsApp PN/LID resolution, Discord threads/forum posts, Telegram topics, reactions, polls, edits, replies, or routing presentation must preserve these rules unless a product change explicitly says otherwise:

- ordinary attribution: `<group-alias>/<name>`;
- friendly child attribution: `<group-alias>:<thread-or-topic-label>/<name>`;
- `:` after the group alias is the intentional child-context delimiter;
- display name wins over phone number in `push_name` mode;
- phone number is only a fallback when display name is absent;
- hash mode continues to use the opaque sender ID;
- rendered presentation is never parsed as routing identity.

Automated tests should assert the exact rendered forms whenever presentation logic is changed.