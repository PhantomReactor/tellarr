// Package linkresolver resolves third-party file-host aggregator pages
// (HubCloud, GDFlix) into direct downloadable URLs. These providers rotate
// domains and layouts frequently; every scraping step here is heuristic and
// may need patching when they change.
package linkresolver

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// UserAgent mimics a browser; some hosts reject default Go UA.
const UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

// Result is a resolved direct download link.
type Result struct {
	URL      string
	Filename string
	Size     int64
	Headers  map[string]string
}

type provider struct {
	name     string
	envKey   string
	defaults []string
	resolve  func(ctx context.Context, client *http.Client, rawURL string) (*Result, error)
}

func envList(key string, def []string) []string {
	out := def
	if v := os.Getenv(key); v != "" {
		for _, part := range strings.Split(v, ",") {
			part = strings.ToLower(strings.TrimSpace(part))
			if part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

// matchers are re-read on every call so runtime env changes apply. Defaults
// rely on product branding inside the domain since these services rotate TLDs
// constantly.
func (p *provider) matchers() []string {
	return envList(p.envKey, p.defaults)
}

var providers = []provider{
	{
		name:     "hubcloud",
		envKey:   "HUBCLOUD_HOSTS",
		defaults: []string{"hubcloud"},
		resolve:  resolveHubCloud,
	},
	{
		name:     "gdflix",
		envKey:   "GDFLIX_HOSTS",
		defaults: []string{"gdflix"},
		resolve:  resolveGDFlix,
	},
	{
		name:     "tmbcloud",
		envKey:   "TMBCLOUD_HOSTS",
		defaults: []string{"tmbcloud"},
		resolve:  resolveTMBCloud,
	},
}

func providerFor(rawURL string) *provider {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return nil
	}
	host := strings.ToLower(u.Host)
	for i := range providers {
		for _, m := range providers[i].matchers() {
			if strings.Contains(host, m) {
				return &providers[i]
			}
		}
	}
	return nil
}

// Name reports which provider handles the URL ("" when none).
func Name(rawURL string) string {
	if p := providerFor(rawURL); p != nil {
		return p.name
	}
	return ""
}

// Resolve turns a provider page URL into a direct download link.
func Resolve(ctx context.Context, rawURL string) (*Result, error) {
	p := providerFor(rawURL)
	if p == nil {
		return nil, fmt.Errorf("unsupported link host: %s", rawURL)
	}
	client := newFetchClient()
	res, err := p.resolve(ctx, client, rawURL)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.name, err)
	}
	if res.Headers == nil {
		res.Headers = map[string]string{}
	}
	if _, ok := res.Headers["User-Agent"]; !ok {
		res.Headers["User-Agent"] = UserAgent
	}
	return res, nil
}

var (
	anchorRe = regexp.MustCompile(`(?is)<a\b[^>]*href\s*=\s*["']([^"']+)["'][^>]*>(.*?)</a>`)
	jsURLRes = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:window\.)?location(?:\.href)?\s*=\s*["']([^"']+)["']`),
		regexp.MustCompile(`(?i)window\.open\(\s*["']([^"']+)["']`),
		regexp.MustCompile(`(?i)\burl\s*:\s*["'](https?://[^"']+)["']`),
		// data-url attributes back "copy link" buttons on several hosts
		// (e.g. TMBCloud's cloudfiles page) that never render an <a href>.
		regexp.MustCompile(`(?i)data-url\s*=\s*["'](https?://[^"']+)["']`),
	}
	titleRe    = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	tagRe      = regexp.MustCompile(`(?s)<[^>]*>`)
	sizeRe     = regexp.MustCompile(`(?i)(\d+(?:[.,]\d+)?)\s*(gb|mb|kb)`)
	inputRe    = regexp.MustCompile(`(?is)<input\b[^>]*name\s*=\s*["']([^"']+)["'][^>]*value\s*=\s*["']([^"']*)["']`)
	inputRevRe = regexp.MustCompile(`(?is)<input\b[^>]*value\s*=\s*["']([^"']*)["'][^>]*name\s*=\s*["']([^"']+)["']`)
)

type candidate struct {
	url   string
	label string
	size  int64
}

func stripTags(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

func absURL(base, ref string) string {
	ref = strings.TrimSpace(stripTags(ref))
	if ref == "" || strings.HasPrefix(ref, "#") ||
		strings.HasPrefix(ref, "javascript:") || strings.HasPrefix(ref, "data:") ||
		strings.HasPrefix(ref, "mailto:") {
		return ""
	}
	b, err := url.Parse(base)
	if err != nil {
		return ""
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	return b.ResolveReference(r).String()
}

// ParseSize extracts a byte size embedded in arbitrary text such as
// "1.4 GB", "700MB" or "2,5Gb"; returns 0 when no size is present.
func ParseSize(label string) int64 {
	m := sizeRe.FindStringSubmatch(label)
	if m == nil {
		return 0
	}
	var mult float64
	switch strings.ToUpper(m[2]) {
	case "GB":
		mult = 1 << 30
	case "MB":
		mult = 1 << 20
	case "KB":
		mult = 1 << 10
	}
	var n float64
	fmt.Sscanf(strings.ReplaceAll(m[1], ",", "."), "%f", &n)
	return int64(n * mult)
}

var (
	videoExt = []string{".mkv", ".mp4", ".avi", ".webm", ".mov", ".ts", ".m2ts", ".flv", ".wmv"}
	archExt  = []string{".zip", ".rar", ".7z"}
	pageExt  = []string{".html", ".htm", ".php", ".asp", ".aspx", ".xhtml", ".js", ".css", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico", ".woff", ".woff2"}
)

func hasExt(u string, exts []string) bool {
	p := u
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	p = strings.ToLower(p)
	for _, e := range exts {
		if strings.HasSuffix(p, e) {
			return true
		}
	}
	return false
}

// junkHosts are ads/social/redirect targets that appear on provider pages
// but are never the file.
var junkHosts = []string{
	"google.", "tinyurl.", "bit.ly", "t.me", "telegram.me",
	"a-ads.com", "winexch", "facebook.", "twitter.",
}

func hostOf(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Host)
}

func scoreCandidate(c candidate) int {
	u := strings.ToLower(c.url)
	host := hostOf(c.url)
	for _, j := range junkHosts {
		if strings.Contains(host, j) {
			return -50
		}
	}
	// Bare-origin links (nav logos, home buttons) are never files; keep
	// pathless URLs that carry a query (e.g. dl-server ?id= links).
	if parsed, err := url.Parse(c.url); err != nil {
		return -50
	} else if (parsed.Path == "" || parsed.Path == "/") && parsed.RawQuery == "" {
		return -50
	}
	score := 0
	switch {
	case hasExt(u, videoExt):
		if strings.Contains(u, "drive.usercontent.google.com") || strings.Contains(u, "googleusercontent") {
			score = 120
		} else {
			score = 100
		}
	case strings.Contains(u, "drive.usercontent.google.com"), strings.Contains(u, "export=download"):
		score = 95
	case strings.Contains(u, "x-amz-algorithm="), strings.Contains(u, "cloudflarestorage.com"),
		strings.Contains(u, "s3.amazonaws.com"):
		// Signed object-storage links: no file extension but direct files.
		score = 95
	case isDownloadHost(host):
		score = 85
	case hasExt(u, archExt):
		score = 60
	case hasExt(u, pageExt):
		score = -10
	default:
		score = 10
	}
	if strings.Contains(strings.ToLower(c.label), "download") {
		// Proportional bump: breaks ties inside a class without letting a
		// label promote an inferior host past a real file link.
		score += score / 10
	}
	return score
}

// isDownloadHost matches dedicated download-server hostnames like
// gpdl.hubcloud.cx or dl.example.com.
func isDownloadHost(host string) bool {
	for _, l := range strings.Split(host, ".") {
		if l == "dl" || strings.HasPrefix(l, "gpdl") || (len(l) > 2 && strings.HasPrefix(l, "dl")) {
			return true
		}
	}
	return false
}

func extractCandidates(baseURL, html string) []candidate {
	seen := map[string]bool{}
	var out []candidate
	add := func(raw, label string) {
		u := absURL(baseURL, raw)
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		size := ParseSize(label)
		out = append(out, candidate{url: u, label: stripTags(label), size: size})
	}
	for _, m := range anchorRe.FindAllStringSubmatch(html, -1) {
		add(m[1], m[2])
	}
	for _, re := range jsURLRes {
		for _, m := range re.FindAllStringSubmatch(html, -1) {
			add(m[1], "")
		}
	}
	return out
}

func pickBest(cands []candidate) *candidate {
	var best *candidate
	for i := range cands {
		c := &cands[i]
		if scoreCandidate(*c) < 0 {
			continue
		}
		if best == nil {
			best = c
			continue
		}
		sb, sc := scoreCandidate(*best), scoreCandidate(*c)
		if sc > sb || (sc == sb && c.size > best.size) {
			best = c
		}
	}
	return best
}

func pageTitle(html string) string {
	m := titleRe.FindStringSubmatch(html)
	if m == nil {
		return ""
	}
	t := strings.TrimSpace(stripTags(m[1]))
	// Titles come as "File.mkv | Site" or "Site | File.mkv": prefer the
	// part carrying a media extension, else the longer part. Plain hyphens
	// are common inside release names (WEB-DL, H.265) so never split on
	// them directly.
	t = strings.ReplaceAll(t, " - ", " | ")
	parts := strings.FieldsFunc(t, func(r rune) bool {
		return r == '|' || r == '–'
	})
	if len(parts) <= 1 {
		return t
	}
	fileExts := append(append([]string{}, videoExt...), archExt...)
	best := ""
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if hasExt(strings.ToLower(p), fileExts) {
			return p
		}
		if len(p) > len(best) {
			best = p
		}
	}
	return best
}

func filenameFromURL(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	segs := strings.Split(parsed.Path, "/")
	name := segs[len(segs)-1]
	if name == "" {
		return ""
	}
	return sanitizeFilename(name)
}

func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	name = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '_'
		}
		return r
	}, name)
	if len(name) > 150 {
		name = name[len(name)-150:]
	}
	return name
}

var directNameExts = append(append([]string{}, videoExt...), archExt...)

func filenameForResult(res *Result, pageHTML string) string {
	if res.Filename == "" {
		// The resolved URL often ends in the true filename; trust it when
		// it looks like a file and not an opaque token.
		if name := filenameFromURL(res.URL); name != "" && hasExt(strings.ToLower(name), directNameExts) {
			res.Filename = sanitizeFilename(name)
		}
	}
	if res.Filename == "" {
		if t := pageTitle(pageHTML); t != "" && !strings.Contains(t, "http") {
			res.Filename = sanitizeFilename(t)
		}
	}
	if res.Filename == "" {
		res.Filename = filenameFromURL(res.URL)
	}
	return res.Filename
}
