package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
	"log/slog"
	"net/http"
	"strconv"
	"tellarr/internal/database"
	db "tellarr/internal/database/models"
	"tellarr/internal/pkg/models"
	"time"
)

func (s *Server) RegisterTelegramRoutes(r chi.Router) {
	r.Route("/api/telegram", func(r chi.Router) {
		r.Use(JWTAuth)

		r.Post("/code", s.RequestCode)
		r.Post("/verify", s.ValidateCode)
		r.Post("/password", s.ValidatePassword)
		r.Post("/channels", s.AddChannels)
		r.Get("/channels", s.ListChannels)
		r.Get("/messages", s.Search)
		r.Get("/download", s.Download)
		r.Get("/download/{id}", s.Status)
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
	sessionRepo := s.sessionRepo
	sessionId, err := sessionRepo.CreateSession(db.Session{PhoneNumber: request.Phone, Active: true, CreatedAt: time.Now(), UpdatedAt: time.Now()})

	t, err := s.getTelegramClient(sessionId)
	if err != nil {
		slog.Error("error while starting telegram client", "error", err)
		models.NewResponse(w, &models.Response{Message: "error while starting telegram client"}, http.StatusInternalServerError)
		return
	}
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
			sessionRepo.UpdatePhoneCodeHash(sessionId, authSentCode.PhoneCodeHash)
		}
	}
	models.NewResponse(w, &models.Response{SessionId: sessionId}, http.StatusAccepted)
}

func (s *Server) getTelegramClient(sessionId int64) (*TelegramSession, error) {
	slog.Info("g1")
	slog.Info("g2", "SessionId", sessionId)
	s.mu.RLock()
	if session, exisit := s.telegramSessions[sessionId]; exisit {
		slog.Info("found")
		s.mu.RUnlock()
		slog.Info("waiting on ready")
		<-session.ready
		slog.Info("session present")
		return session, nil
	}
	s.mu.RUnlock()
	s.mu.Lock()

	if session, exisit := s.telegramSessions[sessionId]; exisit {
		s.mu.Unlock()
		<-session.ready
		slog.Info("session present")
		return session, nil
	}
	started := make(chan struct{})
	session := &TelegramSession{ready: started}
	s.telegramSessions[sessionId] = session
	s.mu.Unlock()
	slog.Info("creatung new session")
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
			s.telegramSessions[sessionId] = session
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
	case <-time.After(10 * time.Second):
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
	session, err := s.sessionRepo.GetSession(request.SessionId, "")
	if err != nil {
		slog.Error("error while fetching session", "error", err)
		models.NewResponse(w, &models.Response{Message: "error while fetching session"}, http.StatusInternalServerError)
		return
	}
	if session == nil {
		slog.Error(fmt.Sprintf("session not found for %d", request.SessionId), "error", err)
		models.NewResponse(w, &models.Response{Message: "error while fetching session"}, http.StatusInternalServerError)
		return
	}
	slog.Info("code", "code", request.Code, "phone", request.Phone, "hash", session.PhoneCodeHash)
	authRes, err := t.client.Auth().SignIn(t.context, request.Phone, request.Code, session.PhoneCodeHash)
	if errors.Is(auth.ErrPasswordAuthNeeded, err) {
		slog.Error("2FA required", "error", err)
		models.NewResponse(w, &models.Response{Message: "2FA required"}, http.StatusContinue)
		return
	}
	if err != nil {
		slog.Error("send code error", "error", err, "phone", request.Phone)
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
	dialogs, err := s.collectDialogs()
	if err != nil {
		slog.Error("error while collecting channels", "error", err)
		models.NewResponse(w, &models.Response{Message: "unable to load channels"}, http.StatusInternalServerError)
		return
	}
	channels := make([]models.DialogInfo, 0, len(dialogs))
	for _, d := range dialogs {
		channels = append(channels, models.DialogInfo{Name: d.Name, Id: d.DialogId, AccessHash: d.AccessHash})
	}
	models.NewResponse(w, channels, http.StatusOK)
}

func (s *Server) AddChannels(w http.ResponseWriter, r *http.Request) {
	var request models.Request
	json.NewDecoder(r.Body).Decode(&request)
	dialog, err := s.dialogRepo.GetDialogByName(request.DialogName)
	if err != nil {
		slog.Error(err.Error())
		models.NewResponse(w, &models.Response{Message: "unable to fetch dialogs"}, http.StatusInternalServerError)
		return
	}
	if dialog == nil {
		slog.Error(fmt.Sprintf("dialogs not found for %s", request.DialogName))
		models.NewResponse(w, &models.Response{Message: "unable to fetch dialogs"}, http.StatusInternalServerError)
		return
	}
	dialog.Indexer = true
	err = s.dialogRepo.UpdateDialog(*dialog)
	slog.Error("err", "err", err)
	models.NewResponse(w, &models.Response{Message: "channel added"}, http.StatusOK)
}

func (s *Server) Search(w http.ResponseWriter, r *http.Request) {
	dialogName := r.URL.Query().Get("dialogName")
	dialog, err := s.dialogRepo.GetDialogByName(dialogName)
	if err != nil {
		slog.Error(err.Error())
		models.NewResponse(w, &models.Response{Message: "unable to fetch dialogs"}, http.StatusInternalServerError)
		return
	}
	if dialog == nil {
		slog.Error(fmt.Sprintf("dialogs not found for %s", dialogName))
		models.NewResponse(w, &models.Response{Message: "unable to fetch dialogs"}, http.StatusInternalServerError)
		return
	}
	query := r.URL.Query().Get("query")
	t := s.telegramSessions[dialog.SessionId]

	if err != nil {
		slog.Error("error while starting telegram client", "error", err)
		models.NewResponse(w, &models.Response{Message: "error while starting telegram client"}, http.StatusInternalServerError)
		return
	}
	api := t.client.API()
	messages, err := api.MessagesSearch(t.context, &tg.MessagesSearchRequest{
		Peer: &tg.InputPeerChannel{
			ChannelID:  dialog.DialogId,
			AccessHash: dialog.AccessHash,
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
		if !ok {
			continue
		}
		if msg.Media == nil {
			// Link-only post: one entry per aggregator link.
			for _, ref := range providerLinksInMessage(msg) {
				results = append(results, models.MediaInfo{
					Name:      ref.Title,
					Link:      fmt.Sprintf("https://t.me/c/%d/%d", dialog.DialogId, messageId),
					Size:      guessSize(msg, ref.Title),
					MessageId: int64(messageId),
					SessionId: dialog.SessionId,
					DialogId:  dialog.DialogId,
					IsTorrent: false,
				})
			}
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
		filename := documentFilename(doc, "")
		isMedia, _, _, isTorrent := isIndexableMedia(doc, filename)
		if isMedia || isTorrent {
			results = append(results, models.MediaInfo{
				Name:      filename,
				Link:      fmt.Sprintf("https://t.me/c/%d/%d", dialog.DialogId, messageId),
				Size:      doc.Size,
				MessageId: int64(messageId),
				SessionId: dialog.SessionId,
				DialogId:  dialog.DialogId,
				IsTorrent: isTorrent,
			})
		}
	}
	models.NewResponse(w, results, http.StatusOK)
}

func (s *Server) Download(w http.ResponseWriter, r *http.Request) {
	filename := r.URL.Query().Get("filename")
	downloadLink := r.URL.Query().Get("downloadLink")

	// Telegram message links keep the legacy sessionId-driven flow.
	if ref, ok := parseTgLink(downloadLink); ok && ref.Username == "" {
		sessionId, err := strconv.ParseInt(r.URL.Query().Get("sessionId"), 10, 64)
		if err != nil {
			slog.Error("error while getting sessionId", "error", err)
			models.NewResponse(w, &models.Response{Message: "invalid sessionId"}, http.StatusBadRequest)
			return
		}
		t, err := s.getTelegramClient(sessionId)
		if err != nil {
			slog.Error("error while starting telegram client", "error", err)
			models.NewResponse(w, &models.Response{Message: "error while starting telegram client"}, http.StatusInternalServerError)
			return
		}
		dialog, err := s.dialogRepo.GetDialogsByDialogId(ref.ChannelId)
		if err != nil {
			slog.Error("unable to find fetch dialog", "err", err)
			models.NewResponse(w, &models.Response{Message: "unable to fetch dialogs"}, http.StatusInternalServerError)
			return
		}
		if dialog == nil {
			slog.Error(fmt.Sprintf("dialogs not found for %d", ref.ChannelId))
			models.NewResponse(w, &models.Response{Message: "dialogs not found"}, http.StatusInternalServerError)
			return
		}
		doc, err := s.fetchDocument(t, ref.ChannelId, ref.MessageId)
		if err != nil {
			slog.Error("unable to resolve document", "link", downloadLink, "err", err)
			models.NewResponse(w, &models.Response{Message: "media not found"}, http.StatusInternalServerError)
			return
		}

		api, err := t.downloadAPI(t.context, doc.DCID)
		if err != nil {
			slog.Error("error while creating download pool", "err", err)
			models.NewResponse(w, &models.Response{Message: "download pool unavailable"}, http.StatusInternalServerError)
			return
		}
		row, err := s.dm.Start(t.context, api, doc, sessionId, ref.ChannelId, ref.MessageId, filename, "", "")
		if err != nil {
			slog.Error(fmt.Sprintf("cannot download %s", downloadLink), "err", err)
			models.NewResponse(w, &models.Response{Message: "download failed"}, http.StatusInternalServerError)
			return
		}
		models.NewResponse(w, models.DownloadInfo{Id: row.ID, Name: row.Filename, Percent: row.Percent()}, http.StatusOK)
		return
	}

	// Any other supported link (provider pages, magnets, .torrent URLs,
	// telegra.ph posts, direct files).
	id, name, err := s.addAnyLink(r.Context(), downloadLink, filename, "", "")
	if err != nil {
		models.NewResponse(w, &models.Response{Message: "unsupported download link: " + err.Error()}, http.StatusBadRequest)
		return
	}
	models.NewResponse(w, models.DownloadInfo{Id: id, Name: name}, http.StatusOK)
}

func (s *Server) Status(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	row, err := s.dm.Get(id)
	if err != nil {
		models.NewResponse(w, &models.Response{Message: "lookup failed"}, http.StatusInternalServerError)
		return
	}
	if row == nil {
		models.NewResponse(w, &models.Response{Message: "download not found"}, http.StatusNotFound)
		return
	}
	models.NewResponse(w, models.DownloadInfo{
		Id:      row.ID,
		Name:    row.Filename,
		Percent: row.Percent(),
		State:   string(row.State),
		Size:    row.Total,
	}, http.StatusOK)
}
