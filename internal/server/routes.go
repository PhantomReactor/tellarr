package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"tellarr/internal/database"
	db "tellarr/internal/database/models"
	"tellarr/internal/pkg/models"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/go-chi/httplog/v3"
	"github.com/google/uuid"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

func (s *Server) RegisterRoutes() http.Handler {
	r := chi.NewRouter()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	slog.SetLogLoggerLevel(slog.LevelError)
	r.Use(loggingMiddleware)
	r.Use(httplog.RequestLogger(logger, &httplog.Options{
		Level:  slog.LevelInfo,
		Schema: httplog.SchemaECS.Concise(true),
	}))
	r.Use(JSONContentType)

	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"https://*", "http://*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	r.Route("/api/telegram", func(r chi.Router) {
		r.Post("/code", s.RequestCode)
		r.Post("/verify", s.ValidateCode)
		r.Post("/channels", s.AddChannels)
		r.Get("/channels", s.ListChannels)
		r.Get("/messages", s.Search)
	})

	return r
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		requestID := r.Header.Get("requestID")
		if requestID == "" {
			requestID = uuid.New().String()
		}
		ctx = context.WithValue(ctx, middleware.RequestIDKey, requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func JSONContentType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) RequestCode(w http.ResponseWriter, r *http.Request) {
	slog.Info("received telegram login request")
	var request models.Request
	err := json.NewDecoder(r.Body).Decode(&request)
	if err != nil {
		slog.Error("error while decoding login request", "error", err)
		models.NewResponse(w, &models.Response{Message: "invalid credentials"}, http.StatusBadRequest)
		return
	}
	t, err := s.getTelegramClient(request.SessionId)
	if err != nil {
		slog.Error("error while starting telegram client", "error", err)
		models.NewResponse(w, &models.Response{Message: "error while starting telegram client"}, http.StatusInternalServerError)
		return
	}
	sessionRepo := s.sessionRepo
	sessionId, err := sessionRepo.CreateSession(db.Session{PhoneNumber: request.Phone, Active: true, CreatedAt: time.Now(), UpdatedAt: time.Now()})
	if err != nil {
		slog.Error("error while creating session", "error", err)
		models.NewResponse(w, &models.Response{Message: "error while creating sessions"}, http.StatusInternalServerError)
		return
	}
	status, err := t.client.Auth().Status(t.context)
	if err != nil {
		slog.Error(err.Error())
		models.NewResponse(w, &models.Response{Message: "auth error"}, http.StatusInternalServerError)
		return
	}

	if !status.Authorized {
		sentCode, err := t.client.Auth().SendCode(t.context, request.Phone, auth.SendCodeOptions{})
		if err != nil {
			slog.Error("error while sending otp", "error", err)
			models.NewResponse(w, &models.Response{Message: "unable to send failed"}, http.StatusInternalServerError)
			return
		}
		if authSentCode, ok := sentCode.(*tg.AuthSentCode); ok {
			t.phoneCodeHash = authSentCode.PhoneCodeHash
			slog.Debug(t.phoneCodeHash)
		}
	}
	slog.Info("telegram client running")
	models.NewResponse(w, &models.Response{SessionId: sessionId}, http.StatusAccepted)
}

func (s *Server) getTelegramClient(sessionId int) (*TelegramSession, error) {
	s.mu.RLock()
	if session, exisit := s.telegramSessions[sessionId]; exisit {
		s.mu.RUnlock()
		<-session.ready
		return session, nil
	}
	s.mu.RUnlock()
	s.mu.Lock()

	if session, exisit := s.telegramSessions[sessionId]; exisit {
		s.mu.Unlock()
		<-session.ready
		return session, nil
	}
	started := make(chan struct{})
	session := &TelegramSession{ready: started}
	s.telegramSessions[sessionId] = session
	s.mu.Unlock()
	telegramClient := telegram.NewClient(s.appId, s.appHash, telegram.Options{
		SessionStorage: &database.DBSessionStorage{
			SessionRepository: s.sessionRepo,
			SessionID:         sessionId,
		},
	})
	go func() {
		err := telegramClient.Run(context.Background(), func(ctx context.Context) error {
			session.client = telegramClient
			session.context = ctx
			close(started)
			fmt.Println("telegram client running")
			<-ctx.Done()
			fmt.Println("telegram client stopped")
			return nil
		})
		if err != nil {
			slog.Error("failed to start telegram session", "sessionId", sessionId, "err", err)
			session.err = err
			close(started)
			return
		}
	}()
	select {
	case <-started:
		if session.err != nil {
			return nil, session.err
		}
	case <-time.After(5 * time.Second):
		slog.Error("failed to start telegram session", "sessionId", sessionId)
		s.mu.Lock()
		delete(s.telegramSessions, sessionId)
		s.mu.Unlock()
		return nil, fmt.Errorf("failed to start telegram sesion for %d", sessionId)
	}
	return session, nil

}

func (s *Server) ValidateCode(w http.ResponseWriter, r *http.Request) {

	var request models.Request
	err := json.NewDecoder(r.Body).Decode(&request)
	if err != nil {
		slog.Error("json parse error", "error", err)
		models.NewResponse(w, &models.Response{Message: "request parse error"}, http.StatusBadRequest)
		return
	}
	t, err := s.getTelegramClient(request.SessionId)
	if err != nil {
		slog.Error("error while starting telegram client", "error", err)
		models.NewResponse(w, &models.Response{Message: "error while starting telegram client"}, http.StatusInternalServerError)
		return
	}
	authRes, err := t.client.Auth().SignIn(t.context, request.Phone, request.Code, request.PhoneHash)
	if err != nil && err == auth.ErrPasswordAuthNeeded {
		slog.Error("2FA required", "error", err)
		models.NewResponse(w, &models.Response{Message: "2FA required"}, http.StatusContinue)
		return
	}
	if err != nil {
		slog.Error("send code error", "error", err)
		models.NewResponse(w, &models.Response{Message: "invalid code"}, http.StatusBadRequest)
		return
	}
	slog.Debug(authRes.String())
	models.NewResponse(w, nil, http.StatusAccepted)

}

func (s *Server) ValidatePassword(w http.ResponseWriter, r *http.Request) {
	var request models.Request
	err := json.NewDecoder(r.Body).Decode(&request)
	if err != nil {
		slog.Error("json parse error", "error", err)
		models.NewResponse(w, &models.Response{Message: "json parse error"}, http.StatusBadRequest)
		return
	}
	t, err := s.getTelegramClient(request.SessionId)
	if err != nil {
		slog.Error("error while starting telegram client", "error", err)
		models.NewResponse(w, &models.Response{Message: "error while starting telegram client"}, http.StatusInternalServerError)
		return
	}
	authRes, err := t.client.Auth().Password(t.context, request.Password)
	if err != nil {
		slog.Error("invalid password", "error", err)
		models.NewResponse(w, &models.Response{Message: "invalid password"}, http.StatusBadRequest)
		return
	}
	slog.Debug(authRes.String())
	models.NewResponse(w, &models.Response{Message: "success"}, http.StatusOK)
}

func (s *Server) ListChannels(w http.ResponseWriter, r *http.Request) {
	var channels []models.DialogInfo
	for sessionId := range s.telegramSessions {
		t, err := s.getTelegramClient(sessionId)
		if err != nil {
			slog.Error("error while starting telegram client", "error", err)
			models.NewResponse(w, &models.Response{Message: "error while starting telegram client"}, http.StatusInternalServerError)
			return
		}

		api := t.client.API()
		dialogs, err := api.MessagesGetDialogs(t.context, &tg.MessagesGetDialogsRequest{
			OffsetPeer: &tg.InputPeerEmpty{},
		})
		if err != nil {
			slog.Error(err.Error())
			models.NewResponse(w, &models.Response{Message: "unable to load channels"}, http.StatusInternalServerError)
			return
		}
		dialogSlice := dialogs.(*tg.MessagesDialogsSlice)
		for _, dialog := range dialogSlice.Chats {
			if channel, ok := dialog.(*tg.Channel); ok {
				channels = append(channels, models.DialogInfo{Name: channel.Title, Id: channel.ID, AccessHash: channel.AccessHash})
			}
		}
	}
	models.NewResponse(w, channels, http.StatusOK)
}

func (s *Server) AddChannels(w http.ResponseWriter, r *http.Request) {
	var request models.Request
	json.NewDecoder(r.Body).Decode(&request)
	t := s.telegramSessions[request.SessionId]
	api := t.client.API()
	dialogs, err := api.MessagesGetDialogs(t.context, &tg.MessagesGetDialogsRequest{
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      20,
	})
	if err != nil {
		slog.Error(err.Error())
		models.NewResponse(w, &models.Response{Message: "unable to fetch dialogs"}, http.StatusInternalServerError)
		return
	}
	dialogSlice := dialogs.(*tg.MessagesDialogsSlice)
	for _, chat := range dialogSlice.Chats {
		if ch, ok := chat.(*tg.Channel); ok {
			if ch.Title != request.Code {
				continue
			}
			slog.Info("channel info", "channelId", ch.ID, "accessHash", ch.AccessHash)
			break
		}
	}
	models.NewResponse(w, &models.Response{Message: "channel added"}, http.StatusOK)
}

func (s *Server) Search(w http.ResponseWriter, r *http.Request) {
	channelId, err := strconv.ParseInt(r.URL.Query().Get("channelId"), 10, 64)
	if err != nil {
		slog.Error("unable to decode channelId", "error", err)
		models.NewResponse(w, &models.Response{Message: "unable to decode channelId"}, http.StatusInternalServerError)
		return
	}
	accessHash, err := strconv.ParseInt(r.URL.Query().Get("accessHash"), 10, 64)
	if err != nil {
		slog.Error("unable to decode accessHash", "error", err)
		models.NewResponse(w, &models.Response{Message: "unable to decode accessHash"}, http.StatusInternalServerError)
		return
	}
	query := r.URL.Query().Get("query")
	t := s.telegramSessions[1]
	api := t.client.API()
	messages, err := api.MessagesSearch(t.context, &tg.MessagesSearchRequest{
		Peer: &tg.InputPeerChannel{
			ChannelID:  channelId,
			AccessHash: accessHash,
		},
		Q:      query,
		Limit:  10,
		Filter: &tg.InputMessagesFilterEmpty{},
	})
	if err != nil {
		slog.Error("error searching", "error", err)
		models.NewResponse(w, &models.Response{Message: "search error"}, http.StatusInternalServerError)
		return
	}
	res, ok := messages.(*tg.MessagesChannelMessages)
	if !ok {
		slog.Error("error searching", "error", err)
		models.NewResponse(w, &models.Response{Message: "search error"}, http.StatusInternalServerError)
		return
	}
	msgs := res.Messages
	var results []models.MediaInfo
	for _, m := range msgs {
		msg, ok := m.(*tg.Message)
		messageId := msg.GetID()
		if !ok || msg.Media == nil {
			continue
		}
		media, ok := msg.Media.(*tg.MessageMediaDocument)
		if !ok {
			continue
		}
		doc, ok := media.Document.(*tg.Document)
		if !ok {
			continue
		}
		var isVideo bool
		var filename string
		for _, attr := range doc.Attributes {
			switch a := attr.(type) {
			case *tg.DocumentAttributeVideo:
				isVideo = true
			case *tg.DocumentAttributeFilename:
				filename = a.FileName
			}
		}
		if isVideo {
			results = append(results, models.MediaInfo{Name: filename, Link: fmt.Sprintf("https://t.me/c/%d/%d", channelId, messageId)})
		}
	}
	models.NewResponse(w, results, http.StatusOK)
}
