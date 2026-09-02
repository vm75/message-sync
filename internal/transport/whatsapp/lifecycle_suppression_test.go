package whatsapp

import (
	"testing"
	"time"
)

func TestLifecycleSuppressionConsumesOnlyMatchingEchoOnce(t *testing.T) {
	s := newLifecycleSuppression()
	now := time.Unix(100, 0)
	s.mark("wa", "remote-edit", "edit", "", now)
	if s.consume("wa", "remote-edit", "delete", "", now) {
		t.Fatal("different lifecycle kind consumed marker")
	}
	if !s.consume("wa", "remote-edit", "edit", "", now) {
		t.Fatal("matching echo was not suppressed")
	}
	if s.consume("wa", "remote-edit", "edit", "", now) {
		t.Fatal("marker was not consumed exactly once")
	}
}

func TestLifecycleSuppressionExpiresAndIsBounded(t *testing.T) {
	s := newLifecycleSuppression()
	now := time.Unix(100, 0)
	s.mark("wa", "stale", "delete", "", now)
	if s.consume("wa", "stale", "delete", "", now.Add(lifecycleMarkerTTL+time.Second)) {
		t.Fatal("stale marker suppressed a genuine action")
	}
	for i := 0; i < lifecycleMarkerCap+10; i++ {
		s.mark("wa", string(rune(i+1)), "edit", "", now)
	}
	s.mu.Lock()
	count := len(s.markers)
	s.mu.Unlock()
	if count > lifecycleMarkerCap {
		t.Fatalf("marker count=%d, cap=%d", count, lifecycleMarkerCap)
	}
}
