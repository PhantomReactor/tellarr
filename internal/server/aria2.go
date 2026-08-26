package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	dbm "tellarr/internal/database/models"
)

// Aria2Client talks to an aria2c daemon over its HTTP JSON-RPC interface
// (aria2c --enable-rpc). Tellarr does not spawn the process.
type Aria2Client struct {
	baseURL string
	secret  string
	http    *http.Client
}

func NewAria2ClientFromEnv() *Aria2Client {
	return &Aria2Client{
		baseURL: strings.TrimRight(strings.TrimSpace(os.Getenv("ARIA2_RPC_URL")), "/"),
		secret:  os.Getenv("ARIA2_SECRET"),
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (a *Aria2Client) Configured() bool { return a != nil && a.baseURL != "" }

type ariaRequest struct {
	Jsonrpc string `json:"jsonrpc"`
	ID      string `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type ariaError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type ariaResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *ariaError      `json:"error"`
}

// RPCError is a structured error returned by the aria2 JSON-RPC endpoint.
type RPCError struct {
	Code    int
	Message string
}

func (e *RPCError) Error() string { return fmt.Sprintf("aria2 rpc error %d: %s", e.Code, e.Message) }

// IsGidNotFound reports whether err is aria2 rejecting a GID it no longer
// knows about (daemon restarted, finished job pruned from its results, or
// already removed).
func IsGidNotFound(err error) bool {
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) {
		return false
	}
	return rpcErr.Code == 1 && strings.Contains(rpcErr.Message, "is not found")
}

func (a *Aria2Client) call(ctx context.Context, method string, params ...any) (json.RawMessage, error) {
	if !a.Configured() {
		return nil, fmt.Errorf("aria2 rpc not configured")
	}
	if a.secret != "" {
		params = append([]any{"token:" + a.secret}, params...)
	}
	payload, err := json.Marshal(ariaRequest{Jsonrpc: "2.0", ID: "tellarr", Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out ariaResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("aria2: bad rpc response: %w", err)
	}
	if out.Error != nil {
		return nil, &RPCError{Code: out.Error.Code, Message: out.Error.Message}
	}
	return out.Result, nil
}

// Aria2Options are addUri options we care about.
type Aria2Options struct {
	Dir     string
	Out     string
	Headers []string // raw "Key: Value" entries
	Referer string
}

func (a *Aria2Client) AddURI(ctx context.Context, uri string, opts Aria2Options) (string, error) {
	o := map[string]any{}
	if opts.Dir != "" {
		o["dir"] = opts.Dir
	}
	if opts.Out != "" {
		o["out"] = opts.Out
	}
	if len(opts.Headers) > 0 {
		o["header"] = opts.Headers
	}
	if opts.Referer != "" {
		o["referer"] = opts.Referer
	}
	slog.Info("aria2.addUri",
		"uri", uri,
		"dir", opts.Dir,
		"out", opts.Out,
		"headers", len(opts.Headers),
		"referer", opts.Referer,
	)
	res, err := a.call(ctx, "aria2.addUri", []string{uri}, o)
	if err != nil {
		return "", err
	}
	var gid string
	if err := json.Unmarshal(res, &gid); err != nil {
		return "", fmt.Errorf("aria2: unexpected addUri result: %w", err)
	}
	return gid, nil
}

type Aria2File struct {
	Path            string `json:"path"`
	Length          string `json:"length"`
	CompletedLength string `json:"completedLength"`
}

type Aria2Status struct {
	Gid             string      `json:"gid"`
	Status          string      `json:"status"`
	TotalLength     json.Number `json:"totalLength"`
	CompletedLength json.Number `json:"completedLength"`
	DownloadSpeed   json.Number `json:"downloadSpeed"`
	ErrorMessage    string      `json:"errorMessage"`
	Files           []Aria2File `json:"files"`
}

func (s *Aria2Status) Int64(v json.Number) int64 {
	n, _ := v.Int64()
	return n
}

func (a *Aria2Client) TellStatus(ctx context.Context, gid string) (*Aria2Status, error) {
	res, err := a.call(ctx, "aria2.tellStatus", gid,
		[]string{"gid", "status", "totalLength", "completedLength", "downloadSpeed", "errorMessage", "files"})
	if err != nil {
		return nil, err
	}
	var st Aria2Status
	if err := json.Unmarshal(res, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func (a *Aria2Client) Pause(ctx context.Context, gid string) error {
	// forcePause also accepts waiting/queued downloads.
	_, err := a.call(ctx, "aria2.forcePause", gid)
	return err
}

func (a *Aria2Client) Unpause(ctx context.Context, gid string) error {
	_, err := a.call(ctx, "aria2.unpause", gid)
	return err
}

func (a *Aria2Client) Remove(ctx context.Context, gid string) error {
	_, err := a.call(ctx, "aria2.forceRemove", gid)
	return err
}

// MapAriaStatus converts an aria2 status into our DB state plus the qBittorrent
// state string served by the emulated API.
func MapAriaStatus(status string) (dbm.DownloadState, string) {
	switch status {
	case "active":
		return dbm.StateDownloading, "downloading"
	case "waiting":
		return dbm.StateDownloading, "queuedDL"
	case "paused":
		return dbm.StatePaused, "pausedDL"
	case "complete":
		return dbm.StateDone, "pausedUP"
	case "error":
		return dbm.StateError, "errored"
	case "removed":
		return dbm.StateError, "errored"
	default:
		return dbm.StateDownloading, "downloading"
	}
}
