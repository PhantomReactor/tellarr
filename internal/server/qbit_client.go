package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// QBitRealClient talks to a genuine qBittorrent WebUI instance. Genuine
// .torrent files found in Telegram channels are forwarded there.
type QBitRealClient struct {
	baseURL string
	user    string
	pass    string

	mu   sync.Mutex
	http *http.Client
}

func NewQBitRealClientFromEnv() *QBitRealClient {
	jar, _ := cookiejar.New(nil)
	return &QBitRealClient{
		baseURL: strings.TrimRight(os.Getenv("QBIT_REAL_URL"), "/"),
		user:    os.Getenv("QBIT_REAL_USER"),
		pass:    os.Getenv("QBIT_REAL_PASS"),
		http: &http.Client{
			Timeout: 30 * time.Second,
			Jar:     jar,
		},
	}
}

func (q *QBitRealClient) Configured() bool {
	return q.baseURL != ""
}

func (q *QBitRealClient) login() error {
	form := url.Values{"username": {q.user}, "password": {q.pass}}
	resp, err := q.http.PostForm(q.baseURL+"/api/v2/auth/login", form)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "Ok." {
		return fmt.Errorf("login failed (%s): %q at %s", resp.Status, string(body), q.baseURL+"/api/v2/auth/login")
	}
	return nil
}

func (q *QBitRealClient) ensureLogin() error {
	resp, err := q.http.Get(q.baseURL + "/api/v2/app/version")
	if err == nil && resp.StatusCode == http.StatusOK {
		resp.Body.Close()
		return nil
	}
	if resp != nil {
		resp.Body.Close()
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.login()
}

func (q *QBitRealClient) Version() (string, error) {
	if !q.Configured() {
		return "", fmt.Errorf("real qbittorrent not configured")
	}
	if err := q.ensureLogin(); err != nil {
		return "", err
	}
	resp, err := q.http.Get(q.baseURL + "/api/v2/app/version")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), nil
}

// AddTorrentBytes forwards a real .torrent file to the remote client.
func (q *QBitRealClient) AddTorrentBytes(torrent []byte, category, savePath string) error {
	if !q.Configured() {
		return fmt.Errorf("real qbittorrent not configured")
	}
	if err := q.ensureLogin(); err != nil {
		return err
	}

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	fw, err := writer.CreateFormFile("torrents", "torrent.torrent")
	if err != nil {
		return err
	}
	if _, err := fw.Write(torrent); err != nil {
		return err
	}
	if category != "" {
		_ = writer.WriteField("category", category)
	}
	if savePath != "" {
		_ = writer.WriteField("savepath", savePath)
	}
	if err := writer.Close(); err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, q.baseURL+"/api/v2/torrents/add", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := q.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("remote add failed (%s): %s", resp.Status, string(b))
	}
	return nil
}

type RemoteTorrent struct {
	Hash        string  `json:"hash"`
	Name        string  `json:"name"`
	Size        int64   `json:"size"`
	Progress    float64 `json:"progress"`
	State       string  `json:"state"`
	Category    string  `json:"category"`
	SavePath    string  `json:"save_path"`
	ContentPath string  `json:"content_path"`
	DlSpeed     int64   `json:"dlspeed"`
	Eta         int64   `json:"eta"`
}

func (q *QBitRealClient) TorrentsInfo() ([]RemoteTorrent, error) {
	if !q.Configured() {
		return nil, nil
	}
	if err := q.ensureLogin(); err != nil {
		return nil, err
	}
	resp, err := q.http.Get(q.baseURL + "/api/v2/torrents/info")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out []RemoteTorrent
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func (q *QBitRealClient) action(hash, endpoint string, extra url.Values) error {
	if !q.Configured() {
		return fmt.Errorf("real qbittorrent not configured")
	}
	if err := q.ensureLogin(); err != nil {
		return err
	}
	form := url.Values{"hashes": {hash}}
	for k, v := range extra {
		form[k] = v
	}
	resp, err := q.http.PostForm(q.baseURL+endpoint, form)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s failed: %s", endpoint, resp.Status)
	}
	return nil
}

func (q *QBitRealClient) Pause(hashes []string) error {
	return q.action(strings.Join(hashes, "|"), "/api/v2/torrents/pause", nil)
}

func (q *QBitRealClient) Resume(hashes []string) error {
	return q.action(strings.Join(hashes, "|"), "/api/v2/torrents/resume", nil)
}

func (q *QBitRealClient) Delete(hashes []string, deleteFiles bool) error {
	extra := url.Values{"deleteFiles": {fmt.Sprintf("%t", deleteFiles)}}
	return q.action(strings.Join(hashes, "|"), "/api/v2/torrents/delete", extra)
}
