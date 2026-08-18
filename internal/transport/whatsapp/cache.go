package whatsapp

import (
	"sync"
)

type participantCache struct {
	mu       sync.Mutex
	capacity int
	cache    map[string]string
	queue    []string
}

func newParticipantCache(capacity int) *participantCache {
	return &participantCache{
		capacity: capacity,
		cache:    make(map[string]string),
		queue:    make([]string, 0, capacity),
	}
}

func (c *participantCache) Add(messageID, participantJID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.cache[messageID]; exists {
		c.cache[messageID] = participantJID
		return
	}

	if len(c.queue) >= c.capacity {
		oldest := c.queue[0]
		c.queue = c.queue[1:]
		delete(c.cache, oldest)
	}

	c.queue = append(c.queue, messageID)
	c.cache[messageID] = participantJID
}

func (c *participantCache) Get(messageID string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	jid, ok := c.cache[messageID]
	return jid, ok
}
