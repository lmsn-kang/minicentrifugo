package server

import (
	"encoding/json"
	"minicentrifugo/broker"
	"minicentrifugo/client"
	"minicentrifugo/hub"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type Server struct {
	hub    *hub.Hub
	broker *broker.NatsBroker
}

func NewServer(h *hub.Hub) *Server {
	return &Server{
		hub:    h,
		broker: broker.NewNatsBroker(),
	}
}

func (s *Server) RegisterRoutes(r *gin.Engine) {
	r.GET("/", func(c *gin.Context) {
		c.String(200, `
            <h1>miniCentrifugo v2 (Redis + NATS) Running!</h1>
            <p>WebSocket: ws://localhost:8000/ws</p>
            <p>当前连接数: %d</p>
        `, s.hub.TotalConnections())
	})

	r.GET("/ws", s.handleWebSocket)

	r.GET("/presence/:channel", func(c *gin.Context) {
		ch := c.Param("channel")
		users, err := s.engine.Presence(ch)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{
			"count": len(users),
			"users": users,
		})
	})

	r.GET("/history/:channel", func(c *gin.Context) {
		ch := c.Param("channel")
		msgs, err := s.engine.History(ch, 50)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}

		var parsedMsgs []interface{}
		for _, m := range msgs {
			var tmp interface{}
			json.Unmarshal(m, &tmp)
			parsedMsgs = append(parsedMsgs, tmp)
		}

		c.JSON(200, gin.H{
			"history": parsedMsgs,
		})
	})
}

func (s *Server) handleWebSocket(c *gin.Context) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}

	clientID := uuid.New().String()
	client := client.New(clientID, conn)
	s.hub.Add(client)

	go s.writePump(client)
	s.readPump(client)
}

func (s *Server) writePump(cli *client.Client) {
	defer func() {
		s.hub.Remove(cli.ID())
		cli.Close()
	}()

	for data := range cli.Send() {
		if err := cli.conn.WriteMessage(websocket.TextMessage, data); err != nil {
			return
		}
	}
}

func (s *Server) readPump(cli *client.Client) {
	defer func() {
		s.hub.Remove(cli.ID())
		cli.Close()
	}()

	for {
		_, msg, err := cli.conn.ReadMessage()
		if err != nil {
			break
		}

		var cmd struct {
			Subscribe *struct {
				Channel string `json:"channel"`
			} `json:"subscribe,omitempty"`
			Publish *struct {
				Channel string          `json:"channel"`
				Data    json.RawMessage `json:"data"`
			} `json:"publish,omitempty"`
		}

		if err := json.Unmarshal(msg, &cmd); err != nil {
			continue
		}

		if cmd.Subscribe != nil {
			cli.Subscribe(cmd.Subscribe.Channel)
		}

		if cmd.Publish != nil {

			go func() {
				s.engine.AddHistory(channel, data, 100)
			}()

			s.hub.Broadcast(cmd.Publish.Channel, cmd.Publish.Data)
			s.broker.Publish(cmd.Publish.Channel, cmd.Publish.Data)
		}
	}
}
