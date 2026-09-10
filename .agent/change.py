from pathlib import Path
p=Path('internal/transport/telegram/mtproto.go');s=p.read_text()
s=s.replace('\truntimeFactory mtprotoRuntimeFactory\n\n\tmu', '\truntimeFactory  mtprotoRuntimeFactory\n\tlifecycleCtx    context.Context\n\tlifecycleCancel context.CancelFunc\n\n\tmu',1)
s=s.replace('\tadapter := &MTProtoAdapter{\n', '\tlifecycleCtx, lifecycleCancel := context.WithCancel(ctx)\n\tadapter := &MTProtoAdapter{\n',1)
s=s.replace('\t\tstate: &mtprotoStateBox{raw: opts.MTProtoStateStore}, runtimeFactory: factory,\n\t\tauthState: MTProtoAuthDisconnected,', '\t\tstate: &mtprotoStateBox{raw: opts.MTProtoStateStore}, runtimeFactory: factory,\n\t\tlifecycleCtx: lifecycleCtx, lifecycleCancel: lifecycleCancel, authState: MTProtoAuthDisconnected,',1)
s=s.replace('\t\tadapter.startRuntime(ctx, state)\n', '\t\tadapter.startRuntime(lifecycleCtx, state)\n',1)
s=s.replace('\ta.stopRuntime()\n\ta.mu.Lock()\n\tif !a.closed {', '\tif a.lifecycleCancel != nil {\n\t\ta.lifecycleCancel()\n\t}\n\ta.stopRuntime()\n\ta.mu.Lock()\n\tif !a.closed {',1)
s=s.replace('\ta.startRuntime(ctx, current)\n\treturn a.AdminStatus(ctx), nil', '\ta.startRuntime(a.lifecycleCtx, current)\n\treturn a.AdminStatus(ctx), nil',1)
p.write_text(s)

p=Path('internal/transport/telegram/mtproto_test.go');s=p.read_text()
needle='func TestMTProtoInvalidCodeIsSanitizedAndCodeHashNotPersisted'
test='''func TestMTProtoConfigureRuntimeOutlivesRequestContext(t *testing.T) {
	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()
	store := &memoryMTStore{}
	f := &fakeMTAuth{}
	a, err := OpenMTProto(appCtx, Options{ConnectionID: "tg-lifecycle", Logger: testLogger(), MTProtoStateStore: store, mtprotoRuntimeFactory: func(int, string, gotdsession.Storage) mtprotoRuntime { return fakeMTRuntime{f} }})
	if err != nil { t.Fatal(err) }
	defer a.Close()
	reqCtx, reqCancel := context.WithCancel(context.Background())
	if _, err := a.ConfigureMTProto(reqCtx, 1, "hash", "+1000"); err != nil { t.Fatal(err) }
	reqCancel()
	if _, err := a.SendMTProtoCode(context.Background()); err != nil { t.Fatalf("runtime stopped with request context: %v", err) }
}

'''
if needle not in s: raise SystemExit('missing test insertion point')
s=s.replace(needle,test+needle,1);p.write_text(s)
