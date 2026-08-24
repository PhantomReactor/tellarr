package server

import (
	"encoding/xml"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
	dbm "tellarr/internal/database/models"
)

func newDownloader() *downloader.Downloader {
	return downloader.NewDownloader()
}

const newznabNS = "http://www.newznab.com/DTD/2010/feeds/attributes/"

type torznabAttr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type torznabEnclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type torznabItem struct {
	Title     string           `xml:"title"`
	GUID      string           `xml:"guid"`
	PubDate   string           `xml:"pubDate"`
	Size      int64            `xml:"size"`
	Enclosure torznabEnclosure `xml:"enclosure"`
	Attrs     []torznabAttr    `xml:"newznab:attr"`
}

type torznabChannel struct {
	Title       string        `xml:"title"`
	Description string        `xml:"description"`
	Link        string        `xml:"link"`
	Items       []torznabItem `xml:"item"`
}

type torznabRSS struct {
	XMLName xml.Name       `xml:"rss"`
	Version string         `xml:"version,attr"`
	Xmlns   string         `xml:"xmlns:newznab,attr"`
	Channel torznabChannel `xml:"channel"`
}

type torznabError struct {
	XMLName     xml.Name `xml:"error"`
	Code        int      `xml:"code,attr"`
	Description string   `xml:"description,attr"`
}

func (s *Server) RegisterTorznabRoutes(r chi.Router) {
	r.Get("/torznab/{channel}/api", s.HandleTorznab)
	r.Get("/d/{channel}/{messageId}", s.HandleTorrentLink)
	r.Head("/d/{channel}/{messageId}", s.HandleTorrentLink)
}

// validateApiKey checks the apikey query parameter against stored API tokens.
func (s *Server) validateApiKey(r *http.Request) bool {
	apikey := r.URL.Query().Get("apikey")
	if apikey == "" {
		return false
	}
	tokens, err := s.apiTokens()
	if err != nil {
		return false
	}
	for _, t := range tokens {
		if t.Token == apikey {
			return true
		}
	}
	return false
}

func writeTorznabError(w http.ResponseWriter, code int, desc string) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = xml.NewEncoder(w).Encode(torznabError{Code: code, Description: desc})
}

func (s *Server) HandleTorznab(w http.ResponseWriter, r *http.Request) {
	if !s.validateApiKey(r) {
		writeTorznabError(w, 100, "Incorrect user credentials")
		return
	}
	t := r.URL.Query().Get("t")

	if t == "caps" {
		s.writeCaps(w)
		return
	}
	// Standard torznab function names: search, tvsearch, movie
	// ("movieSearch" kept for backwards compatibility).
	t = strings.ToLower(t)
	if t != "search" && t != "tvsearch" && t != "movie" && t != "moviesearch" {
		writeTorznabError(w, 202, "No such function: "+t)
		return
	}

	channelName := chi.URLParam(r, "channel")
	dialog, err := s.dialogRepo.GetDialogByName(channelName)
	if err != nil {
		writeTorznabError(w, 900, "Internal error")
		return
	}
	if dialog == nil || !dialog.Indexer {
		writeTorznabError(w, 300, "Unknown indexer channel: "+channelName)
		return
	}

	query := buildTorznabQuery(r)
	results, err := s.searchChannelDialog(dialog, query, 50)
	if err != nil {
		slog.Error("torznab search failed", "channel", channelName, "err", err)
		writeTorznabError(w, 900, "Search failed")
		return
	}

	base := s.baseURL()
	items := make([]torznabItem, 0, len(results))
	for _, res := range results {
		hash := SyntheticHash(dialog.DialogId, res.MessageId, res.Name)
		pubDate := time.Now().UTC().Format(time.RFC1123Z)
		item := torznabItem{
			Title:   res.Name,
			GUID:    hash,
			PubDate: pubDate,
			Size:    res.Size,
		}
		// Emit parent + common subcategories for BOTH movies and TV so
		// results survive category intersection filtering in
		// Sonarr/Radarr/Prowlarr regardless of query type or which
		// categories were picked in their indexer settings.
		categories := []string{"2000", "2030", "2040", "5000", "5030", "5040"}
		attrs := make([]torznabAttr, 0, 2+len(categories))
		for _, c := range categories {
			attrs = append(attrs, torznabAttr{Name: "category", Value: c})
		}
		if res.IsTorrent {
			link := fmt.Sprintf("%s/d/%d/%d", base, dialog.DialogId, res.MessageId)
			item.Enclosure = torznabEnclosure{URL: link + ".torrent", Length: res.Size, Type: "application/x-bittorrent"}
		} else {
			magnet := magnetFor(base, hash, res.Name, res.Size, dialog.DialogId, res.MessageId, res.URL)
			item.Enclosure = torznabEnclosure{URL: magnet, Length: res.Size, Type: "application/x-bittorrent"}
			attrs = append(attrs,
				torznabAttr{Name: "magnetUrl", Value: magnet},
				torznabAttr{Name: "infohash", Value: hash},
			)
		}
		item.Attrs = attrs
		items = append(items, item)
	}

	feed := torznabRSS{
		Version: "2.0",
		Xmlns:   newznabNS,
		Channel: torznabChannel{
			Title:       "Tellarr[" + channelName + "]",
			Description: "Tellarr Torznab feed for channel " + channelName,
			Link:        base,
			Items:       items,
		},
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(xml.Header))
	_ = xml.NewEncoder(w).Encode(feed)
}

// magnetFor builds the fake magnet *Arr hands to our emulated qBittorrent.
// The original Telegram location travels in the custom x.tellarr parameter;
// for link posts x.tellurl pins the exact aggregator URL the requester picked
// (a message can carry many quality/episode links).
func magnetFor(base, hash, name string, size int64, dialogId, messageId int64, providerURL string) string {
	ref := fmt.Sprintf("%s/d/%d/%d", base, dialogId, messageId)
	var b strings.Builder
	b.WriteString("magnet:?xt=urn:btih:")
	b.WriteString(hash)
	b.WriteString("&dn=")
	b.WriteString(url.QueryEscape(name))
	if size > 0 {
		b.WriteString("&xl=")
		b.WriteString(strconv.FormatInt(size, 10))
	}
	b.WriteString("&x.tellarr=")
	b.WriteString(url.QueryEscape(ref))
	if providerURL != "" {
		b.WriteString("&x.tellurl=")
		b.WriteString(url.QueryEscape(providerURL))
	}
	b.WriteString("&tr=udp://tracker.tellarr.invalid:6969/announce")
	return b.String()
}

// parseTellarrRef extracts the /d/{dialog}/{message} reference from an x.tellarr param.
func parseTellarrRef(ref string) (dialogId, messageId int64, ok bool) {
	idx := strings.LastIndex(ref, "/d/")
	if idx < 0 {
		return 0, 0, false
	}
	parts := strings.Split(strings.TrimPrefix(ref[idx:], "/d/"), "/")
	if len(parts) < 2 {
		return 0, 0, false
	}
	dialogId, err1 := strconv.ParseInt(parts[0], 10, 64)
	messageId, err2 := strconv.ParseInt(parts[1], 10, 64)
	return dialogId, messageId, err1 == nil && err2 == nil
}

// buildTorznabQuery merges q/season/ep into a single search term.
func buildTorznabQuery(r *http.Request) string {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	season := r.URL.Query().Get("season")
	ep := r.URL.Query().Get("ep")
	if season != "" && ep != "" && !strings.Contains(q, "S") {
		sNum, _ := strconv.Atoi(season)
		eNum, _ := strconv.Atoi(ep)
		q = fmt.Sprintf("%s S%02dE%02d", q, sNum, eNum)
	}
	return q
}

func (s *Server) writeCaps(w http.ResponseWriter) {
	caps := `<?xml version="1.0" encoding="UTF-8"?>
<caps>
  <server version="1.0" title="Tellarr"/>
  <limits max="50" default="50"/>
  <registration available="no" open="no"/>
  <searching>
    <search available="yes"/>
    <tv-search available="yes" supportedParams="q,season,ep"/>
    <movie-search available="yes" supportedParams="q"/>
  </searching>
  <categories>
    <category id="5000" title="TV"><subcat id="5030" title="TV/HD"/><subcat id="5040" title="TV/SD"/></category>
    <category id="2000" title="Movies"><subcat id="2030" title="Movies/HD"/><subcat id="2040" title="Movies/SD"/></category>
  </categories>
</caps>`
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = w.Write([]byte(caps))
}

// searchChannelDialog runs a Telegram search against one channel and returns
// enriched media results.
func (s *Server) searchChannelDialog(dialog *dbm.Dialog, query string, limit int) ([]MediaInfoResult, error) {
	sessionId := dialog.SessionId
	t, err := s.getTelegramClient(sessionId)
	if err != nil {
		return nil, err
	}
	api := t.client.API()
	if limit <= 0 {
		limit = 50
	}
	messages, err := api.MessagesSearch(t.context, &tg.MessagesSearchRequest{
		Peer: &tg.InputPeerChannel{
			ChannelID:  dialog.DialogId,
			AccessHash: dialog.AccessHash,
		},
		Q:      query,
		Limit:  limit,
		Filter: &tg.InputMessagesFilterEmpty{},
	})
	if err != nil {
		return nil, err
	}
	res, ok := messages.(*tg.MessagesChannelMessages)
	if !ok {
		return nil, fmt.Errorf("unexpected search response type")
	}
	var out []MediaInfoResult
	for _, m := range res.Messages {
		msg, ok := m.(*tg.Message)
		if !ok {
			continue
		}
		var doc *tg.Document
		if md, isMMD := msg.Media.(*tg.MessageMediaDocument); msg.Media != nil && isMMD {
			doc, _ = md.Document.AsNotEmpty()
		}
		if doc == nil {
			// Link-only post: emit one result per aggregator link so the
			// requester can pick a specific quality/episode. x.tellurl in
			// each magnet pins the exact link.
			for _, ref := range providerLinksInMessage(msg) {
				out = append(out, MediaInfoResult{
					Name:      ref.Title,
					Size:      0,
					MessageId: int64(msg.ID),
					IsTorrent: false,
					URL:       ref.URL,
				})
			}
			continue
		}
		filename := documentFilename(doc, "")
		isTorrent := strings.EqualFold(doc.MimeType, "application/x-bittorrent") ||
			strings.HasSuffix(strings.ToLower(filename), ".torrent")
		isVideo := strings.HasPrefix(doc.MimeType, "video/")
		for _, attr := range doc.Attributes {
			if v, ok := attr.(*tg.DocumentAttributeVideo); ok && v != nil {
				isVideo = true
			}
		}
		if !isVideo && !isTorrent {
			continue
		}
		if filename == "" {
			filename = fmt.Sprintf("%d_%d", dialog.DialogId, msg.ID)
		}
		out = append(out, MediaInfoResult{
			Name:      filename,
			Size:      doc.Size,
			MessageId: int64(msg.ID),
			IsTorrent: isTorrent,
		})
	}
	return out, nil
}

// MediaInfoResult is the internal enriched search result.
type MediaInfoResult struct {
	Name      string
	Size      int64
	MessageId int64
	IsTorrent bool
	// URL is the exact aggregator link for link-post results ("" for media).
	URL string
}

// canAccessLinks allows apikey holders and logged-in UI users.
func (s *Server) canAccessLinks(r *http.Request) bool {
	if s.validateApiKey(r) {
		return true
	}
	c, err := r.Cookie(sessionCookie)
	return err == nil && c.Value != ""
}

// HandleTorrentLink streams a genuine .torrent file out of a Telegram message.
func (s *Server) HandleTorrentLink(w http.ResponseWriter, r *http.Request) {
	if !s.canAccessLinks(r) {
		writeTorznabError(w, 100, "Incorrect user credentials")
		return
	}
	channelId, err := strconv.ParseInt(chi.URLParam(r, "channel"), 10, 64)
	if err != nil {
		http.Error(w, "bad channel id", http.StatusBadRequest)
		return
	}
	messageId, err := strconv.ParseInt(chi.URLParam(r, "messageId"), 10, 64)
	if err != nil {
		http.Error(w, "bad message id", http.StatusBadRequest)
		return
	}
	dialog, err := s.dialogRepo.GetDialogsByDialogId(channelId)
	if err != nil || dialog == nil {
		http.Error(w, "channel not found", http.StatusNotFound)
		return
	}
	t, err := s.getTelegramClient(dialog.SessionId)
	if err != nil {
		http.Error(w, "telegram session unavailable", http.StatusServiceUnavailable)
		return
	}
	doc, err := s.fetchDocument(t, channelId, messageId)
	if err != nil {
		http.Error(w, "media not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/x-bittorrent")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"tellarr_%d_%d.torrent\"", channelId, messageId))
	w.WriteHeader(http.StatusOK)
	dl := newDownloader()
	_, err = dl.Download(t.client.API(), &tg.InputDocumentFileLocation{
		ID:            doc.ID,
		AccessHash:    doc.AccessHash,
		FileReference: doc.FileReference,
	}).Stream(r.Context(), w)
	if err != nil {
		slog.Error("torrent link stream failed", "err", err)
	}
}
