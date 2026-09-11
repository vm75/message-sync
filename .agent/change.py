from pathlib import Path
import base64, gzip

root = Path('.')

def decode_chunks(prefix):
    parts = sorted(root.glob(f'.agent/{prefix}.gz.b64.*'))
    if not parts:
        raise SystemExit(f'missing chunks for {prefix}')
    encoded = ''.join(p.read_text().strip() for p in parts)
    return gzip.decompress(base64.b64decode(encoded)).decode()

(root / 'internal/transport/telegram/mtproto_live.go').write_text(decode_chunks('live'))
(root / 'internal/transport/telegram/mtproto_live_test.go').write_text(decode_chunks('test'))

for p in root.glob('.agent/*.gz.b64.*'):
    p.unlink()

tracker = root / 'docs/TELEGRAM_MTPROTO_SUPPORT_PROGRESS.md'
text = tracker.read_text()
text = text.replace('- [ ] #104 — Implement Telegram MTProto live transport parity for messages, media, replies, reactions, edits, deletes, and topics', '- [x] #104 — Implement Telegram MTProto live transport parity for messages, media, replies, reactions, edits, deletes, and topics')
marker = '\n## Completion rule\n'
log = '''\n### #104 — complete\n\n- Added MTProto live message transport for configured Telegram groups/supergroups/channels while preserving canonical transport identity `telegram`.\n- Added restart-safe encrypted per-connection peer/access-hash persistence with on-demand dialog refresh, so ordinary outbound sends do not depend on fresh post-restart traffic.\n- Added live text/media, native reply/topic routing, reactions, edits and deletes through gotd while preserving media limits and privacy boundaries.\n- Self-originated MTProto updates are suppressed before canonical fan-out to prevent bridge loops; peer/session state remains isolated per Telegram connection.\n- Verification: focused MTProto live-adapter tests plus `go test ./...` and `go vet ./...` in the tested-change workflow.\n'''
if '### #104 — complete' not in text:
    text = text.replace(marker, log + marker)
tracker.write_text(text)
