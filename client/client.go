package client

import (
	"sync"

	"github.com/gorilla/websocket"
)

type Client struct {
	id       string
	mu       sync.RWMutex
	channels map[string]bool
	send     chan []byte
	conn     *websocket.Conn
}

func New(id string, conn *websocket.Conn) *Client {
	return &Client{
		id:       id,
		conn:     conn,
		channels: make(map[string]bool),
		send:     make(chan []byte, 256),
	}
}

func (c *Client) ID() string { return c.id }

func (c *Client) IsSubscribe(ch string) {
	c.mu.Lock()
	c.channels[ch] = true
	c.mu.Unlock()
}

func (c *Client) IsSubscribed(ch string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.channels[ch]
}

func (c *Client) Send() chan<- []byte { return c.send }

func (c *Client) Close() {
	close(c.send)
	c.conn.Close()
}
