package main

import (
	"flag"
	"log"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	pb "cen-demo/protocol"
)

var (
	url         = "ws://127.0.0.1:8000/ws"
	concurrency = flag.Int("c", 2000, "并发连接数")
	channelName = "bench_proto"
)

var (
	msgReceived uint64
	msgSent     uint64
)

func main() {
	flag.Parse()

	log.Printf("开始压测: 目标=%s, 并发=%d, 协议=Protobuf", url, *concurrency)

	var wg sync.WaitGroup
	go statsLoop()

	for i := 0; i < *concurrency; i++ {
		wg.Add(1)

		go func(id int) {
			defer wg.Done()
			runClient(id)
		}(i)

		if i%100 == 0 {
			time.Sleep(50 * time.Millisecond)
		}
	}

	wg.Wait()
}

func runClient(id int) {
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		log.Printf("Client %d 连接失败: %v", id, err)
		return
	}
	defer conn.Close()

	subMsg := &pb.ClientMessage{
		Payload: &pb.ClientMessage_Subscribe{
			Subscribe: &pb.SubscribeRequest{
				Channel: channelName,
			},
		},
	}

	subData, err := proto.Marshal(subMsg)
	if err != nil {
		log.Println("Marshal error:", err)
		return
	}

	if err := conn.WriteMessage(websocket.BinaryMessage, subData); err != nil {
		log.Printf("Client %d 订阅失败: %v", id, err)
		return
	}

	go func() {
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
			atomic.AddUint64(&msgReceived, 1)
		}
	}()

	if id == 0 {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()

		payload := make([]byte, 100)

		for range ticker.C {
			rand.Read(payload)

			pubMsg := &pb.ClientMessage{
				Payload: &pb.ClientMessage_Publish{
					Publish: &pb.PublishRequest{
						Channel: channelName,
						Data:    payload,
					},
				},
			}

			data, _ := proto.Marshal(pubMsg)

			if err := conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
				log.Println("Publish error:", err)
				return
			}
			atomic.AddUint64(&msgSent, 1)
		}
	} else {
		select {}
	}
}

func statsLoop() {
	ticker := time.NewTicker(1 * time.Second)
	for range ticker.C {
		recv := atomic.SwapUint64(&msgReceived, 0)
		sent := atomic.SwapUint64(&msgSent, 0)

		log.Printf("[Stats] Sent: %d msg/s | Received: %d msg/s", sent, recv)
	}
}
