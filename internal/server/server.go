package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"tellarr/internal/database"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	_ "github.com/joho/godotenv/autoload"
)

type Server struct {
	port             int
	db               database.Service
	telegramSessions map[int64]*TelegramSession
	sessionRepo      database.SessionRepository
	dialogRepo       database.DialogsRepository
	userRepo         database.UserRepository
	tokenRepo        database.TokenRepository
	downloadRepo     database.DownloadsRepository
	settingsRepo     database.SettingsRepository
	dm               *DownloadManager
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

	poolMu  sync.Mutex
	dlPools map[int]tg.Invoker
}

func NewServer() *http.Server {
	slog.Info("starting server")
	port, _ := strconv.Atoi(os.Getenv("PORT"))
	appId, _ := strconv.Atoi(os.Getenv("APP_ID"))
	appHash := os.Getenv("APP_HASH")

	db := database.New()
	sessionRepo := database.NewSessionRepository(db.DB)
	dialogRepo := database.NewDialogsRepository(db.DB)
	userRepo := database.NewUserRespository(db.DB)
	tokenRepo := database.NewTokenRepository(db.DB)
	downloadRepo := database.NewDownloadsRepository(db.DB)
	settingsRepo := database.NewSettingsRepository(db.DB)

	downloadDir := os.Getenv("DOWNLOAD_DIR")
	if strings.TrimSpace(downloadDir) == "" {
		downloadDir = "./data/downloads"
	}
	// aria2c and the arrs resolve paths on their own; a relative DOWNLOAD_DIR
	// would be reinterpreted against their working directories, so pin it to
	// an absolute path once at startup.
	if abs, err := filepath.Abs(downloadDir); err == nil {
		downloadDir = abs
	}
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		// Non-fatal: aria2-backed downloads are written by the aria2 process,
		// which may have access where this container does not.
		slog.Error("cannot create download dir", "dir", downloadDir, "err", err)
	}
	slog.Info("download dir configured", "dir", downloadDir)

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
	// A value saved on the Settings page overrides the env default; it also
	// applies live at runtime, so this is just the starting point.
	maxParallel := defaultMaxParallelDownloads()
	if v, err := settingsRepo.Get(SettingMaxParallelDownloads); err != nil {
		slog.Error("failed to read download settings, using default", "err", err)
	} else if v != nil {
		if n, ok := parseParallelSetting(*v); ok {
			maxParallel = n
		} else {
			slog.Warn("invalid stored max parallel downloads, using default", "value", *v)
		}
	}

	NewServer := &Server{
		port:             port,
		telegramSessions: telegramSessions,
		db:               db,
		sessionRepo:      sessionRepo,
		dialogRepo:       dialogRepo,
		userRepo:         userRepo,
		tokenRepo:        tokenRepo,
		downloadRepo:     downloadRepo,
		settingsRepo:     settingsRepo,
		dm:               NewDownloadManager(downloadRepo, downloadDir, maxParallel),
		appId:            appId,
		appHash:          appHash,
	}
	// The download manager needs the server to re-resolve Telegram media
	// when queued downloads are promoted to active transfers.
	NewServer.dm.resolve = NewServer.resolveDownloadMedia

	for _, sessionId := range sessionIds {
		started := make(chan struct{})
		id := sessionId
		s := NewServer.telegramSessions[id]
		go func() {
			err := s.client.Run(context.Background(), func(ctx context.Context) error {
				s.mu.Lock()
				s.context = ctx
				s.mu.Unlock()
				if s.ready == nil {
					s.ready = started
					close(started)
				}
				fmt.Println("telegram client running")
				<-ctx.Done()
				fmt.Println("telegram client stopped")
				return nil
			})
			if err != nil {
				slog.Error("stored telegram session failed", "sessionId", id, "err", err)
			}
		}()
		select {
		case <-started:
			slog.Info(fmt.Sprintf("telegram session %d started", id))
		case <-time.After(10 * time.Second):
			// Do not take the whole app down for one dead session; the user
			// can reconnect from the UI wizard.
			slog.Error("failed to start stored telegram session in time", "sessionId", id)
			NewServer.mu.Lock()
			delete(NewServer.telegramSessions, id)
			NewServer.mu.Unlock()
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
