package whatsapp

import (
	"sync"
	"time"

	"github.com/vm75/message-sync/internal/transport"
)

const (
	lifecycleMarkerTTL = 2 * time.Minute
	lifecycleMarkerCap = 1024
)

type lifecycleMarker struct {
	endpoint transport.EndpointID
	target   string
	kind     string
	emoji    string
	created  time.Time
}

type lifecycleSuppression struct {
	mu      sync.Mutex
	markers map[string]lifecycleMarker
}

func newLifecycleSuppression() *lifecycleSuppression {
	return &lifecycleSuppression{markers: make(map[string]lifecycleMarker)}
}

func lifecycleMarkerKey(endpoint transport.EndpointID, target, kind, emoji string) string {
	return string(endpoint) + "\x00" + target + "\x00" + kind + "\x00" + emoji
}

func (s *lifecycleSuppression) mark(endpoint transport.EndpointID, target, kind, emoji string, now time.Time) {
	if s == nil || endpoint == "" || target == "" || kind == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(now)
	if len(s.markers) >= lifecycleMarkerCap {
		var oldestKey string
		var oldest time.Time
		for key, marker := range s.markers {
			if oldestKey == "" || marker.created.Before(oldest) {
				oldestKey, oldest = key, marker.created
			}
		}
		delete(s.markers, oldestKey)
	}
	s.markers[lifecycleMarkerKey(endpoint, target, kind, emoji)] = lifecycleMarker{endpoint: endpoint, target: target, kind: kind, emoji: emoji, created: now}
}

func (s *lifecycleSuppression) cancel(endpoint transport.EndpointID, target, kind, emoji string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.markers, lifecycleMarkerKey(endpoint, target, kind, emoji))
}

func (s *lifecycleSuppression) consume(endpoint transport.EndpointID, target, kind, emoji string, now time.Time) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(now)
	key := lifecycleMarkerKey(endpoint, target, kind, emoji)
	if _, ok := s.markers[key]; !ok {
		return false
	}
	delete(s.markers, key)
	return true
}

func (s *lifecycleSuppression) cleanupLocked(now time.Time) {
	cutoff := now.Add(-lifecycleMarkerTTL)
	for key, marker := range s.markers {
		if marker.created.Before(cutoff) {
			delete(s.markers, key)
		}
	}
}
