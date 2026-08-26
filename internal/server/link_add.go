// Universal "add download by any link" plumbing: one dispatcher shared by
// the web UI form, the legacy JSON API and the emulated qBittorrent API.
// Supported inputs: t.me/tg:// message links (private and public), magnet:
// URIs, .torrent file URLs, hubcloud/gdflix/tmbcloud provider pages,
// telegra.ph/graph.org post pages, arbitrary HTML pages carrying download
// links, and direct file URLs.
package server

import (
	"context"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	dbm "tellarr/internal/database/models"
	"tellarr/internal/linkresolver"
)

// tgRef is a parsed Telegram message reference.
type tgRef struct {
	ChannelId int64 // numeric channel id from private /c/ links
	MessageId int64
	Username  string // public links: resolved via ContactsResolveUsername
}

// parseTgLink recognizes every common Telegram message-link shape:
//
//	https://t.me/c/1234567/89            private channel
//	https://t.me/c/1234567/89?single     private channel with query
//	https://t.me/channelname/89          public channel/supergroup
//	https://t.me/s/channelname/89        public preview links
//	tg://resolve?domain=name&post=89     deep link
func parseTgLink(link string) (tgRef, bool) {
	raw := strings.TrimSpace(link)
	if raw == "" {
		return tgRef{}, false
	}
	lower := strings.ToLower(raw)

	if strings.HasPrefix(lower, "tg://") {
		u, err := url.Parse(raw)
		if err != nil {
			return tgRef{}, false
		}
		if !strings.HasSuffix(strings.TrimSuffix(u.Host, u.Path), "resolve") && u.Path != "/resolve" {
			return tgRef{}, false
		}
		q := u.Query()
		domain := strings.TrimSpace(q.Get("domain"))
		post, _ := strconv.ParseInt(q.Get("post"), 10, 64)
		if domain == "" || post <= 0 {
			return tgRef{}, false
		}
		return tgRef{Username: domain, MessageId: post}, true
	}

	if !strings.Contains(lower, "t.me/") && !strings.Contains(lower, "telegram.me/") {
		return tgRef{}, false
	}
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return tgRef{}, false
	}
	host := strings.ToLower(u.Host)
	host = strings.TrimPrefix(host, "www.")
	if host != "t.me" && host != "telegram.me" &&
		!strings.HasSuffix(host, ".t.me") && !strings.HasSuffix(host, ".telegram.me") {
		return tgRef{}, false
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")

	switch {
	case len(segs) >= 3 && segs[0] == "s":
		msgId, _ := strconv.ParseInt(segs[2], 10, 64)
		if msgId <= 0 || !validTgUsername(segs[1]) {
			return tgRef{}, false
		}
		return tgRef{Username: segs[1], MessageId: msgId}, true

	case len(segs) >= 3 && segs[0] == "c":
		channelId, err1 := strconv.ParseInt(segs[1], 10, 64)
		msgId, err2 := strconv.ParseInt(segs[2], 10, 64)
		if err1 != nil || err2 != nil || channelId <= 0 || msgId <= 0 {
			return tgRef{}, false
		}
		return tgRef{ChannelId: channelId, MessageId: msgId}, true

	case len(segs) >= 2 && segs[0] != "c" && segs[0] != "s":
		msgId, err := strconv.ParseInt(segs[1], 10, 64)
		if err != nil || msgId <= 0 || !validTgUsername(segs[0]) {
			return tgRef{}, false
		}
		return tgRef{Username: segs[0], MessageId: msgId}, true
	}
	return tgRef{}, false
}

// validTgUsername filters invite/join shapes that carry no message context.
func validTgUsername(s string) bool {
	if s == "" || strings.HasPrefix(s, "+") {
		return false
	}
	switch strings.ToLower(s) {
	case "joinchat", "share", "addlist", "addstickers", "proxy", "socks":
		return false
	}
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_'
		if !ok {
			return false
		}
	}
	return true
}

// addAnyLink routes any supported link onto the right download engine and
// returns the created row id plus display filename.
func (s *Server) addAnyLink(ctx context.Context, raw, nameOverride, category, savePath string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("empty link")
	}

	// Our own torznab-generated refs keep their Telegram-backed semantics.
	if d, m, isTor, ok := s.isOwnRef(raw); ok {
		return s.addTellarrRef(ctx, raw, d, m, isTor, category, savePath)
	}
	if ref, ok := parseTgLink(raw); ok {
		return s.addTelegramDownload(ctx, ref, nameOverride, category, savePath)
	}
	if strings.HasPrefix(strings.ToLower(raw), "magnet:") {
		return s.addMagnetLink(ctx, raw, nameOverride, category, savePath)
	}
	if looksLikeTorrentURL(raw) {
		return s.addTorrentLink(ctx, raw, nameOverride, category, savePath)
	}
	if linkresolver.Name(raw) != "" || linkresolver.LooksLikeFileURL(raw) {
		return s.startExternalDownload(ctx, 0, 0, 0, nil, raw, category, savePath, nameOverride)
	}
	// Unknown web page: scrape it for downloadable targets before giving up.
	return s.addScrapedPage(ctx, raw, nameOverride, category, savePath)
}

func looksLikeTorrentURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.HasSuffix(strings.ToLower(u.Path), ".torrent")
}

// addTelegramDownload starts a transfer for media inside a Telegram message,
// resolving public usernames through any live session first.
func (s *Server) addTelegramDownload(ctx context.Context, ref tgRef, filename, category, savePath string) (string, string, error) {
	var (
		dialog *dbm.Dialog
		err    error
	)
	if ref.Username != "" {
		dialog, err = s.resolveUsernameChannel(ref.Username)
		if err != nil {
			return "", "", err
		}
	} else {
		dialog, err = s.dialogRepo.GetDialogsByDialogId(ref.ChannelId)
		if err != nil || dialog == nil {
			return "", "", fmt.Errorf("channel %d is not indexed", ref.ChannelId)
		}
	}
	channelId := dialog.DialogId

	t, err := s.getTelegramClient(dialog.SessionId)
	if err != nil {
		return "", "", fmt.Errorf("telegram session unavailable")
	}
	doc, err := s.fetchDocument(t, channelId, ref.MessageId)
	if err != nil {
		return "", "", fmt.Errorf("media not found in message %d/%d", channelId, ref.MessageId)
	}
	if strings.TrimSpace(filename) == "" {
		filename = documentFilename(doc, fmt.Sprintf("%d_%d", channelId, ref.MessageId))
	}
	api, err := t.downloadAPI(t.context, doc.DCID)
	if err != nil {
		return "", "", fmt.Errorf("download pool unavailable")
	}
	row, err := s.dm.Start(t.context, api, doc, dialog.SessionId, channelId, ref.MessageId, filename, category, savePath)
	if err != nil {
		return "", "", err
	}
	return row.ID, row.Filename, nil
}

// resolveUsernameChannel maps a public @username onto an indexed dialog by
// asking Telegram for the numeric channel id via any active session.
func (s *Server) resolveUsernameChannel(username string) (*dbm.Dialog, error) {
	for _, sessionId := range s.activeSessionIds() {
		t, err := s.getTelegramClient(sessionId)
		if err != nil {
			continue
		}
		res, err := t.client.API().ContactsResolveUsername(t.context, &tg.ContactsResolveUsernameRequest{Username: username})
		if err != nil {
			slog.Debug("username resolve failed", "username", username, "err", err)
			continue
		}
		pc, ok := res.Peer.(*tg.PeerChannel)
		if !ok {
			continue
		}
		dialog, derr := s.dialogRepo.GetDialogsByDialogId(pc.ChannelID)
		if derr == nil && dialog != nil {
			return dialog, nil
		}
	}
	return nil, fmt.Errorf("channel @%s is not indexed (join it with a logged-in session and refresh channels)", username)
}

func (s *Server) activeSessionIds() []int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]int64, 0, len(s.telegramSessions))
	for id := range s.telegramSessions {
		out = append(out, id)
	}
	return out
}

var base32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

// magnetInfoHash extracts the v1 infohash (hex) from a magnet URI, decoding
// base32-encoded btih values; "" when absent or malformed.
func magnetInfoHash(mag string) string {
	u, err := url.Parse(mag)
	if err != nil {
		return ""
	}
	for _, xt := range u.Query()["xt"] {
		xt = strings.ToLower(strings.TrimSpace(xt))
		const p = "urn:btih:"
		if !strings.HasPrefix(xt, p) {
			continue
		}
		h := strings.TrimSpace(xt[len(p):])
		switch len(h) {
		case 40:
			if _, err := hex.DecodeString(h); err == nil {
				return h
			}
		case 32:
			if b, err := base32NoPad.DecodeString(strings.ToUpper(h)); err == nil && len(b) == 20 {
				return hex.EncodeToString(b)
			}
		}
	}
	return ""
}

// magnetDisplayName pulls the dn= parameter as a human-readable name.
func magnetDisplayName(mag string) string {
	u, err := url.Parse(mag)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(u.Query().Get("dn"))
}

func remoteRowID(hash string, dialogId, messageId int64, filename string) string {
	if hash != "" {
		return hash
	}
	return SyntheticHash(dialogId, messageId, filename)
}

// addMagnetLink hands a real magnet URI to a BitTorrent backend: the genuine
// qBittorrent instance when configured, else aria2 (which ingests magnets
// natively). Rows are keyed by infohash so live status matches back.
func (s *Server) addMagnetLink(ctx context.Context, mag, nameOverride, category, savePath string) (string, string, error) {
	hash := magnetInfoHash(mag)
	name := firstNonEmpty(nameOverride, magnetDisplayName(mag))
	if name == "" {
		if hash != "" {
			name = hash
		} else {
			name = "magnet-download"
		}
	}

	if qb := NewQBitRealClientFromEnv(); qb.Configured() {
		if err := qb.AddMagnet(mag, category, savePath); err != nil {
			return "", "", fmt.Errorf("qbittorrent add failed: %w", err)
		}
		id := remoteRowID(hash, 0, 0, name)
		if err := s.recordRemoteDownload(0, 0, name, category, savePath, 0, hash); err != nil {
			return "", "", err
		}
		return id, name, nil
	}

	aria := NewAria2ClientFromEnv()
	if !aria.Configured() {
		return "", "", fmt.Errorf("magnet needs a BitTorrent backend: configure QBIT_REAL_* or ARIA2_RPC_URL")
	}
	id := remoteRowID(hash, 0, 0, name)
	if hash == "" {
		id = externalHash(mag)
	}
	if existing, err := s.downloadRepo.Get(id); err == nil && existing != nil && existing.State == dbm.StateDownloading {
		return id, existing.Filename, nil
	}
	if strings.TrimSpace(savePath) == "" {
		savePath = s.dm.baseDir
	}
	now := time.Now().UTC()
	err := s.downloadRepo.Create(dbm.TorrentDownload{
		ID:        id,
		Filename:  name,
		State:     dbm.StateDownloading,
		Origin:    dbm.OriginAria2,
		Category:  category,
		SavePath:  savePath,
		SourceURL: mag,
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		return "", "", err
	}
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		gid, gerr := aria.AddURI(bg, mag, Aria2Options{Dir: savePath})
		if gerr != nil {
			slog.Error("aria2 magnet add failed", "err", gerr)
			_ = s.downloadRepo.UpdateProgress(id, 0, dbm.StateError, gerr.Error())
			return
		}
		if err := s.downloadRepo.SetRemoteGid(id, gid); err != nil {
			slog.Error("failed to persist aria2 gid", "id", id, "err", err)
		}
	}()
	return id, name, nil
}

var torrentFetchClient = &http.Client{Timeout: 60 * time.Second}

// addTorrentLink fetches remote .torrent bytes and forwards them to the
// real qBittorrent instance.
func (s *Server) addTorrentLink(ctx context.Context, raw, nameOverride, category, savePath string) (string, string, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, raw, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", linkresolver.UserAgent)
	resp, err := torrentFetchClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("could not fetch torrent: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("torrent fetch failed (%s)", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", "", fmt.Errorf("could not read torrent: %w", err)
	}
	hash := torrentInfoHash(data)
	if hash == "" {
		return "", "", fmt.Errorf("url did not return a valid .torrent file")
	}
	if _, err := s.forwardTorrentBytes(data, category, savePath); err != nil {
		return "", "", err
	}
	name := firstNonEmpty(nameOverride, strings.TrimSuffix(linkresolver.FileNameFromURL(raw), ".torrent"))
	if name == "" {
		name = hash
	}
	if err := s.recordRemoteDownload(0, 0, name, category, savePath, 0, hash); err != nil {
		return "", "", err
	}
	return remoteRowID(hash, 0, 0, name), name, nil
}

// addScrapedPage handles telegra.ph/graph.org posts and unknown web pages:
// extract downloadable targets and route the best one onward.
func (s *Server) addScrapedPage(ctx context.Context, pageURL, nameOverride, category, savePath string) (string, string, error) {
	scrapeCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	targets, err := linkresolver.ScrapeTargets(scrapeCtx, pageURL)
	cancel()
	if err != nil {
		return "", "", fmt.Errorf("could not load page: %w", err)
	}
	pick := linkresolver.PickTarget(targets)
	if pick == nil {
		return "", "", fmt.Errorf("no downloadable links found on page")
	}
	slog.Info("scraped page target picked", "page", pageURL, "kind", pick.Kind, "url", pick.URL)
	switch pick.Kind {
	case linkresolver.TargetProvider, linkresolver.TargetFile:
		return s.startExternalDownload(ctx, 0, 0, 0, nil, pick.URL, category, savePath, nameOverride)
	case linkresolver.TargetTorrent:
		return s.addTorrentLink(ctx, pick.URL, nameOverride, category, savePath)
	case linkresolver.TargetMagnet:
		return s.addMagnetLink(ctx, pick.URL, nameOverride, category, savePath)
	}
	return "", "", fmt.Errorf("no downloadable links found on page")
}
