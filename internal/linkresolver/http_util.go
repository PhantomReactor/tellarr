package linkresolver

import (
	"context"
	"fmt"
	"io"
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
