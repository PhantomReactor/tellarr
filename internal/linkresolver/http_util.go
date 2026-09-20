package linkresolver

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type page struct {
	Body     string
	FinalURL string
	Status   int
}

// deadPageMarkers are phrases host sites use when a file is gone. Matching
// them lets us fail with a clear reason instead of scraping garbage.
var deadPageMarkers = []string{
	"file you are trying to download is no longer available",
	"file not found", "404! page not found", "page not found",
	"file has been removed", "file has been deleted",
	"link has expired", "file was deleted", "no longer exists",
}

// deadFileErr reports whether the page announces the file is gone.
func deadFileErr(p *page) error {
	lower := strings.ToLower(stripTags(p.Body))
	for _, m := range deadPageMarkers {
		if strings.Contains(lower, m) {
			return fmt.Errorf("file unavailable: %s", m)
		}
	}
	return nil
}

func newFetchClient() *http.Client {
	// Some providers (TMBCloud) bind hand-off tokens to a session cookie
	// (PHPSESSID) set on the initial page load and required on later XHR
	// POSTs; without a jar those cookies never make it onto the follow-up
	// requests and the server rejects them as invalid.
	jar, _ := cookiejar.New(nil)
	return &http.Client{Timeout: 45 * time.Second, Jar: jar}
}

func fetchPage(ctx context.Context, client *http.Client, rawURL string) (*page, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return &page{Body: string(body), FinalURL: resp.Request.URL.String(), Status: resp.StatusCode}, nil
}

// probeOK reports whether a GET returns a usable HTML page.
func probeOK(ctx context.Context, client *http.Client, rawURL string) bool {
	p, err := fetchPage(ctx, client, rawURL)
	return err == nil && p.Status == http.StatusOK && len(p.Body) > 200
}

func schemeHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	scheme := u.Scheme
	if scheme == "" {
		scheme = "https"
	}
	return scheme + "://" + u.Host
}

type formInfo struct {
	Action string
	Method string
	Fields url.Values
}

var formRe = regexp.MustCompile(`(?is)<form\b([^>]*)>(.*?)</form>`)
var attrRe = regexp.MustCompile(`([a-z-]+)\s*=\s*["']([^"']*)["']`)

// findForms extracts POST forms with their hidden inputs.
func findForms(baseURL, html string) []formInfo {
	var out []formInfo
	for _, m := range formRe.FindAllStringSubmatch(html, -1) {
		attrs := map[string]string{}
		for _, a := range attrRe.FindAllStringSubmatch(m[1], -1) {
			attrs[strings.ToLower(a[1])] = a[2]
		}
		action := absURL(baseURL, attrs["action"])
		if action == "" {
			continue
		}
		method := strings.ToUpper(attrs["method"])
		if method == "" {
			method = http.MethodGet
		}
		fields := url.Values{}
		for _, in := range inputRe.FindAllStringSubmatch(m[2], -1) {
			fields.Set(in[1], in[2])
		}
		for _, in := range inputRevRe.FindAllStringSubmatch(m[2], -1) {
			if !fields.Has(in[2]) {
				fields.Set(in[2], in[1])
			}
		}
		out = append(out, formInfo{Action: action, Method: method, Fields: fields})
	}
	return out
}

// submitForm posts (or gets) a form and returns the response page.
func submitForm(ctx context.Context, client *http.Client, f formInfo, referer string) (*page, error) {
	var req *http.Request
	var err error
	if f.Method == http.MethodPost {
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, f.Action, strings.NewReader(f.Fields.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, f.Action+"?"+f.Fields.Encode(), nil)
		if err != nil {
			return nil, err
		}
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Referer", referer)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return &page{Body: string(body), FinalURL: resp.Request.URL.String(), Status: resp.StatusCode}, nil
}

// jsonURLs pulls every http(s) value out of an arbitrary JSON blob; provider
// generate-link endpoints return differently named redirect fields.
func jsonURLs(body string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range jsonStrRe.FindAllStringSubmatch(body, -1) {
		v := strings.TrimSpace(m[1])
		if strings.HasPrefix(v, "http") && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

var jsonStrRe = regexp.MustCompile(`"(?:[^"\\]|\\.)*"\s*:\s*"((?:https?://)[^"]+)"`)

// probeResult is what probeDownload learns about a candidate URL.
type probeResult struct {
	ContentType string // content type with any charset/parameter stripped
	Filename    string // filename from Content-Disposition, "" when absent
	Size        int64  // full or partial entity length, 0 when unknown
	// FinalURL is the request URL after the client followed every
	// redirect; it differs from the probed URL when the host redirects.
	FinalURL string
	// Resolved is set only when the probe had to drill through an HTML
	// interstitial: it is the real file URL the interstitial wraps. aria2
	// must receive this, not the interstitial entry point.
	Resolved string
}

// probeDownload issues a short ranged GET and reads only the response
// headers; the body is discarded immediately. Existing resolver cookies are
// deliberately not used: the request must behave like a fresh, standalone
// client (which is what aria2 will be).
func probeDownload(ctx context.Context, rawURL string) (*probeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Range", "bytes=0-1023")
	resp, err := newFetchClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	res := &probeResult{Size: resp.ContentLength, FinalURL: resp.Request.URL.String()}
	if v, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err == nil {
		res.ContentType = strings.TrimSpace(v)
	}
	if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil {
		res.Filename = strings.TrimSpace(params["filename"])
	}
	return res, nil
}

// maxProbeDepth bounds interstitial drill-down so a chain of relay pages
// cannot fan out into an oversized fetch cascade.
const maxProbeDepth = 2

// probeVerified resolves a candidate as aria2 would, following redirects
// the whole way. When the response is an HTML interstitial the probe digs
// for the real file URL buried in it — the fastdl-style relay pages embed
// the actual download target in their own ?url= query parameter — and
// probes that instead. Returns the verdict; HTML even after drill-in means
// the candidate is not directly downloadable.
func probeVerified(ctx context.Context, rawURL string, depth int) (*probeResult, error) {
	res, err := probeDownload(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	if !isHTMLContentType(res.ContentType) {
		return res, nil
	}
	if depth < maxProbeDepth {
		if u, ok := interstitialTarget(res.FinalURL); ok {
			if inner, err := probeVerified(ctx, u, depth+1); err == nil && !isHTMLContentType(inner.ContentType) {
				inner.Resolved = u
				return inner, nil
			}
		}
	}
	return res, nil
}

// interstitialTarget extracts the real download URL an HTML relay page
// wraps. The known pattern hands the target over in a url=... query
// parameter of the interstitial page URL; it appears either escaped or
// verbatim inside the hop chain.
var relayURLParamRe = regexp.MustCompile(`[?&]url=((?:https?%3A|https?://)[^&\s"'<>]+)`)

func interstitialTarget(rawURL string) (string, bool) {
	m := relayURLParamRe.FindStringSubmatch(rawURL)
	if m == nil {
		return "", false
	}
	raw := m[1]
	if u, err := url.QueryUnescape(raw); err == nil && strings.Contains(u, "://") && !strings.Contains(u, " ") {
		raw = u
	}
	if !strings.Contains(raw, "://") || hasExt(strings.ToLower(raw), pageExt) {
		return "", false
	}
	return raw, true
}

func isHTMLContentType(ct string) bool {
	return ct == "" || strings.HasPrefix(ct, "text/html") || strings.HasPrefix(ct, "application/xhtml")
}

// pickProbed returns the best candidate whose response probe shows a real
// downloadable file, plus the probe verdict. HTML interstitials, unreachable
// hosts and CDNs that serve an error page to non-browser clients are
// dropped from the candidate list; interstitials that wrap the real file
// URL get that URL surfaced through the verdict's Resolved field. A nil
// best means every candidate failed the probe (the caller decides whether
// to fall back to plain pickBest).
func pickProbed(ctx context.Context, cands []candidate) (*candidate, *probeResult) {
	working := copyCands(cands)
	for probes := 0; probes < 8; probes++ {
		best := pickBest(working)
		if best == nil {
			return nil, nil
		}
		res, err := probeVerified(ctx, best.url, 0)
		if err != nil || isHTMLContentType(res.ContentType) {
			// Unreachable or HTML relay without a usable target: drop and
			// consider the next best candidate.
			working = removeCand(working, best.url)
			continue
		}
		if res.Resolved != "" {
			best = &candidate{url: res.Resolved, label: best.label, size: best.size}
		}
		return best, res
	}
	return nil, nil
}

func removeCand(cands []candidate, url string) []candidate {
	for i := range cands {
		if cands[i].url == url {
			return append(cands[:i:i], cands[i+1:]...)
		}
	}
	return cands
}
