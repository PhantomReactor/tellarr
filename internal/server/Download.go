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

	// speed sampling state (guarded by dm.mu): bytes seen at lastSample.
	lastWritten int64
	lastSample  time.Time
	speed       int64
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

// DocResolver re-resolves a stored download's Telegram media (and a download
// API for it) when a queued item is promoted to an active transfer. Resolving
// late keeps file references fresh no matter how long the queue waited.
type DocResolver func(sessionId, dialogId, messageId int64) (*tg.Client, *tg.Document, error)

type DownloadManager struct {
	mu      sync.Mutex
	repo    database.DownloadsRepository
	live    map[string]*liveDownload
	baseDir string

	// slots caps concurrent transfers; queue holds rows waiting for one.
	slots   *slotLimiter
	queue   []db.TorrentDownload
	resolve DocResolver
}

// slotLimiter is a counting semaphore whose cap can be changed at runtime
// (Settings page). Acquire is non-blocking: full means "queue it".
type slotLimiter struct {
	mu    sync.Mutex
	limit int
	held  int
}

func newSlotLimiter(limit int) *slotLimiter { return &slotLimiter{limit: clampParallel(limit)} }

func (l *slotLimiter) tryAcquire() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held >= l.limit {
		return false
	}
	l.held++
	return true
}

func (l *slotLimiter) release() {
	l.mu.Lock()
	if l.held > 0 {
		l.held--
	}
	l.mu.Unlock()
}

func (l *slotLimiter) setLimit(limit int) {
	l.mu.Lock()
	l.limit = clampParallel(limit)
	l.mu.Unlock()
}

func (l *slotLimiter) getLimit() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.limit
}

func NewDownloadManager(repo database.DownloadsRepository, baseDir string, maxParallel int) *DownloadManager {
	return &DownloadManager{
		repo:    repo,
		live:    make(map[string]*liveDownload),
		baseDir: baseDir,
		slots:   newSlotLimiter(maxParallel),
	}
}

// MaxParallel reports how many files may transfer at once.
func (dm *DownloadManager) MaxParallel() int { return dm.slots.getLimit() }

// SetMaxParallel changes the transfer cap; callers should follow with pump()
// so queued downloads can claim any newly available slots.
func (dm *DownloadManager) SetMaxParallel(n int) {
	dm.slots.setLimit(n)
}

func (dm *DownloadManager) path(filename string) string {
	return filepath.Join(dm.baseDir, filepath.Base(filename))
}

// maybeFlush throttles progress persistence to once per second per download.
// It persists synchronously (outside the lock) so a flush always lands before
// the finalize write in Start's goroutine; async writes here used to race with
// pause/completion and resurrect stale "downloading" states.
func (dm *DownloadManager) maybeFlush(id string) {
	dm.mu.Lock()
	live, ok := dm.live[id]
	if !ok {
		dm.mu.Unlock()
		return
	}
	now := time.Now()
	if now.Sub(live.lastFlush) < time.Second {
		dm.mu.Unlock()
		return
	}
	live.lastFlush = now
	written := live.written.Load()
	state := db.StateDownloading
	if live.total > 0 && written >= live.total {
		state = db.StateDone
	}
	dm.mu.Unlock()
	if err := dm.repo.UpdateProgress(id, written, state, ""); err != nil {
		slog.Error("failed to persist download progress", "id", id, "err", err)
	}
}

func (dm *DownloadManager) Start(ctx context.Context, api *tg.Client, doc *tg.Document, sessionId, dialogId, messageId int64, filename, category, savePath string) (*db.TorrentDownload, error) {
	id := SyntheticHash(dialogId, messageId, filename)

	existing, err := dm.repo.Get(id)
	if err != nil {
		return nil, err
	}
	if existing != nil && (existing.State == db.StateDownloading || existing.State == db.StateQueued) {
		return existing, nil
	}
	if strings.TrimSpace(savePath) == "" {
		savePath = dm.baseDir
	}

	// A slot decides whether the row is born downloading or queued; both
	// states are persisted up front so restarts keep the picture accurate.
	dm.mu.Lock()
	queued := !dm.slots.tryAcquire()
	state := db.StateDownloading
	if queued {
		state = db.StateQueued
	}
	row := &db.TorrentDownload{
		ID:          id,
		SessionId:   sessionId,
		DialogId:    dialogId,
		MessageId:   messageId,
		Filename:    filename,
		Total:       doc.Size,
		Written:     0,
		State:       state,
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
		dm.mu.Unlock()
		cancel()
		return nil, err
	}
	if queued {
		dm.queue = append(dm.queue, *row)
		dm.mu.Unlock()
		slog.Info("download queued", "id", id, "name", filename)
		return row, nil
	}
	dm.live[id] = live
	dm.mu.Unlock()

	if err := dm.openTransfer(ctx, row, live, api, doc); err != nil {
		dm.releaseSlot()
		cancel()
		dm.mu.Lock()
		delete(dm.live, id)
		dm.mu.Unlock()
		_ = dm.repo.UpdateProgress(id, 0, db.StateError, err.Error())
		dm.pump()
		return nil, err
	}
	return row, nil
}

// openTransfer creates the output file and spawns the transfer goroutine.
// The caller must have registered live in dm.live and hold a slot.
func (dm *DownloadManager) openTransfer(ctx context.Context, row *db.TorrentDownload, live *liveDownload, api *tg.Client, doc *tg.Document) error {
	file, err := os.Create(dm.path(row.Filename))
	if err != nil {
		return err
	}
	go dm.runTransfer(ctx, file, row, live, api, doc)
	return nil
}

// runTransfer drives one active download and returns its slot to the queue
// when it ends, whatever the reason.
func (dm *DownloadManager) runTransfer(ctx context.Context, file *os.File, row *db.TorrentDownload, live *liveDownload, api *tg.Client, doc *tg.Document) {
	id := row.ID
	defer func() {
		live.cancel()
		dm.releaseSlot()
		dm.mu.Lock()
		delete(dm.live, id)
		dm.mu.Unlock()
		dm.pump()
	}()
	defer file.Close()

	d := downloader.NewDownloader().WithPartSize(downloadPartSize)
	writer := &progressWriter{id: id, live: live, w: file, dm: dm}
	_, dlErr := d.Download(api, &tg.InputDocumentFileLocation{
		ID:            doc.ID,
		AccessHash:    doc.AccessHash,
		FileReference: doc.FileReference,
	}).WithThreads(bestThreads(doc.Size, maxDownloadThreads)).Parallel(ctx, writer)

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
}

// releaseSlot frees one transfer slot. Callers should follow with pump().
func (dm *DownloadManager) releaseSlot() {
	dm.slots.release()
}

// pump promotes queued downloads into free slots, FIFO. Resolution runs in a
// goroutine so a slow Telegram round-trip never blocks the caller.
func (dm *DownloadManager) pump() {
	for {
		dm.mu.Lock()
		if len(dm.queue) == 0 {
			dm.mu.Unlock()
			return
		}
		if !dm.slots.tryAcquire() {
			dm.mu.Unlock()
			return
		}
		item := dm.queue[0]
		dm.queue = dm.queue[1:]
		dm.mu.Unlock()

		go func(item db.TorrentDownload) {
			dm.promote(item)
		}(item)
	}
}

// promote starts one queued download. A slot has already been reserved by
// pump; every exit path must give it back.
func (dm *DownloadManager) promote(item db.TorrentDownload) {
	defer func() {
		dm.releaseSlot()
		dm.pump()
	}()

	row, err := dm.repo.Get(item.ID)
	if err != nil || row == nil || row.State != db.StateQueued {
		// paused, deleted or otherwise superseded while waiting
		return
	}
	if dm.resolve == nil {
		slog.Error("no document resolver configured, failing queued download", "id", item.ID)
		_ = dm.repo.SetState(item.ID, db.StateError)
		return
	}
	api, doc, err := dm.resolve(item.SessionId, item.DialogId, item.MessageId)
	if err != nil {
		slog.Error("queued download resolve failed", "id", item.ID, "err", err)
		_ = dm.repo.UpdateProgress(item.ID, item.Written, db.StateError, err.Error())
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	live := &liveDownload{cancel: cancel, total: doc.Size}

	dm.mu.Lock()
	dm.live[item.ID] = live
	dm.mu.Unlock()

	_ = dm.repo.SetState(item.ID, db.StateDownloading)
	slog.Info("download started from queue", "id", item.ID, "name", row.Filename)

	if err := dm.openTransfer(ctx, row, live, api, doc); err != nil {
		slog.Error("queued download failed to start", "id", item.ID, "err", err)
		cancel()
		dm.mu.Lock()
		delete(dm.live, item.ID)
		dm.mu.Unlock()
		_ = dm.repo.UpdateProgress(item.ID, 0, db.StateError, err.Error())
	}
}

// sampleSpeed derives a bytes-per-second estimate from the delta since the
// previous call. Callers must hold dm.mu; the UI polls every couple of
// seconds, which is a natural sampling window. A short exponential moving
// average keeps the displayed number from jumping around.
func sampleSpeed(live *liveDownload) int64 {
	now := time.Now()
	written := live.written.Load()
	if live.lastSample.IsZero() {
		live.lastSample = now
		live.lastWritten = written
		return live.speed
	}
	elapsed := now.Sub(live.lastSample).Seconds()
	if elapsed < 0.75 {
		return live.speed
	}
	rate := float64(written-live.lastWritten) / elapsed
	live.lastSample = now
	live.lastWritten = written
	const alpha = 0.6
	smoothed := alpha*rate + (1-alpha)*float64(live.speed)
	if smoothed < 0 {
		smoothed = 0
	}
	live.speed = int64(smoothed)
	return live.speed
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
	for i := range rows {
		if live, ok := dm.live[rows[i].ID]; ok {
			rows[i].Written = live.written.Load()
			rows[i].Speed = sampleSpeed(live)
			if rows[i].State == db.StateDownloading && live.total > 0 && rows[i].Written >= live.total {
				rows[i].State = db.StateDone
			}
		}
	}
	dm.mu.Unlock()
	return rows, nil
}

// dequeueLocked drops an id from the wait list; caller holds dm.mu.
func (dm *DownloadManager) dequeueLocked(id string) {
	for i := range dm.queue {
		if dm.queue[i].ID == id {
			dm.queue = append(dm.queue[:i], dm.queue[i+1:]...)
			return
		}
	}
}

func (dm *DownloadManager) Pause(id string) error {
	dm.mu.Lock()
	live, ok := dm.live[id]
	if !ok {
		dm.dequeueLocked(id)
	}
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
	dm.dequeueLocked(id)
	dm.mu.Unlock()
	return dm.repo.Delete(id)
}

func (dm *DownloadManager) FileExists(row *db.TorrentDownload) bool {
	_, err := os.Stat(row.ContentPath)
	return err == nil
}

// RestartDownload re-resolves a stored download's Telegram message and starts
// the transfer again (used for resume-after-restart and pause/resume).
// aria2-backed rows are resumed through the RPC instead. Full slots put the
// row back into the queue rather than starting it immediately.
func (s *Server) RestartDownload(row *db.TorrentDownload) (*db.TorrentDownload, error) {
	if row.Origin == db.OriginAria2 {
		return s.restartExternalDownload(row)
	}
	api, doc, err := s.resolveDownloadMedia(row.SessionId, row.DialogId, row.MessageId)
	if err != nil {
		return nil, err
	}
	return s.dm.Start(context.Background(), api, doc, row.SessionId, row.DialogId, row.MessageId, row.Filename, row.Category, row.SavePath)
}

// resolveDownloadMedia fetches a fresh document handle plus a download API
// (pooled connection) for it. Shared by new downloads and queued promotions.
func (s *Server) resolveDownloadMedia(sessionId, dialogId, messageId int64) (*tg.Client, *tg.Document, error) {
	t, err := s.getTelegramClient(sessionId)
	if err != nil {
		return nil, nil, fmt.Errorf("telegram session unavailable: %w", err)
	}
	doc, err := s.fetchDocument(t, dialogId, messageId)
	if err != nil {
		return nil, nil, err
	}
	api, err := t.downloadAPI(t.context, doc.DCID)
	if err != nil {
		return nil, nil, fmt.Errorf("download pool unavailable: %w", err)
	}
	return api, doc, nil
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
		go s.runExternalDownload(context.Background(), row.ID, row.SourceURL, dir, row.Filename)
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
	go s.runExternalDownload(context.Background(), row.ID, targetURL, dir, row.Filename)
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
