package hub

import (
	"minicentrifugo/client"
	"sync/atomic"
)

const shardCount = 256

type Hub struct {
	shards []*Shard
	mask   uint64
	count  uint64
}

func New() *Hub {
	h := &Hub{
		shards: make([]*Shard, shardCount),
		mask:   uint64(shardCount - 1),
	}
	for i := 0; i < shardCount; i++ {
		h.shards[i] = NewShard()
	}
	return h
}

func (h *Hub) shardIndex(clientID string) uint64 {
	hash := uint64(14695981039346656037)
	for _, b := range []byte(clientID) {
		hash ^= uint64(b)
		hash *= 1099511628211
	}
	return hash & h.mask
}

func (h *Hub) Add(c *client.Client) {
	idx := h.shardIndex(c.ID)
	h.shards[idx].Add(c)
	atomic.AddUint64(&h.count, 1)
}

func (h *Hub) Remove(clientID string) {
	idx := h.shardIndex(clientID)
	h.shards[idx].Remove(clientID)
	atomic.AddUint64(&h.count, ^uint64(0))
}

func (h *Hub) Broadcast(channel string, data []byte) {
	for i := 0; i < shardCount; i++ {
		h.shards[i].Broadcast(channel, data)
	}
}

func (h *Hub) TotalConnections() uint64 {
	return atomic.LoadUint64(&h.count)
}
