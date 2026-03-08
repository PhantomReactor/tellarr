package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"tellarr/internal/database"
	"time"

	"github.com/gotd/td/telegram"
	_ "github.com/joho/godotenv/autoload"
)

type Server struct {
	port             int
	db               database.Service
	telegramSessions map[int64]*TelegramSession
	sessionRepo      database.SessionRepository
	dialogRepo       database.DialogsRepository
	userRepo         database.UserRepository
	refreshTokenRepo database.RefreshTokenRepository
	dm               DownloadManager
	appId            int
	appHash          string
	mu               sync.RWMutex
}

type TelegramSession struct {
	mu      sync.RWMutex
	context context.Context
	client  *telegram.Client
	ready   chan struct{}
	err     error
}

func NewServer() *http.Server {
	slog.Info("starting server")
	port, _ := strconv.Atoi(os.Getenv("PORT"))
	appId, _ := strconv.Atoi(os.Getenv("APP_ID"))
	appHash := os.Getenv("APP_HASH")

	db := database.New()
	sessionRepo := database.NewSessionRepository(db.DB)
	sessionIds, err := sessionRepo.GetAllSessionIds()
	if err != nil {
		fmt.Println("ignoring exisiting sessions")
	}
	telegramSessions := make(map[int64]*TelegramSession)

	for _, sessionId := range sessionIds {
		telegramClient := telegram.NewClient(appId, appHash, telegram.Options{
			SessionStorage: &database.DBSessionStorage{
				SessionRepository: sessionRepo,
				SessionID:         sessionId,
			},
		})
		telegramSessions[sessionId] = &TelegramSession{client: telegramClient, context: nil}

	}
	NewServer := &Server{
		port:             port,
		telegramSessions: telegramSessions,
		db:               db,
		sessionRepo:      sessionRepo,
		dialogRepo:       database.NewDialogsRepository(db.DB),
		dm:               NewDownloadManger(),
		appId:            appId,
		appHash:          appHash,
	}

	for _, sessionId := range sessionIds {
		started := make(chan struct{})
		id := sessionId
		s := NewServer.telegramSessions[id]
		go func() {
			err := s.client.Run(context.Background(), func(ctx context.Context) error {
				s.mu.Lock()
				s.context = ctx
				s.mu.Unlock()
				s.ready = started
				close(started)
				fmt.Println("telegram client running")
				<-ctx.Done()
				fmt.Println("telegram client stopped")
				return nil
			})
			if err != nil {
				panic(err)
			}
		}()
		select {
		case <-started:
			fmt.Printf("telegram sesison %d started", id)
		case <-time.After(5 * time.Second):
			panic(fmt.Errorf("failed to start telegram session %d", sessionId))
		}
	}

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
