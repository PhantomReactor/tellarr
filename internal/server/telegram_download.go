package server

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/bin"
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
// Tunables (read once from the environment at startup):
//   - MAX_PARALLEL_DOWNLOADS: how many files transfer at once; extra
//     requests wait in the queue (default 3)
//   - DOWNLOAD_THREADS: parallel chunk workers per file, 1..16 (default 16)
const (
	downloadPoolSize = 8           // parallel connections per DC
	downloadPartSize = 1024 * 1024 // 1 MiB, maximum chunk per upload.getFile
)

var (
	maxParallelDownloads = envCount("MAX_PARALLEL_DOWNLOADS", 3)
	maxDownloadThreads   = envCount("DOWNLOAD_THREADS", 16)
)

func envCount(name string, def int) int {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return min(n, 16)
		}
		slog.Warn("invalid value for tuning env, using default", "env", name, "value", v, "default", def)
	}
	return def
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

// downloadAPI returns a *tg.Client whose RPCs are distributed across a pool
// of parallel connections on dc. Pass the document's home DCID: unlike the
// primary client invoker, pooled connections get no automatic FILE_MIGRATE
// handling, and connecting straight to the home DC also skips redirect hops.
// Pools are cached per session+DC and die with the telegram client.
func (t *TelegramSession) downloadAPI(ctx context.Context, dc int) (*tg.Client, error) {
	t.poolMu.Lock()
	defer t.poolMu.Unlock()

	if t.dlPools == nil {
		t.dlPools = make(map[int]tg.Invoker)
	}
	if inv, ok := t.dlPools[dc]; ok {
		return tg.NewClient(inv), nil
	}

	t.mu.RLock()
	sessionCtx := t.context
	t.mu.RUnlock()
	if sessionCtx != nil {
		ctx = sessionCtx
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
	t.dlPools[dc] = floodWaitInvoker{next: inv}
	return tg.NewClient(t.dlPools[dc]), nil
}
