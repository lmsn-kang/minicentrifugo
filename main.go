package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"cen-demo/config"
	"cen-demo/internal"

	"cen-demo/server"
)

func main() {
	internal.InitNATS()

	srv := server.NewServer()

	if err := srv.Start(":" + config.C.Port); err != nil {
		log.Fatalf("server start failed: %v", err)
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")
	srv.Stop()
	log.Println("Server exited")
}
