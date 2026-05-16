package server

import (
	"time"

	"cen-demo/client"
	"cen-demo/pool"

	"cen-demo/protocol"
	mytype "cen-demo/type"

	"github.com/google/uuid"

	"github.com/lesismal/nbio/nbhttp/websocket"
	"google.golang.org/protobuf/proto"
)

func (s *Server) onOpen(conn *websocket.Conn) {
	clientID := uuid.New().String()
	c := client.New(clientID, conn)
	s.Hub.Add(c)
	conn.SetReadDeadline(time.Now().Add(10 * time.Minute))
}

func (s *Server) onClose(conn *websocket.Conn, err error) {
	if session := conn.Session(); session != nil {
		if c, ok := session.(*client.Client); ok {
			s.Hub.Remove(c.ID())
			c.Close()
		}
	}
}

func (s *Server) onMessage(conn *websocket.Conn, messageType websocket.MessageType, data []byte) {
	conn.SetReadDeadline(time.Now().Add(10 * time.Minute))
	if messageType != websocket.BinaryMessage {
		return
	}
	session := conn.Session()
	if session == nil {
		return
	}
	cli := session.(*client.Client)

	req := pool.GetClientMessage()
	defer pool.PutClientMessage(req)

	if err := proto.Unmarshal(data, req); err != nil {
		return
	}
	switch payload := req.Payload.(type) {
	case *protocol.ClientMessage_Subscribe:
		ch := payload.Subscribe.Channel
		s.Hub.Subscribe(cli, ch)

		s.WorkerPool.Submit(func() {
			s.Engine.AddPresence(ch, mytype.ClientInfo{
				ClientID: cli.ID(),
				UserID:   "anon",
			}, time.Now().Add(60*time.Second).Unix())
		})

		s.sendReply(cli, "subscribed to "+ch)
	case *protocol.ClientMessage_Publish:
		ch := payload.Publish.Channel
		rawData := payload.Publish.Data

		s.WorkerPool.Submit(func() {
			s.Engine.AddHistory(ch, rawData, 100)

			pushMsg := pool.GetServerMessage()
			pushMsg.Payload = &protocol.ServerMessage_Push{
				Push: &protocol.PushMessage{
					Channel: ch,
					Data:    rawData,
				},
			}
			bytesOut, _ := proto.Marshal(pushMsg)
			pool.PutServerMessage(pushMsg)

			s.Hub.Broadcast(ch, bytesOut)
			s.Broker.Publish(ch, bytesOut)
		})

	}
}

func (s *Server) sendReply(cli *client.Client, text string) {
	msg := pool.GetServerMessage()
	msg.Payload = &protocol.ServerMessage_Reply{
		Reply: &protocol.ReplyMessage{
			Text: text,
		},
	}
	bytesOut, _ := proto.Marshal(msg)
	pool.PutServerMessage(msg)
	cli.Write(bytesOut)
}
