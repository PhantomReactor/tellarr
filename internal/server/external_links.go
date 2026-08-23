package server

import (
	"net/url"
	"regexp"
	"strings"
	"unicode/utf16"

	"github.com/gotd/td/tg"
	"tellarr/internal/linkresolver"
)

var anyURLRe = regexp.MustCompile(`(?i)https?://[^\s<>"'\]]+`)
var slugExtRe = regexp.MustCompile(`(?i)\.(html?|php|aspx?)$`)

// LinkRef is one supported aggregator URL inside a message together with the
// display title that identifies it (encoding/quality/episode).
type LinkRef struct {
	URL   string
	Title string
}

// genericSlugs are URL path endings that carry no identity information.
var genericSlug = map[string]bool{
	"": true, "index.php": true, "index": true, "file": true, "files": true,
	"drive": true, "download": true, "downloads": true,
	"link": true, "get": true, "d": true, "api": true,
}

// genericLabels are anchor texts that describe the action, not the content.
var genericLabel = map[string]bool{
	"download": true, "download link": true, "downloads": true,
	"drive": true, "drive download": true, "drive link": true,
	"g drive": true, "gdrive": true, "direct": true, "direct link": true,
	"link": true, "here": true, "click here": true, "get link": true,
	"get it": true, "server": true, "watch": true, "stream": true,
}

func normalizeKey(u string) string {
	return strings.ToLower(strings.TrimRight(u, "/"))
}

// providerLinksInMessage extracts every supported host-aggregator URL from a
// message's text and hyperlink entities, each titled from its surrounding
// line, the preceding header line, or its anchor label. Order is stable so
// titles (and therefore hashes) match between search time and add time.
func providerLinksInMessage(msg *tg.Message) []LinkRef {
	if msg == nil {
		return nil
	}
	var out []LinkRef
	seenURL := map[string]bool{}

	add := func(raw, title string) {
		raw = strings.TrimRight(raw, ".,);]")
		if raw == "" || seenURL[normalizeKey(raw)] {
			return
		}
		if linkresolver.Name(raw) == "" {
			return
		}
		seenURL[normalizeKey(raw)] = true
		out = append(out, LinkRef{URL: raw, Title: title})
	}

	lastHeader := ""
	for _, line := range strings.Split(msg.Message, "\n") {
		urls := anyURLRe.FindAllString(line, -1)
		lineText := cleanLineText(line)
		supportedHere := false
		for _, u := range urls {
			if linkresolver.Name(u) == "" {
				continue
			}
			supportedHere = true
			title := lineText
			if title == "" {
				title = lastHeader
			} else if lastHeader != "" && !strings.Contains(strings.ToLower(title), strings.ToLower(lastHeader)) {
				title = strings.TrimSpace(lastHeader + " " + title)
			}
			add(u, title)
		}
		if !supportedHere && lineText != "" {
			lastHeader = lineText
		}
	}

	for _, e := range msg.Entities {
		tu, ok := e.(*tg.MessageEntityTextURL)
		if !ok {
			continue
		}
		label := entityLabel(msg.Message, tu)
		idx := indexOfURL(out, tu.URL)
		switch {
		case idx >= 0 && informativeLabel(label):
			out[idx].Title = label
		case idx < 0:
			title := label
			if !informativeLabel(label) {
				title = ""
			}
			add(tu.URL, title)
		}
	}

	// Fill blanks and force unique titles: equal titles would collide into a
	// single SyntheticHash and hide one of the links.
	usedTitles := map[string]int{}
	fallbackBase := ""
	for i := range out {
		if out[i].Title == "" {
			if slug := informativeSlug(out[i].URL); slug != "" {
				out[i].Title = slug
			} else {
				if fallbackBase == "" {
					fallbackBase = linkPostTitle(msg, out[i].URL)
				}
				out[i].Title = fallbackBase
			}
		}
		key := strings.ToLower(out[i].Title)
		n := usedTitles[key]
		usedTitles[key] = n + 1
		if n > 0 {
			out[i].Title = truncateTitle(out[i].Title) + " [" + itoa(n+1) + "]"
		} else {
			out[i].Title = truncateTitle(out[i].Title)
		}
	}
	return out
}

// titleForLink returns the canonical title for one specific provider URL of a
// message. It must agree exactly with what providerLinksInMessage produced at
// search time because it feeds SyntheticHash.
func titleForLink(msg *tg.Message, providerURL string) string {
	if msg != nil {
		for _, ref := range providerLinksInMessage(msg) {
			if strings.EqualFold(ref.URL, providerURL) {
				return ref.Title
			}
		}
	}
	return linkPostTitle(msg, providerURL)
}

// providerURLsInMessage lists just the supported URLs, in stable order.
func providerURLsInMessage(msg *tg.Message) []string {
	var out []string
	for _, ref := range providerLinksInMessage(msg) {
		out = append(out, ref.URL)
	}
	return out
}

func indexOfURL(refs []LinkRef, raw string) int {
	for i := range refs {
		if normalizeKey(refs[i].URL) == normalizeKey(raw) {
			return i
		}
	}
	return -1
}

// entityLabel decodes an entity's visible text. Telegram offsets are in
// UTF-16 code units.
func entityLabel(text string, e *tg.MessageEntityTextURL) string {
	units := utf16.Encode([]rune(text))
	start, end := e.Offset, e.Offset+e.Length
	if start < 0 || end > len(units) || start >= end {
		return ""
	}
	return strings.TrimSpace(string(utf16.Decode(units[start:end])))
}

func informativeLabel(label string) bool {
	clean := strings.ToLower(strings.Trim(label, " \t:-·|"))
	if clean == "" || genericLabel[clean] || len([]rune(clean)) < 3 {
		return false
	}
	return true
}

// informativeSlug returns the URL path slug when it identifies content
// (rejects index.php-style and tiny random ids).
func informativeSlug(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	last := segs[len(segs)-1]
	slug := strings.ToLower(slugExtRe.ReplaceAllString(last, ""))
	if genericSlug[slug] || len([]rune(slug)) < 3 {
		return ""
	}
	return slugExtRe.ReplaceAllString(last, "")
}

func cleanLineText(line string) string {
	clean := anyURLRe.ReplaceAllString(line, "")
	clean = strings.TrimSpace(clean)
	clean = strings.TrimSpace(strings.Trim(clean, "|-–—•·=:>\t "))
	clean = strings.Join(strings.Fields(clean), " ")
	return clean
}

// linkPostTitle derives a deterministic display title when no per-link
// context exists (raw pasted URLs, blank messages): first text line, else the
// URL slug.
func linkPostTitle(msg *tg.Message, providerURL string) string {
	if msg != nil {
		for _, line := range strings.Split(msg.Message, "\n") {
			clean := cleanLineText(line)
			if len([]rune(clean)) < 3 {
				continue
			}
			return truncateTitle(clean)
		}
	}
	if u, err := url.Parse(providerURL); err == nil {
		segs := strings.Split(strings.Trim(u.Path, "/"), "/")
		slug := segs[len(segs)-1]
		slug = slugExtRe.ReplaceAllString(slug, "")
		if !genericSlug[strings.ToLower(slug)] {
			return truncateTitle(slug)
		}
	}
	return "external-download"
}

func truncateTitle(s string) string {
	r := []rune(s)
	if len(r) > 120 {
		return string(r[:117]) + "..."
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
