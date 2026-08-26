package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/pool"
	"github.com/gotd/td/rpc"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// Download throughput tuning, mirroring tdl (github.com/iyear/tdl): chunk
// fetches are spread over several parallel MTProto connections instead of the
// single connection gotd uses by default (the usual ~1 MB/s bottleneck),
// chunks are the maximum size Telegram accepts, and enough workers run to
// keep every connection busy.
//
// Tunables:
//   - MAX_PARALLEL_DOWNLOADS: how many files transfer at once; extra
//     requests wait in the queue (default 3). The value stored in the
//     settings table (Settings page) overrides it and can be changed at
//     runtime; the env var is only the initial fallback.
//   - DOWNLOAD_THREADS: parallel chunk workers per file, 1..16 (default 16)
const (
	downloadPoolSize = 8           // parallel connections per DC
	downloadPartSize = 1024 * 1024 // 1 MiB, maximum chunk per upload.getFile

	// SettingMaxParallelDownloads is the settings-table key holding how many
	// files may transfer at once.
	SettingMaxParallelDownloads = "max_parallel_downloads"
)

var maxDownloadThreads = envCount("DOWNLOAD_THREADS", 16)

func envCount(name string, def int) int {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return min(n, 16)
		}
		slog.Warn("invalid value for tuning env, using default", "env", name, "value", v, "default", def)
	}
	return def
}

func defaultMaxParallelDownloads() int {
	return envCount("MAX_PARALLEL_DOWNLOADS", 3)
}

// clampParallel keeps the transfer limit within what the connection pool
// (downloadPoolSize per DC) can sensibly serve.
func clampParallel(n int) int {
	return min(max(n, 1), 16)
}

// parseParallelSetting converts a stored settings value into a clamped
// limit; ok reports whether the value was usable.
func parseParallelSetting(v string) (n int, ok bool) {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 1 {
		return 0, false
	}
	return clampParallel(n), true
}

// bestThreads scales worker count by file size so small files don't hammer
// Telegram with pointless parallelism (same ladder as tdl).
func bestThreads(size int64, max int) int {
	switch {
	case size < 1<<20:
		return min(1, max)
	case size < 5<<20:
		return min(2, max)
	case size < 20<<20:
		return min(4, max)
	case size < 50<<20:
		return min(8, max)
	default:
		return max
	}
}

// floodWaitInvoker retries RPCs rejected with FLOOD_WAIT after the
// server-requested delay, so long parallel transfers survive transient
// throttling instead of dying mid-file. Bounded to keep pauses responsive.
type floodWaitInvoker struct{ next tg.Invoker }

func (f floodWaitInvoker) Invoke(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
	for attempt := 0; ; attempt++ {
		err := f.next.Invoke(ctx, input, output)
		if err == nil {
			return nil
		}
		wait, ok := tgerr.AsFloodWait(err)
		if !ok || attempt >= 5 || wait > time.Minute {
			return err
		}
		slog.Warn("telegram flood wait during download", "wait", wait, "attempt", attempt+1)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait + time.Second):
		}
	}
}

// retryInvoker re-runs RPCs that failed because a pooled connection died
// mid-request (engine closed, connection dead, transport error). A retry
// simply acquires another pooled connection — usually a freshly created
// one — so connection churn no longer aborts a multi-gigabyte transfer.
// Bounded attempts with backoff keep a truly dead session from spinning.
type retryInvoker struct {
	next tg.Invoker
	max  int
}

func (r retryInvoker) Invoke(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
	var err error
	for attempt := 0; ; attempt++ {
		err = r.next.Invoke(ctx, input, output)
		if err == nil || attempt >= r.max || !transientInvokeErr(ctx, err) {
			return err
		}
		delay := time.Duration(attempt+1) * 500 * time.Millisecond
		if delay > 5*time.Second {
			delay = 5 * time.Second
		}
		slog.Warn("transient telegram rpc failure during download, retrying",
			"attempt", attempt+1, "max", r.max, "delay", delay, "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
}

// transientInvokeErr reports whether err looks like a pooled connection
// dying mid-request rather than a permanent RPC failure. A done caller
// context means the download itself was paused/stopped, never retried.
func transientInvokeErr(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	if _, ok := tgerr.AsFloodWait(err); ok {
		return false // handled by floodWaitInvoker
	}
	switch {
	case errors.Is(err, rpc.ErrEngineClosed), // "engine was closed"
		errors.Is(err, pool.ErrConnDead), // "connection dead"
		errors.Is(err, context.Canceled), // "engine forcibly closed"
		errors.Is(err, io.EOF),
		errors.Is(err, io.ErrUnexpectedEOF):
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// downloadAPI returns a *tg.Client whose RPCs are distributed across a pool
// of parallel connections on dc. Pass the document's home DCID: unlike the
// primary client invoker, pooled connections get no automatic FILE_MIGRATE
// handling, and connecting straight to the home DC also skips redirect hops.
// Pools are cached per session+DC and die with the telegram client.
func (t *TelegramSession) downloadAPI(ctx context.Context, dc int) (*tg.Client, error) {
	t.poolMu.Lock()
	defer t.poolMu.Unlock()

	t.mu.RLock()
	sessionCtx := t.context
	t.mu.RUnlock()
	if sessionCtx != nil {
		if sessionCtx.Err() != nil {
			// The session client stopped; cached pool engines died with it.
			t.dlPools = nil
			return nil, fmt.Errorf("telegram session stopped")
		}
		ctx = sessionCtx
	}

	if t.dlPools == nil {
		t.dlPools = make(map[int]tg.Invoker)
	}
	if inv, ok := t.dlPools[dc]; ok {
		return tg.NewClient(inv), nil
	}

	var (
		inv telegram.CloseInvoker
		err error
	)
	if dc == 0 || dc == t.client.Config().ThisDC {
		inv, err = t.client.Pool(downloadPoolSize)
	} else {
		inv, err = t.client.DC(ctx, dc, downloadPoolSize)
	}
	if err != nil {
		return nil, err
	}
	t.dlPools[dc] = floodWaitInvoker{next: retryInvoker{next: inv, max: 8}}
	return tg.NewClient(t.dlPools[dc]), nil
}
