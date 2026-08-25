package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"

	"tellarr/internal/database/models"
	"tellarr/internal/pkg/enums"
	"tellarr/internal/web/views"
)

func timeNow() time.Time {
	return time.Now().UTC()
}

func (s *Server) webTelegramPage(w http.ResponseWriter, r *http.Request) {
	renderTelegram(w, r, "phone", 0, "", "")
}

func (s *Server) webTelegramPhone(w http.ResponseWriter, r *http.Request) {
	phone := strings.TrimSpace(r.FormValue("phone"))
	if phone == "" {
		renderTelegram(w, r, "phone", 0, "", "phone number required")
		return
	}
	sessionId, err := s.sessionRepo.CreateSession(models.Session{PhoneNumber: phone, Active: true, CreatedAt: timeNow(), UpdatedAt: timeNow()})
	if err != nil {
		slog.Error("session create failed", "err", err)
		renderTelegram(w, r, "phone", 0, "", "could not create session")
		return
	}
	t, err := s.getTelegramClient(sessionId)
	if err != nil {
		renderTelegram(w, r, "phone", sessionId, "", "error while starting telegram client")
		return
	}
	status, err := t.client.Auth().Status(t.context)
	if err != nil {
		renderTelegram(w, r, "phone", sessionId, "", "auth error")
		return
	}
	if !status.Authorized {
		sentCode, err := t.client.Auth().SendCode(t.context, phone, auth.SendCodeOptions{})
		if err != nil {
			slog.Error("send code failed", "err", err)
			renderTelegram(w, r, "phone", sessionId, "", "unable to send code")
			return
		}
		if authSentCode, ok := sentCode.(*tg.AuthSentCode); ok {
			_ = s.sessionRepo.UpdatePhoneCodeHash(sessionId, authSentCode.PhoneCodeHash)
		}
	} else {
		renderTelegram(w, r, "done", sessionId, "already authorized", "")
		return
	}
	renderTelegram(w, r, "code", sessionId, "code sent to your Telegram", "")
}

func (s *Server) webTelegramCode(w http.ResponseWriter, r *http.Request) {
	sessionId, _ := strconv.ParseInt(r.FormValue("sessionId"), 10, 64)
	code := strings.TrimSpace(r.FormValue("code"))
	t, err := s.getTelegramClient(sessionId)
	if err != nil {
		renderTelegram(w, r, "code", sessionId, "", "error while starting telegram client")
		return
	}
	session, err := s.sessionRepo.GetSession(sessionId, "")
	if err != nil || session == nil {
		renderTelegram(w, r, "phone", 0, "", "session not found, start over")
		return
	}
	_, err = t.client.Auth().SignIn(t.context, session.PhoneNumber, code, session.PhoneCodeHash)
	if errors.Is(auth.ErrPasswordAuthNeeded, err) {
		renderTelegram(w, r, "password", sessionId, "", "2FA password required")
		return
	}
	if err != nil {
		slog.Error("sign-in failed", "err", err)
		renderTelegram(w, r, "code", sessionId, "", "invalid code")
		return
	}
	http.Redirect(w, r, "/ui/indexers", http.StatusSeeOther)
}

func (s *Server) webTelegramPassword(w http.ResponseWriter, r *http.Request) {
	sessionId, _ := strconv.ParseInt(r.FormValue("sessionId"), 10, 64)
	password := r.FormValue("password")
	t, err := s.getTelegramClient(sessionId)
	if err != nil {
		renderTelegram(w, r, "password", sessionId, "", "error while starting telegram client")
		return
	}
	if _, err := t.client.Auth().Password(t.context, password); err != nil {
		renderTelegram(w, r, "password", sessionId, "", "invalid password")
		return
	}
	http.Redirect(w, r, "/ui/indexers", http.StatusSeeOther)
}

// collectDialogs discovers channels from every active Telegram session,
// upserts them and returns the DB view.
func (s *Server) collectDialogs() ([]models.Dialog, error) {
	s.mu.RLock()
	sessionIds := make([]int64, 0, len(s.telegramSessions))
	for id := range s.telegramSessions {
		sessionIds = append(sessionIds, id)
	}
	s.mu.RUnlock()

	for _, sessionId := range sessionIds {
		session, err := s.sessionRepo.GetSession(sessionId, "")
		if err != nil || session == nil {
			continue
		}
		t, err := s.getTelegramClient(sessionId)
		if err != nil {
			slog.Error("telegram client unavailable", "sessionId", sessionId, "err", err)
			continue
		}
		api := t.client.API()
		dialogs, err := api.MessagesGetDialogs(t.context, &tg.MessagesGetDialogsRequest{
			Limit:      1000,
			OffsetPeer: &tg.InputPeerEmpty{},
		})
		if err != nil {
			slog.Error("dialog fetch failed", "sessionId", sessionId, "err", err)
			continue
		}
		var chats []tg.ChatClass
		switch d := dialogs.(type) {
		case *tg.MessagesDialogsSlice:
			chats = d.Chats
		case *tg.MessagesDialogs:
			chats = d.Chats
		}
		for _, chat := range chats {
			channel, ok := chat.(*tg.Channel)
			if !ok {
				continue
			}
			existing, err := s.dialogRepo.GetDialogByName(channel.Title)
			if err != nil {
				return nil, err
			}
			if existing != nil {
				continue
			}
			_, _ = s.dialogRepo.CreateDialog(models.Dialog{
				PhoneNumber: session.PhoneNumber,
				Name:        channel.Title,
				Type:        "channel",
				SessionId:   sessionId,
				DialogId:    channel.ID,
				AccessHash:  channel.AccessHash,
				Active:      true,
				Indexer:     false,
				CreatedAt:   timeNow(),
				UpdatedAt:   timeNow(),
			})
		}
	}
	return s.dialogRepo.ListDialogs(false)
}

func (s *Server) webIndexers(w http.ResponseWriter, r *http.Request) {
	dialogs, err := s.collectDialogs()
	errMsg := ""
	if err != nil {
		slog.Error("collect dialogs failed", "err", err)
		errMsg = "failed to load channels"
	}
	vms := make([]views.ChannelVM, 0, len(dialogs))
	for _, d := range dialogs {
		vms = append(vms, views.ChannelVM{Name: d.Name, IsIndex: d.Indexer})
	}
	_ = views.IndexersPage(vms, r.URL.Query().Get("msg"), errMsg).Render(r.Context(), w)
}

func (s *Server) webIndexerToggle(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	dialog, err := s.dialogRepo.GetDialogByName(name)
	if err != nil || dialog == nil {
		redirectFlash(w, r, "/ui/indexers", "", "channel not found")
		return
	}
	dialog.Indexer = !dialog.Indexer
	if err := s.dialogRepo.UpdateDialog(*dialog); err != nil {
		redirectFlash(w, r, "/ui/indexers", "", "update failed")
		return
	}
	state := "disabled"
	if dialog.Indexer {
		state = "enabled"
	}
	redirectFlash(w, r, "/ui/indexers", name+" "+state, "")
}

// indexerFeedURL builds the torznab feed URL for a channel using the first
// API key (same shape as the settings page shows).
func (s *Server) indexerFeedURL(channel string) (string, error) {
	tokens, err := s.apiTokens()
	if err != nil {
		return "", fmt.Errorf("could not load API keys")
	}
	if len(tokens) == 0 {
		return "", fmt.Errorf("generate an API key on the Settings page first")
	}
	return s.baseURL() + "/torznab/" + url.PathEscape(channel) + "/api?apikey=" + url.QueryEscape(tokens[0].Token), nil
}

// webIndexerProwlarr registers one channel as an indexer in Prowlarr.
// Preferred path is the Prowlarr REST API (Generic Torznab); when only a
// filesystem path is configured, a Cardigann YML definition is written into
// Prowlarr's custom definitions folder instead.
func (s *Server) webIndexerProwlarr(w http.ResponseWriter, r *http.Request) {
	channel := strings.TrimSpace(r.FormValue("name"))
	dialog, err := s.dialogRepo.GetDialogByName(channel)
	if err != nil || dialog == nil {
		redirectFlash(w, r, "/ui/indexers", "", "channel not found")
		return
	}
	if !dialog.Indexer {
		redirectFlash(w, r, "/ui/indexers", "", "enable the channel as an indexer first")
		return
	}
	feedURL, err := s.indexerFeedURL(channel)
	if err != nil {
		redirectFlash(w, r, "/ui/indexers", "", err.Error())
		return
	}
	indexerName := "Tellarr - " + channel

	prowlarr := NewProwlarrClientFromEnv()
	switch {
	case prowlarr.Configured():
		if err := prowlarr.AddTorznabIndexer(r.Context(), indexerName, feedURL); err != nil {
			slog.Error("prowlarr add failed", "channel", channel, "err", err)
			redirectFlash(w, r, "/ui/indexers", "", "Prowlarr: "+err.Error())
			return
		}
		redirectFlash(w, r, "/ui/indexers", indexerName+" added to Prowlarr", "")
		return

	case os.Getenv("PROWLARR_DEFINITIONS_DIR") != "" || os.Getenv("PROWLARR_CONFIG_DIR") != "":
		path, err := WriteProwlarrDefinition(os.Getenv("PROWLARR_DEFINITIONS_DIR"), channel, feedURL)
		if err != nil {
			slog.Error("prowlarr definition write failed", "channel", channel, "err", err)
			redirectFlash(w, r, "/ui/indexers", "", "could not write definition: "+err.Error())
			return
		}
		msg := fmt.Sprintf("definition written to %s — restart Prowlarr, then add %q under Custom indexers", path, indexerName)
		redirectFlash(w, r, "/ui/indexers", msg, "")
		return

	default:
		redirectFlash(w, r, "/ui/indexers", "", "configure PROWLARR_URL + PROWLARR_API_KEY, or set PROWLARR_DEFINITIONS_DIR to write a custom definition")
	}
}

// webIndexerProwlarrYML serves the Cardigann definition for manual copying.
func (s *Server) webIndexerProwlarrYML(w http.ResponseWriter, r *http.Request) {
	channel := strings.TrimSpace(r.URL.Query().Get("name"))
	dialog, err := s.dialogRepo.GetDialogByName(channel)
	if err != nil || dialog == nil || !dialog.Indexer {
		http.Error(w, "channel not found or not an indexer", http.StatusNotFound)
		return
	}
	feedURL, err := s.indexerFeedURL(channel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	yml := TorznabCardigannYAML("tellarr-"+slugify(channel), "Tellarr - "+channel, channel, feedURL)
	w.Header().Set("Content-Type", "application/x-yaml")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"tellarr-%s.yml\"", slugify(channel)))
	_, _ = w.Write(yml)
}

// webIndexerProwlarrYMLView renders the Cardigann definition in a viewer
// (textarea + copy/download actions) for the indexers page.
func (s *Server) webIndexerProwlarrYMLView(w http.ResponseWriter, r *http.Request) {
	channel := strings.TrimSpace(r.URL.Query().Get("name"))
	dialog, err := s.dialogRepo.GetDialogByName(channel)
	if err != nil || dialog == nil || !dialog.Indexer {
		http.Error(w, "channel not found or not an indexer", http.StatusNotFound)
		return
	}
	feedURL, err := s.indexerFeedURL(channel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	yml := string(TorznabCardigannYAML("tellarr-"+slugify(channel), "Tellarr - "+channel, channel, feedURL))
	if err := views.YMLViewer(channel, yml).Render(r.Context(), w); err != nil {
		slog.Error("yml viewer render failed", "err", err)
	}
}

func downloadRowVMs(rows []models.TorrentDownload) []views.DownloadRowVM {
	vms := make([]views.DownloadRowVM, 0, len(rows))
	for _, row := range rows {
		vms = append(vms, views.DownloadRowVM{
			ID:       row.ID,
			Name:     row.Filename,
			State:    string(row.State),
			Percent:  row.Percent(),
			Origin:   string(row.Origin),
			Category: row.Category,
			Written:  row.Written,
			Total:    row.Total,
			Speed:    row.Speed,
			ETA:      row.ETA(),
			Error:    row.Error,
		})
	}
	return vms
}

// remoteTorrentVM converts a real-qBittorrent torrent into a UI row.
func remoteTorrentVM(rt RemoteTorrent) views.DownloadRowVM {
	written := int64(rt.Progress * float64(rt.Size))
	eta := int64(-1)
	if rt.Eta > 0 && rt.Eta < qBitEtaUnknown {
		eta = rt.Eta
	}
	return views.DownloadRowVM{
		ID:       rt.Hash,
		Name:     rt.Name,
		State:    qbitUIState(rt.State),
		Percent:  rt.Progress * 100,
		Origin:   "qbittorrent",
		Category: rt.Category,
		Written:  written,
		Total:    rt.Size,
		Speed:    rt.DlSpeed,
		ETA:      eta,
	}
}

// downloadsPageVMs assembles the rows shown on the Downloads page: local
// (telegram/aria2) rows plus live torrents from the real qBittorrent. When the
// real client answers, its torrents supersede the persisted external_qbit
// placeholder rows so a forwarded .torrent is never listed twice.
func (s *Server) downloadsPageVMs(rows []models.TorrentDownload) []views.DownloadRowVM {
	qb := NewQBitRealClientFromEnv()
	var remotes []RemoteTorrent
	liveRemotes := false
	if qb.Configured() {
		var err error
		remotes, err = qb.TorrentsInfo()
		if err != nil {
			slog.Error("real qbit info failed", "err", err)
		} else {
			liveRemotes = true
		}
	}
	vms := make([]views.DownloadRowVM, 0, len(rows)+len(remotes))
	for _, row := range rows {
		if liveRemotes && row.Origin == models.OriginExternalQb {
			continue
		}
		vms = append(vms, views.DownloadRowVM{
			ID:       row.ID,
			Name:     row.Filename,
			State:    string(row.State),
			Percent:  row.Percent(),
			Origin:   string(row.Origin),
			Category: row.Category,
			Written:  row.Written,
			Total:    row.Total,
			Speed:    row.Speed,
			ETA:      row.ETA(),
			Error:    row.Error,
		})
	}
	if liveRemotes {
		for _, rt := range remotes {
			vms = append(vms, remoteTorrentVM(rt))
		}
	}
	return vms
}

func (s *Server) webDownloads(w http.ResponseWriter, r *http.Request) {
	rows, err := s.dm.List()
	if err != nil {
		rows = nil
	}
	s.refreshAriaRows(rows)
	msg, errMsg := r.URL.Query().Get("msg"), r.URL.Query().Get("err")
	_ = views.DownloadsPage(s.downloadsPageVMs(rows), msg, errMsg).Render(r.Context(), w)
}

func (s *Server) webDownloadsTable(w http.ResponseWriter, r *http.Request) {
	rows, err := s.dm.List()
	if err != nil {
		rows = nil
	}
	s.refreshAriaRows(rows)
	_ = views.DownloadsTable(s.downloadsPageVMs(rows)).Render(r.Context(), w)
}

func (s *Server) webDownloadAdd(w http.ResponseWriter, r *http.Request) {
	link := strings.TrimSpace(r.FormValue("link"))
	filename := strings.TrimSpace(r.FormValue("filename"))

	channelId, messageId, ok := parseTgLink(link)
	if !ok {
		redirectFlash(w, r, "/ui/downloads", "", "unrecognized link format")
		return
	}
	dialog, err := s.dialogRepo.GetDialogsByDialogId(channelId)
	if err != nil || dialog == nil {
		redirectFlash(w, r, "/ui/downloads", "", "channel not indexed or unknown")
		return
	}
	t, err := s.getTelegramClient(dialog.SessionId)
	if err != nil {
		redirectFlash(w, r, "/ui/downloads", "", "telegram session unavailable")
		return
	}
	doc, err := s.fetchDocument(t, channelId, messageId)
	if err != nil {
		redirectFlash(w, r, "/ui/downloads", "", "media not found in message")
		return
	}
	if filename == "" {
		filename = documentFilename(doc, fmt.Sprintf("%d_%d", channelId, messageId))
	}
	api, err := t.downloadAPI(t.context, doc.DCID)
	if err != nil {
		redirectFlash(w, r, "/ui/downloads", "", "download pool unavailable")
		return
	}
	if _, err := s.dm.Start(t.context, api, doc, dialog.SessionId, channelId, messageId, filename, "", ""); err != nil {
		redirectFlash(w, r, "/ui/downloads", "", "download could not be started")
		return
	}
	redirectFlash(w, r, "/ui/downloads", "download started: "+filename, "")
}

func parseTgLink(link string) (channelId, messageId int64, ok bool) {
	link = strings.TrimSpace(link)
	idx := strings.LastIndex(link, "/c/")
	if idx < 0 {
		return 0, 0, false
	}
	parts := strings.Split(strings.TrimPrefix(link[idx:], "/c/"), "/")
	if len(parts) < 2 {
		return 0, 0, false
	}
	channelId, err1 := strconv.ParseInt(parts[0], 10, 64)
	messageId, err2 := strconv.ParseInt(parts[1], 10, 64)
	return channelId, messageId, err1 == nil && err2 == nil
}

func documentFilename(doc *tg.Document, fallback string) string {
	for _, attr := range doc.Attributes {
		if f, ok := attr.(*tg.DocumentAttributeFilename); ok {
			return f.FileName
		}
	}
	return fallback
}

func (s *Server) webDownloadAction(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	action := chi.URLParam(r, "action")

	row, err := s.dm.Get(id)
	if err != nil || row == nil {
		// Not a local row: it may be a torrent living on the real
		// qBittorrent instance.
		s.webRemoteDownloadAction(w, r, id, action)
		return
	}

	var msg, errMsg string
	switch action {
	case "pause":
		if row.Origin == models.OriginAria2 && row.RemoteGid != "" {
			// Pausing only in the DB lets refreshAriaRows flip the state
			// straight back to downloading; pause in aria2 itself.
			aria := NewAria2ClientFromEnv()
			if !aria.Configured() {
				errMsg = "aria2 rpc not configured"
				break
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			err := aria.Pause(ctx, row.RemoteGid)
			cancel()
			if err != nil {
				errMsg = "pause failed"
			} else {
				_ = s.downloadRepo.SetState(id, models.StatePaused)
				msg = "paused"
			}
		} else if err := s.dm.Pause(id); err != nil {
			errMsg = "pause failed"
		} else {
			msg = "paused"
		}
	case "resume":
		if _, err := s.RestartDownload(row); err != nil {
			errMsg = "resume failed: telegram session may be offline"
		} else {
			msg = "resumed"
		}
	case "delete", "delete-files":
		delFiles := action == "delete-files"
		// aria2-backed rows must also have their RPC job stopped, or the
		// download keeps running invisibly after the row disappears.
		if row.Origin == models.OriginAria2 && row.RemoteGid != "" {
			if aria := NewAria2ClientFromEnv(); aria.Configured() {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				if err := aria.Remove(ctx, row.RemoteGid); err != nil {
					slog.Warn("aria2 remove failed", "gid", row.RemoteGid, "err", err)
				}
				cancel()
			}
		}
		var derr error
		if delFiles && row.ContentPath != "" &&
			(row.Origin == models.OriginTelegram || row.Origin == models.OriginAria2) {
			derr = s.deleteLocal(*row, true)
		} else {
			derr = s.dm.Remove(id)
		}
		if derr != nil {
			slog.Error("delete failed", "id", id, "err", derr)
			errMsg = "delete failed"
		} else if delFiles {
			msg = "download deleted with files"
		} else {
			msg = "record removed"
		}
	default:
		errMsg = "unknown action"
	}
	redirectFlash(w, r, "/ui/downloads", msg, errMsg)
}

// webRemoteDownloadAction applies pause/resume/delete to a torrent on the
// real qBittorrent instance (rows that only exist there, keyed by infohash).
func (s *Server) webRemoteDownloadAction(w http.ResponseWriter, r *http.Request, id, action string) {
	qb := NewQBitRealClientFromEnv()
	if !qb.Configured() {
		redirectFlash(w, r, "/ui/downloads", "", "download not found")
		return
	}
	var err error
	okMsg := ""
	switch action {
	case "pause":
		err = qb.Pause([]string{id})
		okMsg = "paused"
	case "resume":
		err = qb.Resume([]string{id})
		okMsg = "resumed"
	case "delete":
		err = qb.Delete([]string{id}, false)
		okMsg = "record removed"
	case "delete-files":
		err = qb.Delete([]string{id}, true)
		okMsg = "download deleted with files"
	default:
		redirectFlash(w, r, "/ui/downloads", "", "unknown action")
		return
	}
	if err != nil {
		slog.Error("remote qbittorrent action failed", "hash", id, "action", action, "err", err)
		redirectFlash(w, r, "/ui/downloads", "", action+" failed")
		return
	}
	redirectFlash(w, r, "/ui/downloads", okMsg, "")
}

func (s *Server) baseURL() string {
	if b := strings.TrimSpace(os.Getenv("BASE_URL")); b != "" {
		return strings.TrimRight(b, "/")
	}
	return fmt.Sprintf("http://localhost:%d", s.port)
}

func (s *Server) apiTokens() ([]models.Token, error) {
	ptr, err := s.tokenRepo.GetTokensByTokenType(enums.API)
	if ptr == nil {
		return nil, err
	}
	return *ptr, err
}

func (s *Server) webSettings(w http.ResponseWriter, r *http.Request) {
	tokens, _ := s.apiTokens()
	tokenVMs := make([]views.TokenVM, 0, len(tokens))
	for _, t := range tokens {
		tokenVMs = append(tokenVMs, views.TokenVM{ID: t.ID, Token: t.Token, CreatedAt: t.CreatedAt.Format("2006-01-02")})
	}

	dialogs, _ := s.dialogRepo.ListDialogs(true)
	apikey := "<apikey>"
	if len(tokens) > 0 {
		apikey = tokens[0].Token
	}
	feeds := make([]views.IndexerFeedVM, 0, len(dialogs))
	for _, d := range dialogs {
		feeds = append(feeds, views.IndexerFeedVM{
			Name:    d.Name,
			FeedURL: s.baseURL() + "/torznab/" + url.PathEscape(d.Name) + "/api?apikey=" + url.QueryEscape(apikey),
		})
	}

	qb := NewQBitRealClientFromEnv()
	realURL := ""
	if qb.Configured() {
		realURL = qb.baseURL
	}
	msg, errMsg := r.URL.Query().Get("msg"), r.URL.Query().Get("err")
	testResult := r.URL.Query().Get("qbtest")
	_ = views.SettingsPage(tokenVMs, feeds, s.baseURL(), realURL, testResult, msg, errMsg).Render(r.Context(), w)
}

func (s *Server) webTokenCreate(w http.ResponseWriter, r *http.Request) {
	claims, _ := r.Context().Value("claims").(*Claims)
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		redirectFlash(w, r, "/ui/settings", "", "token generation failed")
		return
	}
	val := hex.EncodeToString(buf)
	token := &models.Token{
		UserId:    claims.UserID,
		Token:     val,
		Type:      enums.API,
		ExpiresAt: timeNow().AddDate(10, 0, 0),
		CreatedAt: timeNow(),
		UpdatedAt: timeNow(),
	}
	if _, err := s.tokenRepo.CreateToken(token); err != nil {
		redirectFlash(w, r, "/ui/settings", "", "could not save token")
		return
	}
	redirectFlash(w, r, "/ui/settings", "created key: "+val, "")
}

func (s *Server) webTokenDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		redirectFlash(w, r, "/ui/settings", "", "bad token id")
		return
	}
	if err := s.tokenRepo.Delete(id); err != nil {
		redirectFlash(w, r, "/ui/settings", "", "delete failed")
		return
	}
	redirectFlash(w, r, "/ui/settings", "key deleted", "")
}

func (s *Server) webQBitTest(w http.ResponseWriter, r *http.Request) {
	qb := NewQBitRealClientFromEnv()
	if !qb.Configured() {
		_, _ = w.Write([]byte("<em>not configured</em>"))
		return
	}
	if err := qb.login(); err != nil {
		_, _ = w.Write([]byte("<mark class=\"err-mark\">connection failed: " + http.StatusText(http.StatusInternalServerError) + "</mark>"))
		return
	}
	_, _ = w.Write([]byte("<mark>connected</mark>"))
}
