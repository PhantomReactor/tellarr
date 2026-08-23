package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gotd/td/tg"
	dbm "tellarr/internal/database/models"
	"tellarr/internal/linkresolver"
)

const qbCookieName = "SID"

type qbSessionStore struct {
	mu       sync.Mutex
	sessions map[string]time.Time
}

var qbSessions = &qbSessionStore{sessions: make(map[string]time.Time)}

func (st *qbSessionStore) create() string {
	buf := make([]byte, 16)
	rand.Read(buf)
	sid := hex.EncodeToString(buf)
	st.mu.Lock()
	defer st.mu.Unlock()
	st.sessions[sid] = time.Now().Add(24 * time.Hour)
	return sid
}

func (st *qbSessionStore) valid(sid string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	expiry, ok := st.sessions[sid]
	return ok && time.Now().Before(expiry)
}

func (st *qbSessionStore) drop(sid string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.sessions, sid)
}

func (s *Server) qbUsername() string {
	if v := os.Getenv("QBIT_USER"); v != "" {
		return v
	}
	return "admin"
}

func (s *Server) qbPassword() string {
	if v := os.Getenv("QBIT_PASS"); v != "" {
		return v
	}
	return "adminadmin"
}

type qbTorrentInfo struct {
	AddedOn     int64   `json:"added_on"`
	Category    string  `json:"category"`
	Completed   int64   `json:"completed"`
	ContentPath string  `json:"content_path"`
	DlSpeed     int64   `json:"dlspeed"`
	Eta         int64   `json:"eta"`
	Hash        string  `json:"hash"`
	Name        string  `json:"name"`
	NumSeeds    int     `json:"num_seeds"`
	NumComplete int     `json:"num_complete"`
	Progress    float64 `json:"progress"`
	Ratio       float64 `json:"ratio"`
	SavePath    string  `json:"save_path"`
	Size        int64   `json:"size"`
	State       string  `json:"state"`
	Tags        string  `json:"tags"`
	Tracker     string  `json:"tracker"`
}

func (s *Server) RegisterQBitRoutes(r chi.Router) {
	r.Route("/api/v2", func(r chi.Router) {
		r.Post("/auth/login", s.qbLogin)
		r.Get("/auth/logout", s.qbLogout)
		r.Post("/auth/logout", s.qbLogout)
		r.Group(func(r chi.Router) {
			r.Use(qbAuth)

			r.Get("/app/version", func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte("v5.0.2"))
			})
			r.Get("/app/webapiVersion", func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte("2.11.2"))
			})
			r.Get("/app/preferences", func(w http.ResponseWriter, r *http.Request) {
				writeJSONRaw(w, map[string]any{
					"save_path":           os.Getenv("DOWNLOAD_DIR"),
					"temp_path_enabled":   false,
					"temp_path":           "",
					"alt_dl_limit":        0,
					"dl_limit":            0,
					"up_limit":            0,
					"max_active_torrents": 50,
					"listen_port":         6881,
				})
			})
			r.Get("/transfer/info", func(w http.ResponseWriter, r *http.Request) {
				writeJSONRaw(w, map[string]any{
					"dl_info_speed":     0,
					"dl_info_data":      0,
					"up_info_speed":     0,
					"up_info_data":      0,
					"dht_nodes":         0,
					"connection_status": "connected",
				})
			})

			r.Post("/torrents/add", s.qbAddTorrent)
			r.Get("/torrents/info", s.qbTorrentsInfo)
			r.Get("/torrents/files", s.qbTorrentFiles)
			r.Post("/torrents/pause", s.wrapTorrentAction(s.pauseTorrents, ariaPause, qbPause))
			r.Post("/torrents/resume", s.wrapTorrentAction(s.resumeTorrents, ariaResume, qbResume))
			r.Post("/torrents/stop", s.wrapTorrentAction(s.pauseTorrents, ariaPause, qbPause))
			r.Post("/torrents/start", s.wrapTorrentAction(s.resumeTorrents, ariaResume, qbResume))
			r.Post("/torrents/delete", s.qbDeleteTorrents)

			r.Get("/sync/maindata", s.qbSyncMaindata)
			r.Get("/torrents/categories", func(w http.ResponseWriter, r *http.Request) {
				writeJSONRaw(w, map[string]any{})
			})
			r.Post("/torrents/createCategory", okHandler)
			r.Post("/torrents/createFolder", okHandler)
		})
	})
}

func okHandler(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("Ok."))
}

func writeJSONRaw(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func qbAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(qbCookieName)
		if err != nil || c.Value == "" || !qbSessions.valid(c.Value) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) qbLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if r.FormValue("username") == s.qbUsername() && r.FormValue("password") == s.qbPassword() {
		sid := qbSessions.create()
		http.SetCookie(w, &http.Cookie{
			Name:     qbCookieName,
			Value:    sid,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
		w.Write([]byte("Ok."))
		return
	}
	w.Write([]byte("Fails."))
}

func (s *Server) qbLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(qbCookieName); err == nil {
		qbSessions.drop(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: qbCookieName, Value: "", Path: "/", MaxAge: -1})
	w.Write([]byte("Ok."))
}

// isOwnRef recognizes our synthetic links: {BASE}/d/{dialog}/{message}[.torrent]
// and magnets carrying x.tellarr=<that same ref>.
func (s *Server) isOwnRef(raw string) (dialogId, messageId int64, isTorrent bool, ok bool) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "magnet:") {
		mu, err := url.Parse(raw)
		if err != nil {
			return 0, 0, false, false
		}
		ref := mu.Query().Get("x.tellarr")
		d, m, valid := parseTellarrRef(ref)
		return d, m, false, valid
	}
	u, err := url.Parse(raw)
	if err != nil || u.Path == "" {
		return 0, 0, false, false
	}
	idx := strings.LastIndex(u.Path, "/d/")
	if idx < 0 {
		return 0, 0, false, false
	}
	isTor := strings.HasSuffix(u.Path, ".torrent")
	path := strings.TrimSuffix(u.Path, ".torrent")
	segs := strings.Split(path[idx+3:], "/")
	if len(segs) < 2 {
		return 0, 0, false, false
	}
	d, err1 := strconv.ParseInt(segs[0], 10, 64)
	m, err2 := strconv.ParseInt(segs[1], 10, 64)
	return d, m, isTor, err1 == nil && err2 == nil
}

// resolveMessage loads a Telegram message for a /d/ reference along with its
// owning session.
func (s *Server) resolveMessage(ctx context.Context, dialogId, messageId int64) (*TelegramSession, *tg.Message, error) {
	dialog, err := s.dialogRepo.GetDialogsByDialogId(dialogId)
	if err != nil || dialog == nil {
		return nil, nil, fmt.Errorf("dialog %d not found", dialogId)
	}
	t, err := s.getTelegramClient(dialog.SessionId)
	if err != nil {
		return nil, nil, fmt.Errorf("telegram session unavailable")
	}
	msg, err := s.fetchMessage(t, dialogId, messageId)
	if err != nil {
		return nil, nil, err
	}
	return t, msg, nil
}

// documentOfMessage unwraps the document media of a message, if any.
func documentOfMessage(msg *tg.Message) *tg.Document {
	if msg == nil || msg.Media == nil {
		return nil
	}
	if md, ok := msg.Media.(*tg.MessageMediaDocument); ok {
		if doc, ok := md.Document.AsNotEmpty(); ok {
			return doc
		}
	}
	return nil
}

func (s *Server) qbAddTorrent(w http.ResponseWriter, r *http.Request) {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/") {
		if err := r.ParseMultipartForm(512 << 20); err != nil {
			w.Write([]byte("Fails."))
			return
		}
	} else if err := r.ParseForm(); err != nil {
		w.Write([]byte("Fails."))
		return
	}

	category := firstNonEmpty(r.FormValue("category"), "")
	savePath := firstNonEmpty(r.FormValue("savepath"), "")

	failed := 0

	// Uploaded .torrent files -> straight to the real qBittorrent.
	if r.MultipartForm != nil {
		for _, fhList := range r.MultipartForm.File {
			for _, fh := range fhList {
				file, err := fh.Open()
				if err != nil {
					failed++
					continue
				}
				data, err := io.ReadAll(file)
				file.Close()
				if err != nil {
					failed++
					continue
				}
				if err := s.forwardTorrentBytes(data, category, savePath); err != nil {
					slog.Error("uploaded torrent forward failed", "err", err)
					failed++
				}
			}
		}
	}

	// URLs: magnets with x.tellarr, or our own /d/ torrent links.
	if urlsParam := r.FormValue("urls"); urlsParam != "" {
		for _, line := range strings.FieldsFunc(urlsParam, func(c rune) bool { return c == '\n' || c == '\r' }) {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if err := s.addTorrentFromURL(r.Context(), line, category, savePath); err != nil {
				slog.Error("add torrent url failed", "url", line, "err", err)
				failed++
			}
		}
	}

	if failed > 0 {
		w.Write([]byte("Fails."))
		return
	}
	w.Write([]byte("Ok."))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func (s *Server) addTorrentFromURL(ctx context.Context, raw, category, savePath string) error {
	dialogId, messageId, isTorrent, ok := s.isOwnRef(raw)
	if !ok {
		// A raw provider link pasted directly into the qBittorrent UI.
		if linkresolver.Name(raw) != "" {
			return s.startExternalDownload(ctx, 0, 0, 0, nil, raw, category, savePath)
		}
		return fmt.Errorf("unsupported url")
	}
	dialog, err := s.dialogRepo.GetDialogsByDialogId(dialogId)
	if err != nil || dialog == nil {
		return fmt.Errorf("dialog %d not found", dialogId)
	}
	t, msg, err := s.resolveMessage(ctx, dialogId, messageId)
	if err != nil {
		return err
	}

	doc := documentOfMessage(msg)
	filename := ""
	if doc != nil {
		filename = documentFilename(doc, fmt.Sprintf("%d_%d", dialogId, messageId))
	}

	switch {
	case doc == nil:
		told := parseTellarrURL(raw)
		if told == "" || linkresolver.Name(told) == "" {
			// No pinned link (legacy magnet): fall back to the first link.
			urls := providerURLsInMessage(msg)
			if len(urls) == 0 {
				return fmt.Errorf("message %d/%d has no downloadable content", dialogId, messageId)
			}
			told = urls[0]
		}
		return s.startExternalDownload(ctx, dialog.SessionId, dialogId, messageId, msg, told, category, savePath)

	case isTorrent:
		// Real torrent file: pull bytes and hand off to the genuine client.
		buf := new(bytes.Buffer)
		dl := newDownloader()
		_, err = dl.Download(t.client.API(), &tg.InputDocumentFileLocation{
			ID:            doc.ID,
			AccessHash:    doc.AccessHash,
			FileReference: doc.FileReference,
		}).Stream(t.context, buf)
		if err != nil {
			return fmt.Errorf("fetching torrent bytes failed: %w", err)
		}
		if err := s.forwardTorrentBytes(buf.Bytes(), category, savePath); err != nil {
			return err
		}
		return s.recordRemoteDownload(dialogId, messageId, filename+".torrent", category, savePath, dialog.SessionId)

	default:
		// Direct media: start our own downloader under the fake hash.
		_, err = s.dm.Start(t.context, t.client.API(), doc, dialog.SessionId, dialogId, messageId, filename, category, savePath)
		return err
	}
}

// parseTellarrURL extracts the pinned aggregator link (x.tellurl) from a
// magnet produced by our torznab feed.
func parseTellarrURL(raw string) string {
	if !strings.HasPrefix(raw, "magnet:") {
		return ""
	}
	mu, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(mu.Query().Get("x.tellurl"))
}

// externalHash is the stable id for downloads added from a bare URL
// (no Telegram message backing).
func externalHash(rawURL string) string {
	sum := sha1.Sum([]byte("tellarr:url:" + rawURL))
	return hex.EncodeToString(sum[:])
}

// startExternalDownload records an aria2-backed download row immediately and
// resolves + hands off the link in the background so qBittorrent gets a fast
// "Ok." response.
func (s *Server) startExternalDownload(ctx context.Context, sessionId, dialogId, messageId int64, msg *tg.Message, providerURL, category, savePath string) error {
	title := titleForLink(msg, providerURL)
	var id string
	if dialogId != 0 || messageId != 0 {
		id = SyntheticHash(dialogId, messageId, title)
	} else {
		id = externalHash(providerURL)
	}
	if existing, err := s.downloadRepo.Get(id); err == nil && existing != nil && existing.State == dbm.StateDownloading {
		return nil
	}
	if strings.TrimSpace(savePath) == "" {
		savePath = s.dm.baseDir
	}
	now := time.Now().UTC()
	row := dbm.TorrentDownload{
		ID:        id,
		SessionId: sessionId,
		DialogId:  dialogId,
		MessageId: messageId,
		Filename:  title,
		State:     dbm.StateDownloading,
		Origin:    dbm.OriginAria2,
		Category:  category,
		SavePath:  savePath,
		SourceURL: providerURL,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.downloadRepo.Create(row); err != nil {
		return err
	}
	go s.runExternalDownload(context.Background(), id, providerURL, savePath)
	return nil
}

// runExternalDownload resolves the aggregator page and submits the direct
// link to aria2c. Runs on its own goroutine; results land in DOWNLOADS.
func (s *Server) runExternalDownload(ctx context.Context, id, providerURL, dir string) {
	res, err := linkresolver.Resolve(ctx, providerURL)
	if err != nil {
		slog.Error("external link resolve failed", "url", providerURL, "err", err)
		_ = s.downloadRepo.UpdateProgress(id, 0, dbm.StateError, err.Error())
		return
	}

	name := sanitizeExternalFilename(res.Filename)
	opts := Aria2Options{Dir: dir, Out: name}
	for k, v := range res.Headers {
		opts.Headers = append(opts.Headers, k+": "+v)
	}

	aria := NewAria2ClientFromEnv()
	gid, err := aria.AddURI(ctx, res.URL, opts)
	if err != nil {
		slog.Error("aria2 addUri failed", "url", res.URL, "err", err)
		_ = s.downloadRepo.UpdateProgress(id, 0, dbm.StateError, err.Error())
		return
	}
	contentPath := filepath.Join(dir, name)
	if err := s.downloadRepo.UpdateAriaProgress(id, 0, res.Size, dbm.StateDownloading, contentPath, name, ""); err != nil {
		slog.Error("failed to persist aria2 download", "id", id, "err", err)
	}
	if err := s.downloadRepo.SetRemoteGid(id, gid); err != nil {
		slog.Error("failed to persist aria2 gid", "id", id, "err", err)
	}
	slog.Info("external download started", "id", id, "gid", gid, "file", name)
}

func sanitizeExternalFilename(name string) string {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	name = filepath.Base(name)
	if name == "" || name == "." || name == "/" || name == filepath.VolumeName(name) {
		name = "download.bin"
	}
	return name
}

func (s *Server) forwardTorrentBytes(data []byte, category, savePath string) error {
	qb := NewQBitRealClientFromEnv()
	if !qb.Configured() {
		return fmt.Errorf("real qbittorrent not configured")
	}
	return qb.AddTorrentBytes(data, category, savePath)
}

func (s *Server) recordRemoteDownload(dialogId, messageId int64, filename, category, savePath string, sessionId int64) error {
	id := SyntheticHash(dialogId, messageId, filename)
	existing, err := s.downloadRepo.Get(id)
	if err == nil && existing != nil {
		return nil
	}
	row := dbm.TorrentDownload{
		ID:          id,
		SessionId:   sessionId,
		DialogId:    dialogId,
		MessageId:   messageId,
		Filename:    filename,
		State:       dbm.StateRemote,
		Origin:      dbm.OriginExternalQb,
		Category:    category,
		SavePath:    savePath,
		ContentPath: savePath + "/" + strings.TrimSuffix(filename, ".torrent"),
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	return s.downloadRepo.Create(row)
}

func (s *Server) qbTorrentRows() ([]dbm.TorrentDownload, []RemoteTorrent, error) {
	local, err := s.dm.List()
	var remote []RemoteTorrent
	if qb := NewQBitRealClientFromEnv(); qb.Configured() {
		if remotes, err := qb.TorrentsInfo(); err == nil {
			remote = remotes
		} else {
			slog.Error("real qbit info failed", "err", err)
		}
	}
	s.refreshAriaRows(local)
	return local, remote, err
}

// refreshAriaRows pulls live status for aria2-backed rows from the RPC and
// persists it so torrents/info and sync/maindata serve fresh data.
func (s *Server) refreshAriaRows(rows []dbm.TorrentDownload) {
	aria := NewAria2ClientFromEnv()
	if !aria.Configured() {
		return
	}
	for i := range rows {
		row := &rows[i]
		if row.Origin != dbm.OriginAria2 || row.RemoteGid == "" {
			continue
		}
		switch row.State {
		case dbm.StateDownloading, dbm.StatePaused, dbm.StateDone:
		default:
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		st, err := aria.TellStatus(ctx, row.RemoteGid)
		cancel()
		if err != nil {
			slog.Error("aria2 status failed", "gid", row.RemoteGid, "err", err)
			continue
		}
		newState, _ := MapAriaStatus(st.Status)
		written := st.Int64(st.CompletedLength)
		total := st.Int64(st.TotalLength)
		contentPath := row.ContentPath
		filename := row.Filename
		if len(st.Files) > 0 && st.Files[0].Path != "" {
			contentPath = st.Files[0].Path
			if base := filepath.Base(contentPath); base != "" && base != "." && base != "/" {
				filename = base
			}
		}
		if row.State == newState && written == row.Written && total == row.Total &&
			contentPath == row.ContentPath && filename == row.Filename {
			continue
		}
		if err := s.downloadRepo.UpdateAriaProgress(row.ID, written, total, newState, contentPath, filename, st.ErrorMessage); err != nil {
			slog.Error("failed to persist aria2 progress", "id", row.ID, "err", err)
		}
		row.Written = written
		row.Total = total
		row.State = newState
		row.ContentPath = contentPath
		row.Filename = filename
	}
}

func localToQbInfo(row dbm.TorrentDownload) qbTorrentInfo {
	progress := 0.0
	if row.Total > 0 {
		progress = float64(row.Written) / float64(row.Total)
		if progress > 1 {
			progress = 1
		}
	}
	state := "downloading"
	switch row.State {
	case dbm.StateDone:
		state = "pausedUP"
	case dbm.StatePaused:
		state = "pausedDL"
	case dbm.StateError:
		state = "errored"
	case dbm.StateRemote:
		state = "downloading"
	}
	eta := int64(8640000)
	if progress > 0 && progress < 1 {
		elapsed := time.Since(row.CreatedAt).Seconds()
		if elapsed > 1 {
			rate := float64(row.Written) / elapsed
			if rate > 1 {
				eta = int64(float64(row.Total-row.Written) / rate)
			}
		}
	} else if progress >= 1 {
		eta = 0
	}
	return qbTorrentInfo{
		AddedOn:     row.CreatedAt.Unix(),
		Category:    row.Category,
		Completed:   row.Written,
		ContentPath: row.ContentPath,
		Eta:         eta,
		Hash:        row.ID,
		Name:        row.Filename,
		Progress:    progress,
		SavePath:    row.SavePath,
		Size:        row.Total,
		State:       state,
	}
}

func (s *Server) qbTorrentsInfo(w http.ResponseWriter, r *http.Request) {
	local, remote, err := s.qbTorrentRows()
	if err != nil {
		slog.Error("list downloads failed", "err", err)
	}
	hashFilter := r.URL.Query().Get("hashes")
	categoryFilter := r.URL.Query().Get("category")

	out := make([]qbTorrentInfo, 0, len(local)+len(remote))
	for _, row := range local {
		info := localToQbInfo(row)
		out = append(out, info)
	}
	for _, rt := range remote {
		info := qbTorrentInfo{
			AddedOn:     0,
			Category:    rt.Category,
			Completed:   int64(rt.Progress * float64(rt.Size)),
			ContentPath: rt.ContentPath,
			DlSpeed:     rt.DlSpeed,
			Eta:         rt.Eta,
			Hash:        rt.Hash,
			Name:        rt.Name,
			Progress:    rt.Progress,
			SavePath:    rt.SavePath,
			Size:        rt.Size,
			State:       normalizeRemoteState(rt.State),
		}
		out = append(out, info)
	}

	filtered := out[:0]
	for _, info := range out {
		if hashFilter != "" && hashFilter != "all" {
			match := false
			for _, h := range strings.Split(hashFilter, "|") {
				if h == info.Hash {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		if categoryFilter != "" && info.Category != categoryFilter {
			continue
		}
		filtered = append(filtered, info)
	}
	writeJSONRaw(w, filtered)
}

func normalizeRemoteState(state string) string {
	switch state {
	case "":
		return "downloading"
	default:
		return state
	}
}

func (s *Server) qbTorrentFiles(w http.ResponseWriter, r *http.Request) {
	hash := r.URL.Query().Get("hash")
	row, err := s.dm.Get(hash)
	if err != nil || row == nil {
		// Not an active telegram transfer: try persisted rows (aria2 etc).
		row, err = s.downloadRepo.Get(hash)
	}
	if err != nil || row == nil {
		writeJSONRaw(w, []any{})
		return
	}
	progress := 0.0
	if row.Total > 0 {
		progress = float64(row.Written) / float64(row.Total)
	}
	writeJSONRaw(w, []map[string]any{{
		"index":    0,
		"name":     row.Filename,
		"size":     row.Total,
		"progress": progress,
	}})
}

type torrentActionFn func(localRow dbm.TorrentDownload) error

func ariaPause(a *Aria2Client, hashes []string) error {
	return ariaEach(a, hashes, func(a *Aria2Client, gid string) error {
		return a.Pause(context.Background(), gid)
	})
}

func ariaResume(a *Aria2Client, hashes []string) error {
	return ariaEach(a, hashes, func(a *Aria2Client, gid string) error {
		return a.Unpause(context.Background(), gid)
	})
}

func ariaRemove(a *Aria2Client, hashes []string) error {
	return ariaEach(a, hashes, func(a *Aria2Client, gid string) error {
		return a.Remove(context.Background(), gid)
	})
}

func qbPause(qb *QBitRealClient, hashes []string) error { return qb.Pause(hashes) }

func qbResume(qb *QBitRealClient, hashes []string) error { return qb.Resume(hashes) }

func ariaEach(a *Aria2Client, hashes []string, fn func(*Aria2Client, string) error) error {
	var firstErr error
	for _, h := range hashes {
		if err := fn(a, h); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// wrapTorrentAction routes pause/resume/stop/start to local telegram rows,
// aria2 rows and remote-origin hashes to the real qBittorrent.
func (s *Server) wrapTorrentAction(localFn torrentActionFn, ariaFn func(a *Aria2Client, hashes []string) error, remoteFn func(qb *QBitRealClient, hashes []string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.Write([]byte("Fails."))
			return
		}
		raw := r.FormValue("hashes")
		if raw == "" {
			w.Write([]byte("Fails."))
			return
		}
		rows, _, _ := s.qbTorrentRows()
		localByID := make(map[string]dbm.TorrentDownload)
		for _, row := range rows {
			localByID[row.ID] = row
		}
		qb := NewQBitRealClientFromEnv()
		failed := 0
		var remoteHashes []string
		var ariaHashes []string

		handle := func(h string) {
			if row, ok := localByID[h]; ok {
				switch row.Origin {
				case dbm.OriginTelegram:
					if err := localFn(row); err != nil {
						slog.Error("local action failed", "id", row.ID, "err", err)
						failed++
					}
					delete(localByID, h)
					return
				case dbm.OriginAria2:
					if row.RemoteGid != "" {
						ariaHashes = append(ariaHashes, row.RemoteGid)
					}
					delete(localByID, h)
					return
				case dbm.OriginExternalQb:
					remoteHashes = append(remoteHashes, h)
					delete(localByID, h)
					return
				}
			}
			if qb.Configured() {
				remoteHashes = append(remoteHashes, h)
			} else {
				failed++
			}
		}

		for _, h := range strings.Split(raw, "|") {
			h = strings.TrimSpace(h)
			if h == "" {
				continue
			}
			if h == "all" {
				for _, row := range rows {
					switch row.Origin {
					case dbm.OriginTelegram:
						if err := localFn(row); err != nil {
							slog.Error("local action failed", "id", row.ID, "err", err)
							failed++
						}
					case dbm.OriginAria2:
						if row.RemoteGid != "" {
							ariaHashes = append(ariaHashes, row.RemoteGid)
						}
					default:
						remoteHashes = append(remoteHashes, row.ID)
					}
				}
				continue
			}
			handle(h)
		}

		if len(ariaHashes) > 0 {
			a := NewAria2ClientFromEnv()
			if !a.Configured() {
				failed++
			} else if err := ariaFn(a, ariaHashes); err != nil {
				slog.Error("aria2 action failed", "hashes", ariaHashes, "err", err)
				failed++
			}
		}
		if len(remoteHashes) > 0 && qb.Configured() {
			if err := remoteFn(qb, remoteHashes); err != nil {
				slog.Error("remote action failed", "hashes", remoteHashes, "err", err)
				failed++
			}
		}
		if failed > 0 {
			w.Write([]byte("Fails."))
			return
		}
		w.Write([]byte("Ok."))
	}
}

func (s *Server) pauseTorrents(row dbm.TorrentDownload) error {
	return s.dm.Pause(row.ID)
}

func (s *Server) resumeTorrents(row dbm.TorrentDownload) error {
	_, err := s.RestartDownload(&row)
	return err
}

func (s *Server) qbDeleteTorrents(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		w.Write([]byte("Fails."))
		return
	}
	raw := r.FormValue("hashes")
	deleteFiles := r.FormValue("deleteFiles") == "true"
	if raw == "" {
		w.Write([]byte("Fails."))
		return
	}
	rows, _, _ := s.qbTorrentRows()
	qb := NewQBitRealClientFromEnv()
	rowByID := make(map[string]dbm.TorrentDownload, len(rows))
	for _, row := range rows {
		rowByID[row.ID] = row
	}

	failed := 0
	var remoteHashes []string
	var ariaGIDs []string
	for _, h := range strings.Split(raw, "|") {
		if h == "all" {
			for _, row := range rows {
				switch row.Origin {
				case dbm.OriginTelegram:
					if err := s.deleteLocal(row, deleteFiles); err != nil {
						failed++
					}
				case dbm.OriginAria2:
					if row.RemoteGid != "" {
						ariaGIDs = append(ariaGIDs, row.RemoteGid)
					}
					if deleteFiles && row.ContentPath != "" {
						_ = os.Remove(row.ContentPath)
					}
				default:
					remoteHashes = append(remoteHashes, row.ID)
				}
			}
			continue
		}
		found := false
		for _, row := range rows {
			if row.ID == h {
				found = true
				switch row.Origin {
				case dbm.OriginTelegram:
					if err := s.deleteLocal(row, deleteFiles); err != nil {
						failed++
					}
				case dbm.OriginAria2:
					if row.RemoteGid != "" {
						ariaGIDs = append(ariaGIDs, row.RemoteGid)
					}
					if deleteFiles && row.ContentPath != "" {
						_ = os.Remove(row.ContentPath)
					}
				default:
					remoteHashes = append(remoteHashes, h)
				}
				break
			}
		}
		if !found {
			remoteHashes = append(remoteHashes, h)
		}
	}
	if len(ariaGIDs) > 0 {
		a := NewAria2ClientFromEnv()
		if !a.Configured() {
			failed++
		} else if err := ariaRemove(a, ariaGIDs); err != nil {
			slog.Error("aria2 delete failed", "err", err)
			failed++
		}
		for _, gid := range ariaGIDs {
			for id, row := range rowByID {
				if row.RemoteGid == gid && row.Origin == dbm.OriginAria2 {
					if err := s.downloadRepo.Delete(id); err != nil {
						slog.Error("failed to remove aria2 download row", "id", id, "err", err)
					}
				}
			}
		}
	}
	if len(remoteHashes) > 0 && qb.Configured() {
		if err := qb.Delete(remoteHashes, deleteFiles); err != nil {
			slog.Error("remote delete failed", "err", err)
			failed++
		}
	}
	if failed > 0 {
		w.Write([]byte("Fails."))
		return
	}
	w.Write([]byte("Ok."))
}

func (s *Server) deleteLocal(row dbm.TorrentDownload, deleteFiles bool) error {
	if err := s.dm.Remove(row.ID); err != nil {
		return err
	}
	if deleteFiles && row.ContentPath != "" {
		_ = os.Remove(row.ContentPath)
	}
	return nil
}

func (s *Server) qbSyncMaindata(w http.ResponseWriter, r *http.Request) {
	local, remote, _ := s.qbTorrentRows()
	rid, _ := strconv.Atoi(r.URL.Query().Get("rid"))
	torrents := make(map[string]qbTorrentInfo)
	for _, row := range local {
		torrents[row.ID] = localToQbInfo(row)
	}
	for _, rt := range remote {
		torrents[rt.Hash] = qbTorrentInfo{
			Hash:        rt.Hash,
			Name:        rt.Name,
			Size:        rt.Size,
			Progress:    rt.Progress,
			State:       normalizeRemoteState(rt.State),
			DlSpeed:     rt.DlSpeed,
			Eta:         rt.Eta,
			Category:    rt.Category,
			SavePath:    rt.SavePath,
			ContentPath: rt.ContentPath,
		}
	}
	writeJSONRaw(w, map[string]any{
		"rid":         rid + 1,
		"full_update": true,
		"torrents":    torrents,
		"categories":  map[string]any{},
		"tags":        []string{},
	})
}
