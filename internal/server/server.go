package server

import (
	"context"
	"fmt"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	_ "github.com/joho/godotenv/autoload"
	"net/http"
	"os"
	"strconv"
	"time"
	//"tellarr/internal/database"
)

type Server struct {
	port           int
	telegramClient *telegram.Client
	telegramCtx    context.Context
	//db database.Service
}

func NewServer() *http.Server {
	port, _ := strconv.Atoi(os.Getenv("PORT"))
	appId, _ := strconv.Atoi(os.Getenv("APP_ID"))
	telegramClient := telegram.NewClient(appId, os.Getenv("APP_HASH"), telegram.Options{
		SessionStorage: &session.FileStorage{
			Path: "session.json",
		},
	})

	NewServer := &Server{
		port:           port,
		telegramClient: telegramClient,
	}

	go NewServer.telegramClient.Run(context.Background(), func(ctx context.Context) error {
		NewServer.telegramCtx = ctx
		fmt.Println("telegram client running")
		<-ctx.Done()
		fmt.Println("telegram client stopped")
		return nil
	})

	// Declare Server config
	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", NewServer.port),
		Handler:      NewServer.RegisterRoutes(),
		IdleTimeout:  time.Minute,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	return server
}
