package broker

import (
	"cen-demo/hub"
	"cen-demo/internal"
	"log"

	"github.com/nats-io/nats.go"
)

type NatsBroker struct{}

func NewNatsBroker() *NatsBroker {
	return &NatsBroker{}
}

func (b *NatsBroker) Publish(channel string, data []byte) error {
	subject := "centrifugo.publish." + channel
	_, err := internal.JS.Publish(subject, data)
	return err
}
func (b *NatsBroker) Subscribe(h interface{}) {
	subject := "centrifugo.publish.>"
	sub, err := internal.JS.Subscribe(subject, func(m *nats.Msg) {
		channel := m.Subject[len("centrifugo.publish."):]
		h.(*hub.Hub).Broadcast(channel, m.Data)
		m.Ack()
	}, nats.ManualAck())
	if err != nil {
		log.Fatal(err)
	}
	_ = sub
}
