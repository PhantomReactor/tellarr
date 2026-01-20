package server

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/go-chi/httplog/v3"
	"github.com/google/uuid"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
	"log/slog"
	"net/http"
	"os"
	"tellarr/internal/pkg/models"
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
		r.Post("/add-channel", s.AddChannels)
		r.Post("/search", s.Search)
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
	var phoneHash string
	slog.Info("received telegram login request")
	var telegramUser models.AuthRequest
	err := json.NewDecoder(r.Body).Decode(&telegramUser)
	if err != nil {
		slog.Error("error while decoding login request", "error", err)
		models.NewAppResponse(w, "invalid credentials", "", http.StatusBadRequest)
		return
	}

	status, err := s.telegramClient.Auth().Status(s.telegramCtx)
	if err != nil {
		slog.Error(err.Error())
		models.NewAppResponse(w, "auth error", "", http.StatusInternalServerError)
		return
	}

	if !status.Authorized {
		sentCode, err := s.telegramClient.Auth().SendCode(s.telegramCtx, telegramUser.Phone, auth.SendCodeOptions{})
		if err != nil {
			slog.Error("error while sending otp", "error", err)
			models.NewAppResponse(w, "unable to send failed", "", http.StatusInternalServerError)
			return
		}
		if authSentCode, ok := sentCode.(*tg.AuthSentCode); ok {
			phoneHash = authSentCode.PhoneCodeHash
			slog.Debug(phoneHash)
		}
	}
	slog.Info("telegram client running")
	models.NewAppResponse(w, "success", phoneHash, http.StatusAccepted)
}

func (s *Server) ValidateCode(w http.ResponseWriter, r *http.Request) {

	var authRequest models.AuthRequest
	err := json.NewDecoder(r.Body).Decode(&authRequest)
	if err != nil {
		slog.Error("json parse error", "error", err)
		models.NewAppResponse(w, "request parse error", "", http.StatusBadRequest)
		return
	}
	authRes, err := s.telegramClient.Auth().SignIn(s.telegramCtx, authRequest.Phone, authRequest.Code, authRequest.PhoneHash)
	if err != nil && err == auth.ErrPasswordAuthNeeded {
		slog.Error("2FA required", "error", err)
		models.NewAppResponse(w, "2FA required", "", http.StatusContinue)
		return
	}
	if err != nil {
		slog.Error("send code error", "error", err)
		models.NewAppResponse(w, "invalid code", "", http.StatusBadRequest)
		return
	}
	slog.Debug(authRes.String())
	models.NewAppResponse(w, "success", "", http.StatusAccepted)

}

func (s *Server) ValidatePassword(w http.ResponseWriter, r *http.Request) {
	var authRequest models.AuthRequest
	err := json.NewDecoder(r.Body).Decode(&authRequest)
	if err != nil {
		slog.Error("json parse error", "error", err)
		models.NewAppResponse(w, "json parse error", "", http.StatusBadRequest)
		return
	}
	authRes, err := s.telegramClient.Auth().Password(s.telegramCtx, authRequest.Password)
	if err != nil {
		slog.Error("invalid password", "error", err)
		models.NewAppResponse(w, "invalid password", "", http.StatusBadRequest)
		return
	}
	slog.Debug(authRes.String())
	models.NewAppResponse(w, "success", "", http.StatusOK)
}

func (s *Server) AddChannels(w http.ResponseWriter, r *http.Request) {
	var authRequest models.AuthRequest
	json.NewDecoder(r.Body).Decode(&authRequest)
	api := s.telegramClient.API()
	dialogs, err := api.MessagesGetDialogs(s.telegramCtx, &tg.MessagesGetDialogsRequest{
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      20,
	})
	if err != nil {
		slog.Error(err.Error())
		models.NewAppResponse(w, "unable to fetch dialogs", "", http.StatusInternalServerError)
		return
	}
	dialogSlice := dialogs.(*tg.MessagesDialogsSlice)
	for _, chat := range dialogSlice.Chats {
		if ch, ok := chat.(*tg.Channel); ok {
			if ch.Title != authRequest.Code {
				continue
			}
			slog.Info("channel info", "channelId", ch.ID, "accessHash", ch.AccessHash)
			fmt.Println(ch.ID, ch.AccessHash)
			break
		}
	}
	models.NewAppResponse(w, "channel added", "", http.StatusOK)
}

func (s *Server) Search(w http.ResponseWriter, r *http.Request) {
	var authRequest models.AuthRequest
	json.NewDecoder(r.Body).Decode(&authRequest)
	api := s.telegramClient.API()
	messages, err := api.MessagesSearch(s.telegramCtx, &tg.MessagesSearchRequest{
		Peer: &tg.InputPeerChannel{
			ChannelID:  authRequest.ChannelID,
			AccessHash: authRequest.AccessHash,
		},
		Q:      authRequest.Code,
		Limit:  10,
		Filter: &tg.InputMessagesFilterEmpty{},
	})
	if err != nil {
		slog.Error("error searching", "error", err)
		models.NewAppResponse(w, "search error", "", http.StatusInternalServerError)
		return
	}
	res, ok := messages.(*tg.MessagesChannelMessages)
	if !ok {
		slog.Error("error searching", "error", err)
		models.NewAppResponse(w, "search error", "", http.StatusInternalServerError)
		return
	}
	msgs := res.Messages
	for _, m := range msgs {
		msg, ok := m.(*tg.Message)
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
			slog.Info("message search results", "fileName", filename, "size", doc.Size)
			fmt.Println(filename, doc.Size)
		}
	}
	models.NewAppResponse(w, "success", "", http.StatusOK)
}
