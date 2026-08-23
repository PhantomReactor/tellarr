package server

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
	"tellarr/internal/database"
	db "tellarr/internal/database/models"
)

// SyntheticHash builds the stable 40-hex id used both as download id and as
// the fake magnet btih presented to Sonarr/Radarr.
func SyntheticHash(dialogId, messageId int64, filename string) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("tellarr:%d:%d:%s", dialogId, messageId, filename)))
	return hex.EncodeToString(sum[:])
}

type liveDownload struct {
	cancel    context.CancelFunc
	written   atomic.Int64
	total     int64
	lastFlush time.Time
}

type progressWriter struct {
	id   string
	live *liveDownload
	w    io.WriterAt
	dm   *DownloadManager
}

func (p *progressWriter) WriteAt(b []byte, off int64) (int, error) {
	n, err := p.w.WriteAt(b, off)
	if n > 0 {
		p.live.written.Add(int64(n))
		p.dm.maybeFlush(p.id)
	}
	return n, err
}

type DownloadManager struct {
	mu      sync.Mutex
	repo    database.DownloadsRepository
	live    map[string]*liveDownload
	baseDir string
}

func NewDownloadManager(repo database.DownloadsRepository, baseDir string) *DownloadManager {
	return &DownloadManager{
		repo:    repo,
		live:    make(map[string]*liveDownload),
		baseDir: baseDir,
	}
}

func (dm *DownloadManager) path(filename string) string {
	return filepath.Join(dm.baseDir, filepath.Base(filename))
}

// maybeFlush throttles progress persistence to once per second per download.
func (dm *DownloadManager) maybeFlush(id string) {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	live, ok := dm.live[id]
	if !ok {
		return
	}
	now := time.Now()
	if now.Sub(live.lastFlush) < time.Second {
		return
	}
	live.lastFlush = now
	written := live.written.Load()
	state := db.StateDownloading
	if live.total > 0 && written >= live.total {
		state = db.StateDone
	}
	go func() {
		if err := dm.repo.UpdateProgress(id, written, state, ""); err != nil {
			slog.Error("failed to persist download progress", "id", id, "err", err)
		}
	}()
}

func (dm *DownloadManager) Start(ctx context.Context, api *tg.Client, doc *tg.Document, sessionId, dialogId, messageId int64, filename, category, savePath string) (*db.TorrentDownload, error) {
	id := SyntheticHash(dialogId, messageId, filename)

	existing, err := dm.repo.Get(id)
	if err != nil {
		return nil, err
	}
	if existing != nil && existing.State == db.StateDownloading {
		return existing, nil
	}
	if strings.TrimSpace(savePath) == "" {
		savePath = dm.baseDir
	}
	row := &db.TorrentDownload{
		ID:          id,
		SessionId:   sessionId,
		DialogId:    dialogId,
		MessageId:   messageId,
		Filename:    filename,
		Total:       doc.Size,
		Written:     0,
		State:       db.StateDownloading,
		Origin:      db.OriginTelegram,
		Category:    category,
		SavePath:    savePath,
		ContentPath: filepath.Join(savePath, filepath.Base(filename)),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	ctx, cancel := context.WithCancel(ctx)
	live := &liveDownload{cancel: cancel, total: doc.Size}

	if err := dm.repo.Create(*row); err != nil {
		cancel()
		return nil, err
	}
	dm.live[id] = live

	file, err := os.Create(dm.path(filename))
	if err != nil {
		cancel()
		delete(dm.live, id)
		return nil, err
	}

	go func() {
		defer func() {
			cancel()
			dm.mu.Lock()
			delete(dm.live, id)
			dm.mu.Unlock()
		}()
		defer file.Close()

		d := downloader.NewDownloader()
		writer := &progressWriter{id: id, live: live, w: file, dm: dm}
		_, dlErr := d.Download(api, &tg.InputDocumentFileLocation{
			ID:            doc.ID,
			AccessHash:    doc.AccessHash,
			FileReference: doc.FileReference,
		}).WithThreads(8).Parallel(ctx, writer)

		written := live.written.Load()
		switch {
		case written >= doc.Size:
			dlErr = nil
			if err := dm.repo.UpdateProgress(id, written, db.StateDone, ""); err != nil {
				slog.Error("failed to finalize download", "id", id, "err", err)
			}
		case errors.Is(dlErr, context.Canceled):
			// paused by user or superseded
			if err := dm.repo.UpdateProgress(id, written, db.StatePaused, ""); err != nil {
				slog.Error("failed to finalize download", "id", id, "err", err)
			}
		case dlErr != nil:
			slog.Error("download failed", "id", id, "err", dlErr)
			if err := dm.repo.UpdateProgress(id, written, db.StateError, dlErr.Error()); err != nil {
				slog.Error("failed to finalize download", "id", id, "err", err)
			}
		default:
			// ended cleanly but incomplete (interrupted)
			if err := dm.repo.UpdateProgress(id, written, db.StatePaused, ""); err != nil {
				slog.Error("failed to finalize download", "id", id, "err", err)
			}
		}
	}()

	return row, nil
}

func (dm *DownloadManager) Get(id string) (*db.TorrentDownload, error) {
	row, err := dm.repo.Get(id)
	if err != nil || row == nil {
		return row, err
	}
	dm.mu.Lock()
	live, ok := dm.live[id]
	dm.mu.Unlock()
	if ok {
		row.Written = live.written.Load()
		if row.State == db.StateDownloading && live.total > 0 && row.Written >= live.total {
			row.State = db.StateDone
		}
	}
	return row, nil
}

func (dm *DownloadManager) List() ([]db.TorrentDownload, error) {
	rows, err := dm.repo.List()
	if err != nil {
		return nil, err
	}
	dm.mu.Lock()
	defer dm.mu.Unlock()
	for i := range rows {
		if live, ok := dm.live[rows[i].ID]; ok {
			rows[i].Written = live.written.Load()
		}
	}
	return rows, nil
}

func (dm *DownloadManager) Pause(id string) error {
	dm.mu.Lock()
	live, ok := dm.live[id]
	dm.mu.Unlock()
	if ok {
		live.cancel()
		return nil
	}
	return dm.repo.SetState(id, db.StatePaused)
}

func (dm *DownloadManager) MarkLive(id string) {
	dm.mu.Lock()
	if _, ok := dm.live[id]; ok {
		dm.repo.SetState(id, db.StateDownloading)
	}
	dm.mu.Unlock()
}

func (dm *DownloadManager) Remove(id string) error {
	dm.mu.Lock()
	if live, ok := dm.live[id]; ok {
		live.cancel()
		delete(dm.live, id)
	}
	dm.mu.Unlock()
	return dm.repo.Delete(id)
}

func (dm *DownloadManager) FileExists(row *db.TorrentDownload) bool {
	_, err := os.Stat(row.ContentPath)
	return err == nil
}

// RestartDownload re-resolves a stored download's Telegram message and starts
// the transfer again (used for resume-after-restart and pause/resume).
// aria2-backed rows are resumed through the RPC instead.
func (s *Server) RestartDownload(row *db.TorrentDownload) (*db.TorrentDownload, error) {
	if row.Origin == db.OriginAria2 {
		return s.restartExternalDownload(row)
	}
	t, err := s.getTelegramClient(row.SessionId)
	if err != nil {
		return nil, fmt.Errorf("telegram session unavailable: %w", err)
	}
	doc, err := s.fetchDocument(t, row.DialogId, row.MessageId)
	if err != nil {
		return nil, err
	}
	return s.dm.Start(t.context, t.client.API(), doc, row.SessionId, row.DialogId, row.MessageId, row.Filename, row.Category, row.SavePath)
}

// restartExternalDownload resumes an aria2 download; when the gid is stale
// (daemon restarted) the whole resolve chain runs again.
func (s *Server) restartExternalDownload(row *db.TorrentDownload) (*db.TorrentDownload, error) {
	aria := NewAria2ClientFromEnv()
	if !aria.Configured() {
		return nil, fmt.Errorf("aria2 rpc not configured")
	}
	if row.RemoteGid != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := aria.Unpause(ctx, row.RemoteGid)
		cancel()
		if err == nil {
			_ = s.downloadRepo.SetState(row.ID, db.StateDownloading)
			return row, nil
		}
		slog.Debug("aria2 unpause failed, will re-resolve", "gid", row.RemoteGid, "err", err)
	}
	if row.DialogId == 0 || row.MessageId == 0 {
		// Raw pasted URL without a Telegram backing: only the stored link
		// can be retried.
		if row.SourceURL == "" {
			return nil, fmt.Errorf("cannot re-resolve external download %s", row.ID)
		}
		dir := row.SavePath
		if strings.TrimSpace(dir) == "" {
			dir = s.dm.baseDir
		}
		_ = s.downloadRepo.SetState(row.ID, db.StateDownloading)
		go s.runExternalDownload(context.Background(), row.ID, row.SourceURL, dir)
		return row, nil
	}
	dialog, err := s.dialogRepo.GetDialogsByDialogId(row.DialogId)
	if err != nil || dialog == nil {
		return nil, fmt.Errorf("dialog %d not found", row.DialogId)
	}
	t, err := s.getTelegramClient(dialog.SessionId)
	if err != nil {
		return nil, fmt.Errorf("telegram session unavailable")
	}

	// Prefer the pinned source link; fall back to re-scanning the message.
	targetURL := row.SourceURL
	if targetURL == "" {
		msg, err := s.fetchMessage(t, row.DialogId, row.MessageId)
		if err != nil {
			return nil, err
		}
		urls := providerURLsInMessage(msg)
		if len(urls) == 0 {
			return nil, fmt.Errorf("message no longer contains a supported link")
		}
		targetURL = urls[0]
	}
	dir := row.SavePath
	if strings.TrimSpace(dir) == "" {
		dir = s.dm.baseDir
	}
	_ = s.downloadRepo.SetState(row.ID, db.StateDownloading)
	go s.runExternalDownload(context.Background(), row.ID, targetURL, dir)
	return row, nil
}

// fetchMessage loads a message by channel/message id.
func (s *Server) fetchMessage(t *TelegramSession, channelId, messageId int64) (*tg.Message, error) {
	dialog, err := s.dialogRepo.GetDialogsByDialogId(channelId)
	if err != nil {
		return nil, err
	}
	if dialog == nil {
		return nil, fmt.Errorf("dialog %d not found", channelId)
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
		return nil, err
	}
	var msgs []tg.MessageClass
	if m, ok := messages.(*tg.MessagesChannelMessages); ok {
		msgs = m.Messages
	}
	if len(msgs) == 0 {
		return nil, fmt.Errorf("message %d/%d not found", channelId, messageId)
	}
	msg, ok := msgs[0].(*tg.Message)
	if !ok {
		return nil, fmt.Errorf("message %d/%d is not a plain message", channelId, messageId)
	}
	return msg, nil
}

// fetchDocument loads a message by channel/message id and returns its document.
func (s *Server) fetchDocument(t *TelegramSession, channelId, messageId int64) (*tg.Document, error) {
	msg, err := s.fetchMessage(t, channelId, messageId)
	if err != nil {
		return nil, err
	}
	doc := documentOfMessage(msg)
	if doc == nil {
		return nil, fmt.Errorf("message %d/%d has no media document", channelId, messageId)
	}
	return doc, nil
}
