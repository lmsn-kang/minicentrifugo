package server

import (
	"context"
	"log"
	"net/http"
	"time"

	"cen-demo/broker"
	"cen-demo/engine"
	"cen-demo/hub"
	"cen-demo/worker"

	"github.com/gin-gonic/gin"
	"github.com/lesismal/nbio/nbhttp"
	nbiows "github.com/lesismal/nbio/nbhttp/websocket"
)

type Server struct {
	Hub        *hub.Hub
	Broker     *broker.NatsBroker
	Engine     engine.Engine
	NBEngine   *nbhttp.Engine
	WorkerPool *worker.Pool
}

func NewServer() *Server {
	h := hub.New()
	return &Server{
		Hub:        h,
		Broker:     broker.NewNatsBroker(),
		Engine:     engine.NewRedisEngine(),
		WorkerPool: worker.New(50, 1000),
	}
}

func (s *Server) Start(addr string) error {
	router := gin.New()
	router.GET("/status", func(c *gin.Context) {
		c.JSON(200, gin.H{"connections": s.Hub.TotalConnections()})
	})

	upgrader := nbiows.NewUpgrader()

	upgrader.OnOpen(s.onOpen)
	upgrader.OnMessage(s.onMessage)
	upgrader.OnClose(s.onClose)

	upgrader.CheckOrigin = func(r *http.Request) bool {
		return true
	}

	router.GET("/ws", func(c *gin.Context) {
		w := c.Writer
		r := c.Request
		_, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("upgrade error: %v", err)
			return
		}
	})

	nbConfig := nbhttp.Config{
		Network: "tcp",
		Addrs:   []string{addr},
		Handler: router,
		IOMod:   nbhttp.IOModMixed,
	}

	s.NBEngine = nbhttp.NewEngine(nbConfig)

	go s.Broker.Subscribe(s.Hub)

	log.Printf("NBIO Server starting on %s...", addr)
	return s.NBEngine.Start()
}

func (s *Server) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	if s.WorkerPool != nil {
		s.WorkerPool.Close()
	}

	if s.NBEngine != nil {
		s.NBEngine.Shutdown(ctx)
	}
}
