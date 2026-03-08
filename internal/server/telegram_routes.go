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
	"strings"
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
		slog.Error(fmt.Sprint("session not found for %d", request.SessionId), "error", err)
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
	var channels []models.DialogInfo
	for sessionId, _ := range s.telegramSessions {
		session, err := s.sessionRepo.GetSession(sessionId, "")
		if err != nil {
			slog.Error("error while fetching session", "error", err)
			models.NewResponse(w, &models.Response{Message: "error while fetching session"}, http.StatusInternalServerError)
			return
		}
		if session == nil {
			continue
		}

		t, err := s.getTelegramClient(sessionId)
		if err != nil {
			slog.Error("error while starting telegram client", "error", err)
			models.NewResponse(w, &models.Response{Message: "error while starting telegram client"}, http.StatusInternalServerError)
			return
		}

		api := t.client.API()
		dialogs, err := api.MessagesGetDialogs(t.context, &tg.MessagesGetDialogsRequest{
			Limit:      1000,
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
				existingDialog, err := s.dialogRepo.GetDialogByName(channel.Title)
				if err != nil {
					slog.Error("error while fetching exising channel", "error", err)
					models.NewResponse(w, &models.Response{Message: "error while fetching exising channel"}, http.StatusInternalServerError)
					return
				}
				channels = append(channels, models.DialogInfo{Name: channel.Title, Id: channel.ID, AccessHash: channel.AccessHash})
				if existingDialog != nil {
					continue
				}
				slog.Info("arrrr")
				d := db.Dialog{
					PhoneNumber: session.PhoneNumber,
					Name:        channel.Title,
					Type:        "channel",
					SessionId:   sessionId,
					DialogId:    channel.ID,
					AccessHash:  channel.AccessHash,
					Active:      true,
					Indexer:     false,
					CreatedAt:   time.Now(),
					UpdatedAt:   time.Now(),
				}
				_, err = s.dialogRepo.CreateDialog(d)
				slog.Error("error", "err", err)
			}
		}
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
			isVideo = strings.HasPrefix(doc.MimeType, "video/")
		}
		if isVideo {
			results = append(results, models.MediaInfo{Name: filename, Link: fmt.Sprintf("https://t.me/c/%d/%d", dialog.DialogId, messageId)})
		}
	}
	models.NewResponse(w, results, http.StatusOK)
}

func (s *Server) Download(w http.ResponseWriter, r *http.Request) {
	sessionId, err := strconv.ParseInt(r.URL.Query().Get("sessionId"), 10, 64)
	filename := r.URL.Query().Get("filename")
	if err != nil {
		slog.Error("error while getting sessionId", "error", err)
		models.NewResponse(w, &models.Response{Message: "search error"}, http.StatusInternalServerError)
		return
	}
	downloadLink := r.URL.Query().Get("downloadLink")
	parts := strings.Split(downloadLink, "c/")
	channelAndMessageId := strings.Split(parts[1], "/")
	channelId, _ := strconv.ParseInt(channelAndMessageId[0], 10, 64)
	messageId, _ := strconv.ParseInt(channelAndMessageId[1], 10, 64)
	t := s.telegramSessions[sessionId]
	if err != nil {
		slog.Error("error while starting telegram client", "error", err)
		models.NewResponse(w, &models.Response{Message: "error while starting telegram client"}, http.StatusInternalServerError)
		return
	}
	dialog, err := s.dialogRepo.GetDialogsByDialogId(channelId)
	if err != nil {
		slog.Error("unable to find fetch dialog", "err", err)
		models.NewResponse(w, &models.Response{Message: "unable to fetch dialogs"}, http.StatusInternalServerError)
		return
	}
	if dialog == nil {
		slog.Error(fmt.Sprintf("dialogs not found for %d", channelId))
		models.NewResponse(w, &models.Response{Message: "dialogs not found"}, http.StatusInternalServerError)
		return
	}
	api := t.client.API()
	messages, err := api.ChannelsGetMessages(t.context, &tg.ChannelsGetMessagesRequest{
		Channel: &tg.InputChannel{
			ChannelID:  channelId,
			AccessHash: dialog.AccessHash,
		},
		ID: []tg.InputMessageClass{
			&tg.InputMessageID{ID: int(messageId)},
		},
	})
	if err != nil {
		slog.Error(fmt.Sprintf("error while fetching message from telegram %s", downloadLink))
		models.NewResponse(w, &models.Response{Message: "unable to fetch messages"}, http.StatusInternalServerError)
		return
	}
	var msgs []tg.MessageClass
	switch m := messages.(type) {
	case *tg.MessagesChannelMessages:
		msgs = m.Messages
	}
	if len(msgs) == 0 {
		slog.Error(fmt.Sprintf("messages nor found for %s", downloadLink))
		models.NewResponse(w, &models.Response{Message: "message not found"}, http.StatusInternalServerError)
		return
	}
	msg, ok := msgs[0].(*tg.Message)
	if !ok {
		slog.Error(fmt.Sprintf("messages not found for %s", downloadLink))
		models.NewResponse(w, &models.Response{Message: "message not found"}, http.StatusInternalServerError)
		return
	}
	media, ok := msg.GetMedia()
	if !ok {
		slog.Error(fmt.Sprintf("media not found for %s", downloadLink))
		models.NewResponse(w, &models.Response{Message: "media not found"}, http.StatusInternalServerError)
		return
	}
	md, ok := media.(*tg.MessageMediaDocument)
	if !ok {
		slog.Error(fmt.Sprintf("media not found for %s", downloadLink))
		models.NewResponse(w, &models.Response{Message: "media not found"}, http.StatusInternalServerError)
		return
	}
	doc, ok := md.Document.AsNotEmpty()
	if !ok {
		slog.Error(fmt.Sprintf("media not found for %s", downloadLink))
		models.NewResponse(w, &models.Response{Message: "media not found"}, http.StatusInternalServerError)
		return
	}

	id, err := s.dm.StartDownload(t.context, api, doc, filename)
	if err != nil {
		slog.Error(fmt.Sprintf("cannot download %s", downloadLink))
		models.NewResponse(w, &models.Response{Message: "download failed"}, http.StatusInternalServerError)
		return
	}
	models.NewResponse(w, models.DownloadInfo{Id: id, Name: filename}, http.StatusOK)
}

func (s *Server) Status(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	percent := s.dm.Get(id).Percent()
	models.NewResponse(w, models.DownloadInfo{Id: id, Percent: percent}, http.StatusOK)
}
