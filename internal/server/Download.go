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

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
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

	if err := dm.openTransfer(ctx, row, live, api, doc, 0); err != nil {
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
// resumeFrom > 0 continues the transfer at that byte offset: the file is
// opened without truncation and the chunk loop starts there.
func (dm *DownloadManager) openTransfer(ctx context.Context, row *db.TorrentDownload, live *liveDownload, api *tg.Client, doc *tg.Document, resumeFrom int64) error {
	var file *os.File
	var err error
	if resumeFrom > 0 {
		file, err = os.OpenFile(dm.path(row.Filename), os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		// Trust the bytes actually on disk over the persisted counter: if the
		// file is shorter than Written, clamp so we never write a hole.
		if st, statErr := file.Stat(); statErr == nil && st.Size() < resumeFrom {
			resumeFrom = st.Size()
		}
	} else {
		file, err = os.Create(dm.path(row.Filename))
		if err != nil {
			return err
		}
	}
	live.written.Store(resumeFrom)
	go dm.runTransfer(ctx, file, row, live, api, doc, resumeFrom)
	return nil
}

// runTransfer drives one active download and returns its slot to the queue
// when it ends, whatever the reason.
//
// Downloads are fetched by a small pool of parallel workers (network is the
// bottleneck: one request in flight is RTT-bound, several are bandwidth-bound)
// but bytes are written strictly sequentially: worker k only issues chunk
// indexes drawn round-robin from a shared counter, chunks land in a bounded
// pending map, and the writer consumes frontier indexes in order. That keeps
// written bytes a contiguous prefix, so resume-after-failure / pause is still
// exact — continue at the stored offset, no holes to reconstruct. Per-chunk
// retries absorb the pooled-connection churn ("engine was closed") that used
// to abort the transfer.
func (dm *DownloadManager) runTransfer(ctx context.Context, file *os.File, row *db.TorrentDownload, live *liveDownload, api *tg.Client, doc *tg.Document, resumeFrom int64) {
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

	location := &tg.InputDocumentFileLocation{
		ID:            doc.ID,
		AccessHash:    doc.AccessHash,
		FileReference: doc.FileReference,
	}
	total := doc.Size
	if resumeFrom >= total {
		// Nothing left to fetch (partial-size weirdness): treat as complete.
		if err := dm.repo.UpdateProgress(id, total, db.StateDone, ""); err != nil {
			slog.Error("failed to finalize download", "id", id, "err", err)
		}
		return
	}

	threads := min(bestThreads(total, maxDownloadThreads), downloadPoolSize)
	parts := (total + downloadPartSize - 1) / downloadPartSize

	// Shared chunk conveyor: workers fill pending[frontier..frontier+window),
	// the writer drains strictly in order. Both sides park on one condition.
	q := &chunkQueue{pending: make(map[int64][]byte, threads*2), eof: -1}
	q.cond = sync.NewCond(&q.mu)
	q.nextToIssue = resumeFrom / downloadPartSize
	q.window = int64(threads * 2)

	var wg sync.WaitGroup
	for w := 0; w < threads; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				q.mu.Lock()
				for !q.stopped && q.err == nil && q.eof < 0 && q.nextToIssue >= q.frontier+q.window {
					q.cond.Wait()
				}
				if q.stopped || q.err != nil || q.eof >= 0 {
					q.mu.Unlock()
					return
				}
				idx := q.nextToIssue
				q.nextToIssue++
				q.mu.Unlock()

				offset := idx * downloadPartSize
				if offset >= total {
					// Raced past EOF: treat as terminator like a short chunk.
					q.mu.Lock()
					if q.eof < 0 {
						q.eof = idx
					}
					q.cond.Broadcast()
					q.mu.Unlock()
					return
				}

				b, err := fetchChunk(ctx, api, location, offset, downloadPartSize)

				q.mu.Lock()
				if err != nil {
					if q.err == nil {
						q.err = err
					}
				} else if len(b) < downloadPartSize && q.eof < 0 {
					q.eof = idx
					if len(b) > 0 {
						q.pending[idx] = b
					}
				} else if len(b) > 0 {
					q.pending[idx] = b
				}
				q.cond.Broadcast()
				q.mu.Unlock()
				if err != nil {
					return
				}
			}
		}()
	}

	done, finalErr, written := false, error(nil), int64(resumeFrom)
	frontier := resumeFrom / downloadPartSize
	for !done {
		var buf []byte
		var failErr error
		q.mu.Lock()
		for {
			if q.stopped {
				break
			}
			if b, ok := q.pending[frontier]; ok {
				delete(q.pending, frontier)
				buf = b
				break
			}
			if q.err != nil {
				failErr = q.err
				break
			}
			q.cond.Wait()
		}
		q.frontier = frontier + 1
		q.cond.Broadcast()
		q.mu.Unlock()

		if buf == nil { // transfer error from a worker
			finalErr = failErr
			break
		}
		offset := frontier * downloadPartSize
		if n, werr := file.WriteAt(buf, offset); werr != nil || n < len(buf) {
			if werr == nil {
				werr = io.ErrShortWrite
			}
			slog.Error("download write failed", "id", id, "err", werr)
			finalErr = fmt.Errorf("disk write: %w", werr)
			written = live.written.Load()
			break
		}
		written = offset + int64(len(buf))
		live.written.Store(written)
		dm.maybeFlush(id)

		// Last chunk reported short? Everything is written, transfer done.
		q.mu.Lock()
		eof := q.eof
		q.mu.Unlock()
		if (eof >= 0 && frontier >= eof) || frontier >= parts-1 || written >= total {
			done = true
		}
		frontier++
	}
	q.mu.Lock()
	q.stopped = true
	q.cond.Broadcast()
	q.mu.Unlock()
	wg.Wait()

	live.written.Store(written)
	switch {
	case done, finalErr == nil:
		if err := dm.repo.UpdateProgress(id, written, db.StateDone, ""); err != nil {
			slog.Error("failed to finalize download", "id", id, "err", err)
		}
	case errors.Is(finalErr, context.Canceled), ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled):
		// paused by user or superseded
		if err := dm.repo.UpdateProgress(id, written, db.StatePaused, ""); err != nil {
			slog.Error("failed to finalize download", "id", id, "err", err)
		}
	default:
		slog.Error("download failed", "id", id, "err", finalErr)
		if err := dm.repo.UpdateProgress(id, written, db.StateError, finalErr.Error()); err != nil {
			slog.Error("failed to finalize download", "id", id, "err", err)
		}
	}
}

// chunkQueue carries prefetched chunks between fetch workers and the
// sequential writer inside runTransfer.
type chunkQueue struct {
	mu          sync.Mutex
	cond        *sync.Cond
	pending     map[int64][]byte // chunk index -> data, frontier..frontier+window
	nextToIssue int64            // next chunk index for any worker to fetch
	frontier    int64            // next chunk index the writer expects
	window      int64            // max in-flight chunks ahead of frontier
	err         error            // first worker error, sticky
	eof         int64            // index of first reported short chunk; -1 = none
	stopped     bool             // writer is done; workers must exit
}

// fetchChunk downloads one chunk at offset with bounded retries on transient
// pooled-connection errors (engine closed, conn dead, EOF, flood wait...).
func fetchChunk(ctx context.Context, api *tg.Client, location *tg.InputDocumentFileLocation, offset int64, limit int) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 10; attempt++ {
		if attempt > 0 {
			delay := min(time.Duration(attempt)*time.Second, 10*time.Second)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
		// Bound each attempt: a half-dead pooled connection with no read
		// deadline would otherwise block forever and leave the row showing
		// "downloading" without any transfer happening.
		attemptCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		req := &tg.UploadGetFileRequest{
			Location: location,
			Offset:   offset,
			Limit:    downloadPartSize,
		}
		r, err := api.UploadGetFile(attemptCtx, req)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if wait, ok := tgerr.AsFloodWait(err); ok {
				lastErr = err
				if wait <= 0 {
					wait = 1
				}
				slog.Warn("telegram flood wait between chunks", "offset", offset, "attempt", attempt+1, "wait", wait)
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Duration(wait) * time.Second):
				}
				continue
			}
			lastErr = err
			slog.Warn("telegram chunk fetch failed, retrying", "offset", offset, "attempt", attempt+1, "err", err)
			continue
		}
		if data, ok := r.(*tg.UploadFile); ok {
			return data.Bytes, nil
		}
		return nil, fmt.Errorf("unexpected getFile response %T", r)
	}
	return nil, lastErr
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

	if err := dm.openTransfer(ctx, row, live, api, doc, 0); err != nil {
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

// IsLive reports whether an active transfer goroutine is registered for id.
func (dm *DownloadManager) IsLive(id string) bool {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	_, ok := dm.live[id]
	return ok
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
// Telegram rows with partial data on disk continue from the stored offset —
// no restart from zero — as long as the partial file still exists and is at
// least as large as the persisted Written counter (the transfer is written
// sequentially, so that offset is a contiguous frontier).
func (s *Server) RestartDownload(row *db.TorrentDownload) (*db.TorrentDownload, error) {
	if row.Origin == db.OriginAria2 {
		return s.restartExternalDownload(row)
	}
	api, doc, err := s.resolveDownloadMedia(row.SessionId, row.DialogId, row.MessageId)
	if err != nil {
		return nil, err
	}

	resumeFrom := int64(0)
	if row.Written > 0 && row.Written < doc.Size && s.dm.FileExists(row) {
		resumeFrom = row.Written
	}
	if resumeFrom == 0 && row.Written >= doc.Size && row.State == db.StateDone {
		// completed file restarted: clean slate
		return s.dm.Start(context.Background(), api, doc, row.SessionId, row.DialogId, row.MessageId, row.Filename, row.Category, row.SavePath)
	}

	id := SyntheticHash(row.DialogId, row.MessageId, row.Filename)
	existing, err := s.dm.repo.Get(id)
	if err != nil {
		return nil, err
	}
	if existing != nil && (existing.State == db.StateDownloading || existing.State == db.StateQueued) {
		return existing, nil
	}

	queued := !s.dm.slots.tryAcquire()
	state := db.StateDownloading
	if queued {
		state = db.StateQueued
	}
	ctx, cancel := context.WithCancel(context.Background())
	live := &liveDownload{cancel: cancel, total: doc.Size}
	s.dm.mu.Lock()
	s.dm.live[id] = live
	if queued {
		s.dm.queue = append(s.dm.queue, *row)
	}
	s.dm.mu.Unlock()
	_ = s.dm.repo.UpdateProgress(id, row.Written, state, "")
	if queued {
		slog.Info("download queued", "id", id, "name", row.Filename)
		return row, nil
	}
	if err := s.dm.openTransfer(ctx, row, live, api, doc, resumeFrom); err != nil {
		s.dm.releaseSlot()
		cancel()
		s.dm.mu.Lock()
		delete(s.dm.live, id)
		s.dm.mu.Unlock()
		_ = s.dm.repo.UpdateProgress(id, row.Written, db.StateError, err.Error())
		s.dm.pump()
		return nil, err
	}
	slog.Info("download resumed", "id", id, "name", row.Filename, "offset", resumeFrom)
	return row, nil
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
