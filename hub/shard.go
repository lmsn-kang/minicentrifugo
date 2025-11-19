package hub

import (
	"minicentrifugo/client"
	"sync"
)

type Shard struct {
	mu      sync.RWMutex
	clients map[string]*client.Client
}

func NewShard() *Shard {
	return &Shard{clients: make(map[string]*client.Client)}
}

func (s *Shard) Add(c *client.Client) {
	s.mu.Lock()
	s.clients[c.ID] = c
	s.mu.Unlock()
}

func (s *Shard) Remove(id string) {
	s.mu.Lock()
	delete(s.clients, id)
	s.mu.Unlock()
}

func (s *Shard) Broadcast(channel string, data []byte) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.clients {
		if c.IsSubscribed(channel) {
			select {
			case c.Send <- data:
			default:
				c.Close()
			}
		}
	}
}

func (s *Shard) GetClientsInChannel(channel string) []*client.Client {
	var res []*client.Client
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.clients {
		if c.IsSubscribed(channel) {
			res = append(res, c)
		}
	}
	return res
}
