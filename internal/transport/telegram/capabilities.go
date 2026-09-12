package telegram

import (
	"errors"
	"strings"
)

type DiscoveryMode string

const (
	DiscoveryObserved DiscoveryMode = "observed"
	DiscoveryFull     DiscoveryMode = "full"
)

type Capabilities struct {
	ChatDiscovery     DiscoveryMode `json:"chatDiscovery"`
	TopicDiscovery    DiscoveryMode `json:"topicDiscovery"`
	HistoryRecovery   bool          `json:"historyRecovery"`
	PrivacyModeStatus bool          `json:"privacyModeStatus"`
	Polls             bool          `json:"polls"`
}

var ErrMTProtoAdapterUnavailable = errors.New("Telegram MTProto connection requires authentication before the transport can start")

func CapabilitiesForIntegrationMode(mode string) Capabilities {
	if strings.EqualFold(strings.TrimSpace(mode), "mtproto") {
		return Capabilities{
			ChatDiscovery:     DiscoveryFull,
			TopicDiscovery:    DiscoveryFull,
			HistoryRecovery:   true,
			PrivacyModeStatus: false,
			Polls:             true,
		}
	}
	return Capabilities{
		ChatDiscovery:     DiscoveryObserved,
		TopicDiscovery:    DiscoveryObserved,
		HistoryRecovery:   false,
		PrivacyModeStatus: true,
		Polls:             true,
	}
}
