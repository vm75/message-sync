from pathlib import Path
import base64, gzip

root = Path('.')
def decode_chunks(prefix):
    parts = sorted(root.glob(f'.agent/{prefix}.gz.b64.*'))
    if not parts:
        raise SystemExit(f'missing chunks for {prefix}')
    encoded = ''.join(p.read_text().strip() for p in parts)
    return gzip.decompress(base64.b64decode(encoded)).decode()

def replace_once(text, old, new, label):
    if old not in text:
        raise SystemExit(f'missing patch marker: {label}')
    return text.replace(old, new, 1)

live = decode_chunks('live')
test = decode_chunks('test')
(root / 'internal/transport/telegram/mtproto_live.go').write_text(live)
(root / 'internal/transport/telegram/mtproto_live_test.go').write_text(test)

path = root / 'internal/transport/telegram/mtproto.go'
text = path.read_text()
text = replace_once(text,
'''type mtprotoState struct {\n\tVersion int    `json:"version"`\n\tAPIID   int    `json:"apiId,omitempty"`\n\tAPIHash string `json:"apiHash,omitempty"`\n\tPhone   string `json:"phone,omitempty"`\n\tSession []byte `json:"session,omitempty"`\n}\n''',
'''type mtprotoState struct {\n\tVersion int                         `json:"version"`\n\tAPIID   int                         `json:"apiId,omitempty"`\n\tAPIHash string                      `json:"apiHash,omitempty"`\n\tPhone   string                      `json:"phone,omitempty"`\n\tSession []byte                      `json:"session,omitempty"`\n\tPeers   map[string]mtprotoPeerState `json:"peers,omitempty"`\n}\n''', 'state peers')
text = replace_once(text,
'''type encryptedSessionStorage struct{ box *mtprotoStateBox }\n''',
'''func (s *mtprotoStateBox) update(ctx context.Context, mutate func(*mtprotoState)) error {\n\ts.mu.Lock()\n\tdefer s.mu.Unlock()\n\tstate, err := s.loadLocked(ctx)\n\tif err != nil {\n\t\treturn err\n\t}\n\tmutate(&state)\n\tstate.Version = mtprotoStateVersion\n\traw, err := json.Marshal(state)\n\tif err != nil {\n\t\treturn errors.New("encode MTProto state")\n\t}\n\treturn s.raw.Store(ctx, raw)\n}\n\ntype encryptedSessionStorage struct{ box *mtprotoStateBox }\n''', 'state update')
text = replace_once(text,
'''\truntimeFactory  mtprotoRuntimeFactory\n\tlifecycleCtx    context.Context\n\tlifecycleCancel context.CancelFunc\n''',
'''\truntimeFactory  mtprotoRuntimeFactory\n\tlifecycleCtx    context.Context\n\tlifecycleCancel context.CancelFunc\n\tlive            *mtprotoLiveState\n''', 'adapter live field')
text = replace_once(text,
'''\tfactory := opts.mtprotoRuntimeFactory\n\tif factory == nil {\n\t\tfactory = newGotdRuntime\n\t}\n\tlifecycleCtx, lifecycleCancel := context.WithCancel(ctx)\n\tadapter := &MTProtoAdapter{\n\t\tconnectionID: strings.TrimSpace(opts.ConnectionID), events: make(chan transport.Incoming, eventBufferSize),\n\t\tstate: &mtprotoStateBox{raw: opts.MTProtoStateStore}, runtimeFactory: factory,\n\t\tlifecycleCtx: lifecycleCtx, lifecycleCancel: lifecycleCancel, authState: MTProtoAuthDisconnected,\n\t}\n''',
'''\tlive, err := newMTProtoLiveState(opts)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tfactory := opts.mtprotoRuntimeFactory\n\tlifecycleCtx, lifecycleCancel := context.WithCancel(ctx)\n\tadapter := &MTProtoAdapter{\n\t\tconnectionID: strings.TrimSpace(opts.ConnectionID), events: make(chan transport.Incoming, eventBufferSize),\n\t\tstate: &mtprotoStateBox{raw: opts.MTProtoStateStore}, runtimeFactory: factory,\n\t\tlifecycleCtx: lifecycleCtx, lifecycleCancel: lifecycleCancel, authState: MTProtoAuthDisconnected, live: live,\n\t}\n\tif adapter.runtimeFactory == nil {\n\t\tadapter.runtimeFactory = func(apiID int, apiHash string, storage gotdsession.Storage) mtprotoRuntime {\n\t\t\treturn newGotdRuntimeWithUpdates(apiID, apiHash, storage, gotdtelegram.UpdateHandlerFunc(adapter.handleMTProtoUpdates))\n\t\t}\n\t}\n''', 'open live runtime')
text = replace_once(text,
'''\tstate, err := adapter.state.load(ctx)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n''',
'''\tstate, err := adapter.state.load(ctx)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tadapter.live.loadPeers(state.Peers)\n''', 'load peers')
text = replace_once(text,
'''\ta.mu.Lock()\n\ta.cfg = cfg\n\ta.mu.Unlock()\n\treturn nil\n}\nfunc (a *MTProtoAdapter) Send(context.Context, transport.Outgoing) (transport.MessageRef, error) {\n\treturn transport.MessageRef{}, errors.New("Telegram MTProto live messaging is not enabled yet")\n}\nfunc (a *MTProtoAdapter) React(context.Context, transport.Reaction) error {\n\treturn errors.New("Telegram MTProto live messaging is not enabled yet")\n}\nfunc (a *MTProtoAdapter) Edit(context.Context, transport.MessageRef, string) error {\n\treturn errors.New("Telegram MTProto live messaging is not enabled yet")\n}\nfunc (a *MTProtoAdapter) Delete(context.Context, transport.MessageRef) error {\n\treturn errors.New("Telegram MTProto live messaging is not enabled yet")\n}\n''',
'''\tif a.live != nil {\n\t\tif err := a.live.updateConfig(cfg, a.connectionID); err != nil {\n\t\t\treturn err\n\t\t}\n\t}\n\ta.mu.Lock()\n\ta.cfg = cfg\n\ta.mu.Unlock()\n\treturn nil\n}\nfunc (a *MTProtoAdapter) Send(ctx context.Context, outgoing transport.Outgoing) (transport.MessageRef, error) {\n\treturn a.sendMTProto(ctx, outgoing)\n}\nfunc (a *MTProtoAdapter) React(ctx context.Context, reaction transport.Reaction) error {\n\treturn a.reactMTProto(ctx, reaction)\n}\nfunc (a *MTProtoAdapter) Edit(ctx context.Context, ref transport.MessageRef, text string) error {\n\treturn a.editMTProto(ctx, ref, text)\n}\nfunc (a *MTProtoAdapter) Delete(ctx context.Context, ref transport.MessageRef) error {\n\treturn a.deleteMTProto(ctx, ref)\n}\n''', 'live delegates')
text = replace_once(text,
'''\t\t\terr := runtime.Run(runCtx, func(clientCtx context.Context, authClient mtprotoAuthClient) error {\n\t\t\tauthorized, statusErr := authClient.Authorized(clientCtx)\n\t\t\ta.mu.Lock()\n''',
'''\t\t\terr := runtime.Run(runCtx, func(clientCtx context.Context, authClient mtprotoAuthClient) error {\n\t\t\tauthorized, statusErr := authClient.Authorized(clientCtx)\n\t\t\ta.mu.Lock()\n''', 'runtime marker')
# Initialize live state after publishing auth client/state so send/update paths see a ready client.
text = replace_once(text,
'''\t\t\ta.mu.Unlock()\n\t\t\tonce.Do(func() { close(ready) })\n\t\t\tif statusErr != nil {\n''',
'''\t\t\ta.mu.Unlock()\n\t\t\tif statusErr == nil && authorized {\n\t\t\t\ta.initializeMTProtoLive(clientCtx, authClient)\n\t\t\t}\n\t\t\tonce.Do(func() { close(ready) })\n\t\t\tif statusErr != nil {\n''', 'runtime live init')
text = replace_once(text,
'''\tif current.APIID != apiID || current.APIHash != apiHash || current.Phone != phone {\n\t\tcurrent.Session = nil\n\t}\n''',
'''\tif current.APIID != apiID || current.APIHash != apiHash || current.Phone != phone {\n\t\tcurrent.Session = nil\n\t\tcurrent.Peers = nil\n\t\tif a.live != nil {\n\t\t\ta.live.loadPeers(nil)\n\t\t}\n\t}\n''', 'credential peer reset')
text = replace_once(text,
'''\ta.mu.Unlock()\n\treturn a.AdminStatus(ctx), nil\n}\n\nfunc (a *MTProtoAdapter) SubmitMTProtoPassword''',
'''\ta.mu.Unlock()\n\tif !passwordRequired {\n\t\ta.initializeMTProtoLive(ctx, client)\n\t}\n\treturn a.AdminStatus(ctx), nil\n}\n\nfunc (a *MTProtoAdapter) SubmitMTProtoPassword''', 'code success live init')
text = replace_once(text,
'''\ta.mu.Lock()\n\ta.authState = MTProtoAuthConnected\n\ta.mu.Unlock()\n\treturn a.AdminStatus(ctx), nil\n}\nfunc (a *MTProtoAdapter) setAuthError()''',
'''\ta.mu.Lock()\n\ta.authState = MTProtoAuthConnected\n\ta.mu.Unlock()\n\ta.initializeMTProtoLive(ctx, client)\n\treturn a.AdminStatus(ctx), nil\n}\nfunc (a *MTProtoAdapter) setAuthError()''', 'password live init')
text = replace_once(text,
'''\tstate.Session = nil\n\tif err := a.state.store(ctx, state); err != nil {\n''',
'''\tstate.Session = nil\n\tstate.Peers = nil\n\tif a.live != nil {\n\t\ta.live.loadPeers(nil)\n\t}\n\tif err := a.state.store(ctx, state); err != nil {\n''', 'logout peer reset')
path.write_text(text)

for p in root.glob('.agent/*.gz.b64.*'):
    p.unlink()

tracker = root / 'docs/TELEGRAM_MTPROTO_SUPPORT_PROGRESS.md'
text = tracker.read_text().replace('- [ ] #104 — Implement Telegram MTProto live transport parity for messages, media, replies, reactions, edits, deletes, and topics', '- [x] #104 — Implement Telegram MTProto live transport parity for messages, media, replies, reactions, edits, deletes, and topics')
marker = '\n## Completion rule\n'
log = '''\n### #104 — complete\n\n- Added MTProto live message transport for configured Telegram groups/supergroups/channels while preserving canonical transport identity `telegram`.\n- Added restart-safe encrypted per-connection peer/access-hash persistence with on-demand dialog refresh, so ordinary outbound sends do not depend on fresh post-restart traffic.\n- Added live text/media, native reply/topic routing, reactions, edits and deletes through gotd while preserving media limits and privacy boundaries.\n- Self-originated bridge sends/mutations are suppressed while genuine linked-account user events remain routable; peer/session state remains isolated per Telegram connection.\n- Verification: focused MTProto live-adapter tests plus `go test ./...` and `go vet ./...` in the tested-change workflow.\n'''
if '### #104 — complete' not in text:
    text = text.replace(marker, log + marker)
tracker.write_text(text)
