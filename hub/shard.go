package hub

import (
	"cen-demo/client"
	"sync"
)

type Shard struct {
	mu      sync.RWMutex
	clients map[string]*client.Client
	subs    map[string]map[string]bool
}

func NewShard() *Shard {
	return &Shard{
		clients: make(map[string]*client.Client),
		subs:    make(map[string]map[string]bool),
	}
}

func (s *Shard) Add(c *client.Client) {
	s.mu.Lock()
	s.clients[c.ID()] = c
	s.mu.Unlock()
}

func (s *Shard) Remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, exists := s.clients[id]
	if !exists {
		return
	}
	delete(s.clients, id)
	userChannels := c.GetSubscriptions()
	for _, ch := range userChannels {
		if subMap, ok := s.subs[ch]; ok {
			delete(subMap, id)
			if len(subMap) == 0 {
				delete(s.subs, ch)
			}
		}
	}

}
func (s *Shard) Subscribe(c *client.Client, channel string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	c.Subscribe(channel)
	if _, ok := s.subs[channel]; !ok {
		s.subs[channel] = make(map[string]bool)
	}
	s.subs[channel][c.ID()] = true
}

func (s *Shard) Broadcast(channel string, data []byte) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	subscriberIDs, ok := s.subs[channel]
	if !ok {
		return
	}
	for id := range subscriberIDs {
		if c, exists := s.clients[id]; exists {
			c.Write(data)
		}
	}

}
