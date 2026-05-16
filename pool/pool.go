package pool

import (
	"sync"

	"cen-demo/protocol"

	"google.golang.org/protobuf/proto"
)

var clientMsgPool = sync.Pool{
	New: func() any {
		return &protocol.ClientMessage{}
	},
}

var serverMsgPool = sync.Pool{
	New: func() any {
		return &protocol.ServerMessage{}
	},
}

func GetClientMessage() *protocol.ClientMessage {
	return clientMsgPool.Get().(*protocol.ClientMessage)
}

func PutClientMessage(m *protocol.ClientMessage) {
	proto.Reset(m)
	clientMsgPool.Put(m)
}

func GetServerMessage() *protocol.ServerMessage {
	return serverMsgPool.Get().(*protocol.ServerMessage)
}

func PutServerMessage(m *protocol.ServerMessage) {
	proto.Reset(m)
	serverMsgPool.Put(m)
}