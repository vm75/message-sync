package whatsapp

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/transport"
	"go.mau.fi/whatsmeow"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type lockedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func TestConfigureDeviceProps(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		t.Setenv("WHATSAPP_DEVICE_NAME", "")
		name := configureDeviceProps("")
		if name != defaultDeviceName || waStore.DeviceProps.GetOs() != defaultDeviceName {
			t.Fatalf("expected device os %q, got %q (returned: %q)", defaultDeviceName, waStore.DeviceProps.GetOs(), name)
		}
	})

	t.Run("env_override", func(t *testing.T) {
		t.Setenv("WHATSAPP_DEVICE_NAME", "EnvBridge")
		name := configureDeviceProps("")
		if name != "EnvBridge" || waStore.DeviceProps.GetOs() != "EnvBridge" {
			t.Fatalf("expected device os %q, got %q (returned: %q)", "EnvBridge", waStore.DeviceProps.GetOs(), name)
		}
	})

	t.Run("preferred_override", func(t *testing.T) {
		t.Setenv("WHATSAPP_DEVICE_NAME", "EnvBridge")
		name := configureDeviceProps("CustomSettingsBridge")
		if name != "CustomSettingsBridge" || waStore.DeviceProps.GetOs() != "CustomSettingsBridge" {
			t.Fatalf("expected device os %q, got %q (returned: %q)", "CustomSettingsBridge", waStore.DeviceProps.GetOs(), name)
		}
	})
}

func TestDisablePlaintextPersistence(t *testing.T) {
	client := &whatsmeow.Client{
		EnableDecryptedEventBuffer: true,
		UseRetryMessageStore:       true,
	}
	disablePlaintextPersistence(client)
	if client.EnableDecryptedEventBuffer {
		t.Fatal("decrypted event buffer must be disabled")
	}
	if client.UseRetryMessageStore {
		t.Fatal("retry plaintext message store must be disabled")
	}
}

func TestFormatWhatsAppTextUsesBoldItalicSyntaxForLiveResults(t *testing.T) {
	input := "***Aggregated anonymised live results***\nQuestion\nOption — 1"
	want := "*_Aggregated anonymised live results_*\nQuestion\nOption — 1"
	if got := formatWhatsAppText(input); got != want {
		t.Fatalf("formatted text = %q, want %q", got, want)
	}
}

func TestRevokeSenderUsesCachedParticipantForOtherMessages(t *testing.T) {
	cache := newParticipantCache(4)
	cache.Add("message-id", "15551234567@s.whatsapp.net")

	sender, err := revokeSender(transport.MessageRef{RemoteMessageID: "message-id"}, cache)
	if err != nil {
		t.Fatal(err)
	}
	if got := sender.String(); got != "15551234567@s.whatsapp.net" {
		t.Fatalf("revoke sender = %q", got)
	}

	self, err := revokeSender(transport.MessageRef{IsTargetFromMe: true}, cache)
	if err != nil {
		t.Fatal(err)
	}
	if !self.IsEmpty() {
		t.Fatalf("self revoke sender = %q, want empty", self.String())
	}
}

func TestWhatsAppCheckpointUsesEndpointAndMessageTimestamp(t *testing.T) {
	timestamp := time.Unix(1_700_000_123, 456).UTC()
	cp := whatsappCheckpoint("wa-team", timestamp)
	if !cp.Valid || cp.StreamKey != "wa-team" || cp.Position != timestamp.UnixNano() || !cp.EventTimestamp.Equal(timestamp) {
		t.Fatalf("checkpoint = %#v", cp)
	}
	if got := whatsappCheckpoint("wa-team", time.Time{}); got.Valid {
		t.Fatalf("zero timestamp unexpectedly produced checkpoint: %#v", got)
	}
}

func TestSortHistoryMessagesOldestFirstWithProtocolOrderTieBreak(t *testing.T) {
	newMessage := func(timestamp time.Time) *events.Message {
		return &events.Message{Info: types.MessageInfo{Timestamp: timestamp}}
	}
	old := time.Unix(100, 0)
	messages := []parsedHistoryMessage{
		{message: newMessage(old.Add(time.Hour)), order: 1},
		{message: newMessage(old), order: 2},
		{message: newMessage(old), order: 1},
	}
	sortHistoryMessages(messages)
	if !messages[0].message.Info.Timestamp.Equal(old) || messages[0].order != 1 || messages[1].order != 2 || !messages[2].message.Info.Timestamp.Equal(old.Add(time.Hour)) {
		t.Fatalf("history order = %#v", messages)
	}
}

func TestOpenSessionStoreCreatesRestrictedWhatsAppDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "whatsapp.db")
	container, device, err := openSessionStore(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if device == nil {
		_ = container.Close()
		t.Fatal("device store is nil")
	}
	if err := container.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("whatsapp.db mode = %o, want 600", got)
	}
}

func TestAdapterUnauthenticatedStartupDoesNotBlock(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "whatsapp.db")

	secret := []byte("0123456789abcdef0123456789abcdef")
	hasher, err := identity.New(secret)
	if err != nil {
		t.Fatal(err)
	}

	opts := Options{
		DatabasePath: dbPath,
		GroupJIDs: map[string]string{
			"g1": "123456789@g.us",
		},
		Hasher:           hasher,
		UsernameMode:     config.UsernameModePushName,
		Logger:           slog.Default(),
		EnableTerminalQR: false,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	adapter, err := Open(ctx, opts)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer adapter.Close()

	status := adapter.Status(context.Background())
	if status.Status != "unpaired" {
		t.Fatalf("expected status unpaired, got %s", status.Status)
	}
	if status.IsLoggedIn || status.IsConnected {
		t.Fatalf("expected not logged in and not connected: %+v", status)
	}
}

func TestAdapterConsumeQREventsAndLifecycle(t *testing.T) {
	var logBuf lockedBuffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))

	adapter := &Adapter{
		logger: logger,
	}

	// 1. Initial status
	status := adapter.Status(context.Background())
	if status.Status != "unpaired" {
		t.Fatalf("expected unpaired status, got %s", status.Status)
	}

	qrChan := make(chan whatsmeow.QRChannelItem, 5)
	qrCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go adapter.consumeQR(qrCtx, qrChan)

	// 2. Send "code" event
	qrChan <- whatsmeow.QRChannelItem{
		Event:   "code",
		Code:    "2@sensitive-qr-pairing-payload",
		Timeout: 30 * time.Second,
	}

	// Wait for code to be processed
	for i := 0; i < 50; i++ {
		status = adapter.Status(context.Background())
		if status.Status == "pairing" && status.QRCode == "2@sensitive-qr-pairing-payload" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status.Status != "pairing" || status.QRCode != "2@sensitive-qr-pairing-payload" {
		t.Fatalf("expected pairing with QR code, got %+v", status)
	}

	// Verify sensitive QR payload is NOT logged
	logged := logBuf.String()
	if strings.Contains(logged, "2@sensitive-qr-pairing-payload") {
		t.Fatalf("log leaked raw QR code: %s", logged)
	}
	if !strings.Contains(logged, "whatsapp_qr_generated") {
		t.Fatalf("expected safe log whatsapp_qr_generated: %s", logged)
	}

	// 3. Send "success" event
	qrChan <- whatsmeow.QRChannelItem{
		Event: "success",
	}
	for i := 0; i < 50; i++ {
		status = adapter.Status(context.Background())
		if status.Status == "unpaired" && status.QRCode == "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(logBuf.String(), "whatsapp_pairing_success") {
		t.Fatalf("expected safe log whatsapp_pairing_success: %s", logBuf.String())
	}

	// 4. Test CancelPair
	if err := adapter.CancelPair(context.Background()); err != nil {
		t.Fatalf("CancelPair failed: %v", err)
	}
	if !strings.Contains(logBuf.String(), "whatsapp_pairing_cancelled") {
		t.Fatalf("expected safe log whatsapp_pairing_cancelled: %s", logBuf.String())
	}
}

func TestAdapterUpdateConfig(t *testing.T) {
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}

	norm, err := NewNormalizer(map[string]string{}, hasher, config.UsernameModePushName)
	if err != nil {
		t.Fatal(err)
	}

	adapter := &Adapter{
		normalizer: norm,
		targets:    make(map[transport.EndpointID]types.JID),
		events:     make(chan transport.Incoming, 10),
		logger:     slog.Default(),
	}

	// Update config with 2 new groups
	newCfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"t1": {Transport: config.TransportWhatsApp, RemoteID: "123456789@g.us"},
			"t2": {Transport: config.TransportWhatsApp, RemoteID: "987654321@g.us"},
		},
		SyncSets: []config.SyncSet{
			{ID: "ss", Endpoints: []string{"t1", "t2"}},
		},
		Identity: config.Identity{UsernameMode: config.UsernameModePushName},
		Media:    config.Media{MaxSizeMB: 50, Enabled: true},
		Recovery: config.Recovery{MaxAgeHours: 12, MaxMessagesPerGroup: 100},
	}

	if err := adapter.UpdateConfig(newCfg); err != nil {
		t.Fatalf("UpdateConfig failed: %v", err)
	}

	adapter.mu.Lock()
	if len(adapter.targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(adapter.targets))
	}
	if adapter.mediaMaxBytes != 50*1024*1024 {
		t.Fatalf("expected mediaMaxBytes 50MB, got %d", adapter.mediaMaxBytes)
	}
	if adapter.recoveryMaxCount != 100 {
		t.Fatalf("expected recoveryMaxCount 100, got %d", adapter.recoveryMaxCount)
	}
	adapter.mu.Unlock()
}

func TestAdapterReact(t *testing.T) {
	adapter := &Adapter{
		targets: map[transport.EndpointID]types.JID{
			"t1": types.NewJID("123456789", types.GroupServer),
		},
		logger: slog.Default(),
	}

	// Unknown endpoint returns error
	err := adapter.React(context.Background(), transport.Reaction{
		Endpoint:       "unknown",
		TargetRemoteID: "msg-1",
		Emoji:          "👍",
	})
	if err == nil {
		t.Fatal("expected error for unknown endpoint")
	}

	// Uninitialized client returns error
	err = adapter.React(context.Background(), transport.Reaction{
		Endpoint:       "t1",
		TargetRemoteID: "msg-1",
		Emoji:          "👍",
	})
	if err == nil {
		t.Fatal("expected error for uninitialized client")
	}
}

func TestAdapterLogout(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "whatsapp.db")

	secret := []byte("0123456789abcdef0123456789abcdef")
	hasher, err := identity.New(secret)
	if err != nil {
		t.Fatal(err)
	}

	opts := Options{
		DatabasePath: dbPath,
		GroupJIDs: map[string]string{
			"g1": "123456789@g.us",
		},
		Hasher:           hasher,
		UsernameMode:     config.UsernameModePushName,
		Logger:           slog.Default(),
		EnableTerminalQR: false,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	adapter, err := Open(ctx, opts)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer adapter.Close()

	if err := adapter.Logout(context.Background()); err != nil {
		t.Fatalf("Logout failed: %v", err)
	}

	status := adapter.Status(context.Background())
	if status.IsLoggedIn || status.IsConnected {
		t.Fatalf("expected logged out and disconnected status: %+v", status)
	}
}

func TestBuildClearChatPatch(t *testing.T) {
	jid := types.NewJID("123456789", "g.us")
	cutoff := time.Unix(1700000000, 0).UTC()

	patch := BuildClearChatPatch(jid, cutoff, true)
	if patch.Type != "regular_high" {
		t.Errorf("patch.Type = %s, want regular_high", patch.Type)
	}
	if len(patch.Mutations) != 1 {
		t.Fatalf("expected 1 mutation, got %d", len(patch.Mutations))
	}
	mut := patch.Mutations[0]
	if len(mut.Index) != 4 || mut.Index[0] != "clearChat" || mut.Index[1] != jid.String() || mut.Index[3] != "1" {
		t.Errorf("unexpected mutation Index: %+v", mut.Index)
	}
	if mut.Value == nil || mut.Value.ClearChatAction == nil || mut.Value.ClearChatAction.MessageRange == nil {
		t.Fatalf("expected ClearChatAction value with MessageRange, got %+v", mut.Value)
	}
	if mut.Value.ClearChatAction.MessageRange.GetLastMessageTimestamp() != 1700000000 {
		t.Errorf("got timestamp %d, want 1700000000", mut.Value.ClearChatAction.MessageRange.GetLastMessageTimestamp())
	}
}

func TestAdapterClearChatUnauthenticated(t *testing.T) {
	var a *Adapter
	if err := a.ClearChatOlderThan(context.Background(), types.NewJID("1", "g.us"), time.Now(), true); err == nil {
		t.Fatal("expected error for nil adapter")
	}

	adapter := &Adapter{}
	if err := adapter.ClearChatOlderThan(context.Background(), types.NewJID("1", "g.us"), time.Now(), true); err == nil {
		t.Fatal("expected error for unauthenticated client")
	}

	if _, err := adapter.ClearSyncSetChats(context.Background(), []types.JID{types.NewJID("1", "g.us")}, time.Now()); err == nil {
		t.Fatal("expected error for ClearSyncSetChats with unauthenticated client")
	}

	if adapter.IsLoggedIn() {
		t.Fatal("expected IsLoggedIn to return false")
	}
}

func TestFilterAndFormatJoinedGroups_ExcludesCommunitiesAndAnnouncements(t *testing.T) {
	c1JID := types.NewJID("100", "g.us")
	c2JID := types.NewJID("200", "g.us")

	groups := []*types.GroupInfo{
		// Community 1 parent
		{
			JID:         c1JID,
			GroupName:   types.GroupName{Name: "ms-test1"},
			GroupParent: types.GroupParent{IsParent: true},
		},
		// Community 1 Announcement (default sub group, shares community name)
		{
			JID:               types.NewJID("101", "g.us"),
			GroupName:         types.GroupName{Name: "ms-test1"},
			GroupLinkedParent: types.GroupLinkedParent{LinkedParentJID: c1JID},
			GroupIsDefaultSub: types.GroupIsDefaultSub{IsDefaultSubGroup: true},
			GroupAnnounce:     types.GroupAnnounce{IsAnnounce: true},
		},
		// Community 1 General
		{
			JID:               types.NewJID("102", "g.us"),
			GroupName:         types.GroupName{Name: "General"},
			GroupLinkedParent: types.GroupLinkedParent{LinkedParentJID: c1JID},
		},
		// Community 1 g1
		{
			JID:               types.NewJID("103", "g.us"),
			GroupName:         types.GroupName{Name: "g1"},
			GroupLinkedParent: types.GroupLinkedParent{LinkedParentJID: c1JID},
		},
		// Community 2 parent
		{
			JID:         c2JID,
			GroupName:   types.GroupName{Name: "ms-test2"},
			GroupParent: types.GroupParent{IsParent: true},
		},
		// Community 2 Announcement (named Announcements)
		{
			JID:               types.NewJID("201", "g.us"),
			GroupName:         types.GroupName{Name: "Announcements"},
			GroupLinkedParent: types.GroupLinkedParent{LinkedParentJID: c2JID},
			GroupAnnounce:     types.GroupAnnounce{IsAnnounce: true},
		},
		// Community 2 General
		{
			JID:               types.NewJID("202", "g.us"),
			GroupName:         types.GroupName{Name: "General"},
			GroupLinkedParent: types.GroupLinkedParent{LinkedParentJID: c2JID},
		},
		// Community 2 g2
		{
			JID:               types.NewJID("203", "g.us"),
			GroupName:         types.GroupName{Name: "g2"},
			GroupLinkedParent: types.GroupLinkedParent{LinkedParentJID: c2JID},
		},
	}

	result := filterAndFormatJoinedGroups(groups, nil)

	if len(result) != 4 {
		t.Fatalf("expected 4 groups, got %d: %+v", len(result), result)
	}

	expected := []struct {
		jid  string
		name string
	}{
		{jid: "102@g.us", name: "ms-test1:General"},
		{jid: "103@g.us", name: "ms-test1:g1"},
		{jid: "202@g.us", name: "ms-test2:General"},
		{jid: "203@g.us", name: "ms-test2:g2"},
	}

	for i, exp := range expected {
		if result[i].JID != exp.jid || result[i].Name != exp.name {
			t.Errorf("result[%d] = {JID: %s, Name: %s}, want {JID: %s, Name: %s}",
				i, result[i].JID, result[i].Name, exp.jid, exp.name)
		}
	}
}
