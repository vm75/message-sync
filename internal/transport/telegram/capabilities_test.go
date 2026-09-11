package telegram

import "testing"

func TestCapabilitiesForIntegrationMode(t *testing.T) {
	bot := CapabilitiesForIntegrationMode("bot")
	if bot.ChatDiscovery != DiscoveryObserved || bot.TopicDiscovery != DiscoveryObserved || bot.HistoryRecovery || !bot.PrivacyModeStatus || !bot.Polls {
		t.Fatalf("unexpected bot capabilities: %+v", bot)
	}
	mt := CapabilitiesForIntegrationMode("mtproto")
	if mt.ChatDiscovery != DiscoveryFull || mt.TopicDiscovery != DiscoveryFull || !mt.HistoryRecovery || mt.PrivacyModeStatus || !mt.Polls {
		t.Fatalf("unexpected mtproto capabilities: %+v", mt)
	}
}
