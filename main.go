package main

import (
	"log"
	"minicentrifugo/config"
	"minicentrifugo/hub"
	"minicentrifugo/internal"

	"github.com/gin-gonic/gin"
	"go-micro.dev/v4/server"
)

func main() {

	internal.InitNATS()

	h := hub.New()

	srv := server.NewServer(h)

	go srv.broker.Subscribe(h)

	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()

	srv.RegisterRoutes(r)

	log.Printf("miniCentrifugo v2 started at :%s (Redis + NATS JetStream)", config.C.Port)
	log.Fatal(r.Run(":" + config.C.Port))
}
