package client

import (
	"sync"

	"github.com/lesismal/nbio/nbhttp/websocket"
)

type Client struct {
	id        string
	mu        sync.RWMutex
	channels  map[string]bool
	Conn      *websocket.Conn
	writeCh   chan []byte
	closeCh   chan struct{}
	closeOnce sync.Once
}

func New(id string, conn *websocket.Conn) *Client {
	c := &Client{
		id:       id,
		Conn:     conn,
		channels: make(map[string]bool),
		writeCh:  make(chan []byte, 256),
		closeCh:  make(chan struct{}),
	}
	conn.SetSession(c)
	go c.writeLoop()
	return c
}

func (c *Client) ID() string { return c.id }

func (c *Client) Subscribe(ch string) {
	c.mu.Lock()
	c.channels[ch] = true
	c.mu.Unlock()
}

func (c *Client) Unsubscribe(ch string) {
	c.mu.Lock()
	delete(c.channels, ch)
	c.mu.Unlock()
}

func (c *Client) GetSubscriptions() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	subs := make([]string, 0, len(c.channels))
	for ch := range c.channels {
		subs = append(subs, ch)
	}
	return subs
}

func (c *Client) IsSubscribed(ch string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.channels[ch]
}

func (c *Client) writeLoop() {
	for {
		select {
		case data := <-c.writeCh:
			if c.Conn == nil {
				return
			}
			if err := c.Conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
				c.Close()
				return
			}
		case <-c.closeCh:
			return
		}
	}
}

func (c *Client) Write(data []byte) bool {
	select {
	case c.writeCh <- data:
		return true
	case <-c.closeCh:
		return false
	default:
		go c.Close()
		return false
	}
}

func (c *Client) Close() {
	c.closeOnce.Do(func() {
		close(c.closeCh)
		if c.Conn != nil {
			c.Conn.Close()
		}
	})
}