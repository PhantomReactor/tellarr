package linkresolver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// turnstileSolver produces a valid Cloudflare Turnstile response token for a
// given sitekey + page URL. TMBCloud's gen-link.php validates this token
// server-side (confirmed: a bogus token gets a 403 "Access denied"), so a
// plain scrape can't get past it — this must be a real solve, either via a
// paid solving API (the default here) or a fake stub injected in tests.
type turnstileSolver interface {
	Solve(ctx context.Context, siteKey, pageURL string) (string, error)
}

// capSolverPollInterval/capSolverMaxWait bound how long we'll wait on a
// single solve. CapSolver's own docs say results usually land in 1-20s, but
// under load or for hard challenges it can take longer; capSolverMaxWait
// caps the total so a stuck task can't hang a download forever.
const (
	capSolverPollInterval = 2 * time.Second
	capSolverMaxWait      = 120 * time.Second
)

// capSolverClient implements turnstileSolver against api.capsolver.com's
// AntiTurnstileTaskProxyLess task type.
// https://docs.capsolver.com/en/guide/captcha/cloudflare_turnstile/
type capSolverClient struct {
	apiKey string
	http   *http.Client
}

func newCapSolverFromEnv() *capSolverClient {
	return &capSolverClient{
		apiKey: strings.TrimSpace(os.Getenv("CAPSOLVER_API_KEY")),
		http:   &http.Client{Timeout: 20 * time.Second},
	}
}

func (c *capSolverClient) Configured() bool { return c.apiKey != "" }

type capSolverTaskResponse struct {
	ErrorId          int32          `json:"errorId"`
	ErrorCode        string         `json:"errorCode"`
	ErrorDescription string         `json:"errorDescription"`
	TaskId           string         `json:"taskId"`
	Status           string         `json:"status"`
	Solution         map[string]any `json:"solution"`
}

func (c *capSolverClient) request(ctx context.Context, path string, payload any) (*capSolverTaskResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.capsolver.com"+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	out := &capSolverTaskResponse{}
	if err := json.Unmarshal(raw, out); err != nil {
		return nil, fmt.Errorf("capsolver: bad response: %s", string(raw))
	}
	return out, nil
}

// Solve creates an AntiTurnstileTaskProxyLess task and polls until it's
// ready, capped at capSolverMaxWait.
func (c *capSolverClient) Solve(ctx context.Context, siteKey, pageURL string) (string, error) {
	if !c.Configured() {
		return "", fmt.Errorf("CAPSOLVER_API_KEY not set")
	}
	created, err := c.request(ctx, "/createTask", map[string]any{
		"clientKey": c.apiKey,
		"task": map[string]any{
			"type":       "AntiTurnstileTaskProxyLess",
			"websiteURL": pageURL,
			"websiteKey": siteKey,
		},
	})
	if err != nil {
		return "", fmt.Errorf("capsolver createTask: %w", err)
	}
	if created.ErrorId != 0 {
		return "", fmt.Errorf("capsolver createTask failed: %s", firstNonEmptyStr(created.ErrorDescription, created.ErrorCode))
	}
	if created.TaskId == "" {
		return "", fmt.Errorf("capsolver createTask returned no taskId")
	}

	ctx, cancel := context.WithTimeout(ctx, capSolverMaxWait)
	defer cancel()
	ticker := time.NewTicker(capSolverPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("capsolver: timed out waiting for solve")
		case <-ticker.C:
		}
		res, err := c.request(ctx, "/getTaskResult", map[string]any{
			"clientKey": c.apiKey,
			"taskId":    created.TaskId,
		})
		if err != nil {
			return "", fmt.Errorf("capsolver getTaskResult: %w", err)
		}
		if res.ErrorId != 0 {
			return "", fmt.Errorf("capsolver solve failed: %s", firstNonEmptyStr(res.ErrorDescription, res.ErrorCode))
		}
		switch res.Status {
		case "ready":
			token, _ := res.Solution["token"].(string)
			if token == "" {
				return "", fmt.Errorf("capsolver: ready result had no token")
			}
			return token, nil
		case "failed":
			return "", fmt.Errorf("capsolver: solve failed")
		}
		// "idle"/"processing": keep polling.
	}
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return "unknown error"
}
