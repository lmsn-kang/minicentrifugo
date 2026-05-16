package internal

import (
	"cen-demo/config"
	"log"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
)

var NC *nats.Conn
var JS nats.JetStreamContext

func InitNATS() {
	var err error
	NC, err = nats.Connect(config.C.NatsURL)
	if err != nil {
		log.Fatal(err)
	}

	JS, err = NC.JetStream()
	if err != nil {
		log.Fatal(err)
	}

	streamName := "CENTRIFUGO"

	_ = JS.DeleteStream(streamName)

	_, err = JS.AddStream(&nats.StreamConfig{
		Name:      "CENTRIFUGO",
		Subjects:  []string{"centrifugo.publish.>"},
		Retention: nats.WorkQueuePolicy,
		MaxAge:    24 * time.Hour,
	})
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		log.Fatal(err)
	}
}
