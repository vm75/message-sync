package whatsapp

import (
	"fmt"
	"sync"
	"testing"
)

func TestParticipantCache_Basic(t *testing.T) {
	c := newParticipantCache(2)

	// Add first item
	c.Add("msg1", "jid1")
	if val, ok := c.Get("msg1"); !ok || val != "jid1" {
		t.Errorf("expected jid1, got %v (ok=%v)", val, ok)
	}

	// Add second item
	c.Add("msg2", "jid2")
	if val, ok := c.Get("msg2"); !ok || val != "jid2" {
		t.Errorf("expected jid2, got %v", val)
	}

	// Add third item, should evict first
	c.Add("msg3", "jid3")
	if _, ok := c.Get("msg1"); ok {
		t.Error("expected msg1 to be evicted")
	}
	if val, ok := c.Get("msg3"); !ok || val != "jid3" {
		t.Errorf("expected jid3, got %v", val)
	}
}

func TestParticipantCache_Update(t *testing.T) {
	c := newParticipantCache(2)
	c.Add("msg1", "jid1")
	c.Add("msg1", "jid2") // update

	if val, ok := c.Get("msg1"); !ok || val != "jid2" {
		t.Errorf("expected jid2 after update, got %v", val)
	}

	// Adding one more should not evict since we updated, we only have 1 item in queue
	c.Add("msg2", "jid3")
	if _, ok := c.Get("msg1"); !ok {
		t.Error("expected msg1 to still exist")
	}
}

func TestParticipantCache_Concurrency(t *testing.T) {
	c := newParticipantCache(100)
	var wg sync.WaitGroup

	// Concurrently add and get
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("msg%d", i)
			c.Add(id, "jid")
			c.Get(id)
		}(i)
	}

	wg.Wait()
}
