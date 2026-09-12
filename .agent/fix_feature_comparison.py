from pathlib import Path

path = Path("docs/FEATURE_COMPARISON.md")
text = path.read_text()
old1 = "| Friendly child-context presentation | Optional global mode renders `group/user` or `group:topic-or-thread/user`; labels are local presentation metadata, generic when unknown, and never used for routing. Discord uses the label as its per-message APP name and removes the wrapper from the body; WhatsApp and Telegram retain body attribution | Platform-specific presentation and topic metadata are available in bridge flows | Deliberately opt-in; opaque mode remains the privacy-first default and disabling friendly mode clears its label catalog. Presentation is transport-specific because Telegram Bot API cannot rename a bot per message. |"
new1 = "| Child-context presentation | Child messages render `group/user` or `group/topic-or-thread/user`; live labels are transient presentation metadata, generic when unknown, and never used for routing. `opaque` keeps labels transient; `friendly` may persist bounded observed labels so names survive restart/replay. Discord uses the complete presentation label as its per-message APP name and removes the wrapper from the body; WhatsApp and Telegram retain body attribution | Platform-specific presentation and topic metadata are available in bridge flows | Visible syntax is the same in both storage modes. Disabling friendly mode clears the stored label catalog without affecting opaque ChildScope routing. Presentation remains transport-specific because Telegram Bot API cannot rename a bot per message. |"
old2 = "| Sender presentation in Discord | One managed webhook per destination channel. Opaque mode uses the transient sender display name or HMAC fallback, prefixing cross-endpoint messages with the source alias. Friendly mode uses the complete transient label, such as `tg1:tt1/Alice`, as the per-message APP username and leaves the body unprefixed | Webhook delivery can override username/presentation per forwarded sender | Preserved and narrowed so sender presentation is transient metadata rather than persistent identity; no per-user webhook is created. |"
new2 = "| Sender presentation in Discord | One managed webhook per destination channel. Root messages use the transient sender display name or HMAC fallback, prefixing cross-endpoint messages with the source alias. Child messages use the complete transient label, such as `tg1/tt1/Alice`, as the per-message APP username and leave the body unprefixed | Webhook delivery can override username/presentation per forwarded sender | Preserved and narrowed so sender presentation is transient metadata rather than persistent identity; no per-user webhook is created. |"
for label, old, new in [("child-context row", old1, new1), ("Discord sender row", old2, new2)]:
    count = text.count(old)
    if count != 1:
        raise SystemExit(f"{label}: expected 1 occurrence, found {count}")
    text = text.replace(old, new, 1)
path.write_text(text)
print("feature comparison aligned")
