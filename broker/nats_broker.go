package broker

import (
	"log"
	"minicentrifugo/internal"

	"github.com/nats-io/nats.go"
)

type NatsBroker struct{}

func NewNatsBroker() *NatsBroker {
	return &NatsBroker{}
}

func (b *NatsBroker) Publish(channel string, data []byte) error {
	subject := "centrifugo.publish." + channel
	return internal.JS.Publish(subject, data)
}

func (b *NatsBroker) Subscribe(hub interface{}) {
	subject := "centrifugo.publish.>"
	sub, err := internal.JS.Subscribe(subject, func(m *nats.Msg) {
		channel := m.Subject[len("centrifugo.publish."):]

		hub.(*hub.Hub).Broadcast(channel, m.Data)
		m.Ack()
	}, nats.Durable("centrifugo-worker"), nats.ManualAck())
	if err != nil {
		log.Fatal("NATS subscribe failed:", err)
	}
	log.Printf("NATS subscribed to %s", subject)
	_ = sub
}
