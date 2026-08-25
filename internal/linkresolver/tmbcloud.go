package linkresolver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// resolveTMBCloud walks TMBCloud's flow:
//
//	/download/<code> -> 302 -> /links/<code> (renders a Cloudflare Turnstile
//	  widget; a bogus token is rejected server-side, confirmed live, so this
//	  cannot be scraped like HubCloud/GDFlix without a real solve)
//	    -> POST /step2.php {token_a, page_id} -> {token_b}
//	    -> solve the Turnstile challenge for the page's sitekey
//	    -> POST /gen-link.php {type, page_id, token_b, cf_turnstile_response}
//	      -> {url: "/cloudfiles/<token>"} (relative; confirmed live gen-link.php
//	         also 403s without Origin/Referer headers even with a valid token)
//	    -> GET that page: a static interstitial (no further captcha) whose
//	      real R2/CDN link lives in a copy-button's data-url attribute, not
//	      an <a href>, so it needs the same candidate scoring as HubCloud's
//	      download-page hop rather than trusting gen-link.php's url verbatim.
//
// newTurnstileSolver is a package-level hook so tests can substitute a fake
// solver instead of calling out to a real (paid) solving API.
var newTurnstileSolver = func() turnstileSolver { return newCapSolverFromEnv() }

// defaultTMBCloudLinkType picks which download button TMBCloud's gen-link.php
// should generate for. Overridable via TMBCLOUD_LINK_TYPE (r2, fslv2new).
// Defaults to fastcdnv2: confirmed live (Aug 2026) to be the only mirror
// that yields a working direct link — the r2 mirror's public bucket 404s
// server-side ("Object not found") even though the site still offers it.
func tmbcloudLinkType() string {
	if v := strings.TrimSpace(os.Getenv("TMBCLOUD_LINK_TYPE")); v != "" {
		return v
	}
	return "fastcdnv2"
}

var (
	pageIDRe     = regexp.MustCompile(`(?i)const\s+PAGE_ID\s*=\s*'([^']+)'`)
	tokenARe     = regexp.MustCompile(`(?i)const\s+TOKEN_A\s*=\s*'([^']+)'`)
	siteKeyRe    = regexp.MustCompile(`(?i)sitekey\s*:\s*'([^']+)'`)
	h2TitleRe    = regexp.MustCompile(`(?is)<h2[^>]*>(.*?)</h2>`)
	sizePillRe   = regexp.MustCompile(`(?is)Size:\s*<span>([^<]+)</span>`)
	cfCodeRe     = regexp.MustCompile(`(?i)(?:var|let|const)\s+CODE\s*=\s*'([^']+)'`)
	cdFilenameRe = regexp.MustCompile(`(?i)filename\s*=\s*"?([^";]+)"?`)
)

// tmbPollAttempts/tmbPollInterval bound the cloudfilesv2 readiness poll.
// The site's own JS allows up to 600 tries (~30 min); in practice the first
// poll is already ready. We cap far below that so a wedged job can't hang
// a download goroutine indefinitely.
const (
	tmbPollAttempts = 40
	tmbPollInterval = 3 * time.Second
)

func resolveTMBCloud(ctx context.Context, client *http.Client, rawURL string) (*Result, error) {
	page, err := fetchPage(ctx, client, rawURL)
	if err != nil {
		return nil, err
	}
	if err := deadFileErr(page); err != nil {
		return nil, err
	}

	pageID := firstMatch(pageIDRe, page.Body)
	tokenA := firstMatch(tokenARe, page.Body)
	siteKey := firstMatch(siteKeyRe, page.Body)
	if pageID == "" || tokenA == "" || siteKey == "" {
		return nil, fmt.Errorf("could not find page_id/token_a/sitekey (layout may have changed)")
	}

	base := schemeHost(page.FinalURL)
	tokenB, err := tmbcloudStep2(ctx, client, base, page.FinalURL, tokenA, pageID)
	if err != nil {
		return nil, err
	}

	solver := newTurnstileSolver()
	cfToken, err := solver.Solve(ctx, siteKey, page.FinalURL)
	if err != nil {
		return nil, fmt.Errorf("solving Cloudflare Turnstile: %w", err)
	}

	genURL, err := tmbcloudGenLink(ctx, client, base, page.FinalURL, pageID, tokenB, cfToken)
	if err != nil {
		return nil, err
	}
	genURL = absURL(page.FinalURL, genURL)
	if genURL == "" {
		return nil, fmt.Errorf("gen-link.php returned an unusable url")
	}

	linkURL := genURL
	referer := page.FinalURL
	// gen-link.php's url is usually an interstitial page (e.g.
	// /cloudfiles/<token> or /cloudfilesv2/<id>) rather than the file
	// itself; only trust it directly when it already looks like a real
	// file link.
	if !hasExt(strings.ToLower(genURL), directNameExts) {
		hop, err := fetchPage(ctx, client, genURL)
		if err != nil {
			return nil, fmt.Errorf("fetching generated link page: %w", err)
		}
		referer = hop.FinalURL

		if code := firstMatch(cfCodeRe, hop.Body); code != "" {
			// FastCDN V2 layout: the page polls a worker until the link is
			// prepared, then fetches {downloadUrl} from a JSON endpoint.
			linkURL, err = tmbCloudfilesv2(ctx, client, schemeHost(hop.FinalURL), referer, code)
			if err != nil {
				return nil, err
			}
		} else {
			// r2-style layout: static page whose real link hides in a copy
			// button's data-url attribute rather than an <a href>.
			cands := extractCandidates(hop.FinalURL, hop.Body)
			best := pickBest(cands)
			if best == nil {
				return nil, fmt.Errorf("no direct link found on generated link page")
			}
			linkURL = best.url
		}
	}

	res := &Result{
		URL:  linkURL,
		Size: ParseSize(firstMatch(sizePillRe, page.Body)),
		Headers: map[string]string{
			"Referer": referer,
		},
	}
	// TMBCloud's <h2>/<title> text is lightly obfuscated with random extra
	// dots on every page load (confirmed live, e.g. "Govern.or.2026...."),
	// presumably to defeat naive scraping/caching. Prefer names that come
	// from the file itself: the URL path first, then Content-Disposition on
	// the final URL (FastCDN V2's googleusercontent links carry the clean
	// name there), and only fall back to the (possibly dotted) title last.
	if name := filenameFromURL(linkURL); name != "" && hasExt(strings.ToLower(name), directNameExts) {
		res.Filename = sanitizeFilename(name)
	}
	if res.Filename == "" {
		if name := filenameFromContentDisposition(ctx, client, linkURL); name != "" {
			res.Filename = sanitizeFilename(name)
		}
	}
	if res.Filename == "" {
		filenameForResult(res, page.Body)
	}
	return res, nil
}

func firstMatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

type tmbcloudStep2Response struct {
	TokenB string `json:"token_b"`
	Error  string `json:"error"`
}

func tmbcloudStep2(ctx context.Context, client *http.Client, base, referer, tokenA, pageID string) (string, error) {
	form := url.Values{"token_a": {tokenA}, "page_id": {pageID}}
	body, err := postForm(ctx, client, base+"/step2.php", referer, form)
	if err != nil {
		return "", fmt.Errorf("step2.php: %w", err)
	}
	var out tmbcloudStep2Response
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("step2.php: bad response: %s", string(body))
	}
	if out.TokenB == "" {
		return "", fmt.Errorf("step2.php: %s", firstNonEmptyStr(out.Error, "no token_b returned"))
	}
	return out.TokenB, nil
}

type tmbcloudGenLinkResponse struct {
	URL   string `json:"url"`
	Error string `json:"error"`
}

// tmbCloudfilesv2 finishes the FastCDN V2 flow on the /cloudfilesv2/<id>
// page: poll cloudfilesv2-worker.php until {ready:true}, then ask
// /cloudfilesv2/<CODE>?dl=1&json=1 for {downloadUrl}.
func tmbCloudfilesv2(ctx context.Context, client *http.Client, base, referer, code string) (string, error) {
	pollURL := base + "/cloudfilesv2-worker.php?c=" + url.QueryEscape(code) + "&poll=1"
	for i := 0; i < tmbPollAttempts; i++ {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("cloudfilesv2: %w", err)
		}
		body, status, err := getJSON(ctx, client, pollURL, referer)
		if err != nil {
			return "", fmt.Errorf("cloudfilesv2 poll: %w", err)
		}
		var out struct {
			Ready bool   `json:"ready"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return "", fmt.Errorf("cloudfilesv2 poll: bad response (http %d): %s", status, string(body))
		}
		if out.Error != "" {
			return "", fmt.Errorf("cloudfilesv2 poll: %s", out.Error)
		}
		if out.Ready {
			break
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("cloudfilesv2: %w", ctx.Err())
		case <-time.After(tmbPollInterval):
		}
	}

	dlURL := base + "/cloudfilesv2/" + url.QueryEscape(code) + "?dl=1&json=1"
	body, status, err := getJSON(ctx, client, dlURL, referer)
	if err != nil {
		return "", fmt.Errorf("cloudfilesv2 dl-json: %w", err)
	}
	var out struct {
		Success     bool   `json:"success"`
		DownloadURL string `json:"downloadUrl"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("cloudfilesv2 dl-json: bad response (http %d): %s", status, string(body))
	}
	if out.DownloadURL == "" {
		return "", fmt.Errorf("cloudfilesv2 dl-json: %s", firstNonEmptyStr(out.Error, "no downloadUrl returned"))
	}
	return out.DownloadURL, nil
}

// filenameFromContentDisposition issues a HEAD for the final URL's
// Content-Disposition filename. Only worth a round trip when the URL path
// itself doesn't name the file (e.g. signed googleusercontent links).
func filenameFromContentDisposition(ctx context.Context, client *http.Client, target string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, target, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return ""
	}
	cd := resp.Header.Get("Content-Disposition")
	if cd == "" {
		return ""
	}
	name := strings.TrimSpace(firstMatch(cdFilenameRe, cd))
	if name == "" || !hasExt(strings.ToLower(name), directNameExts) {
		return ""
	}
	return name
}

// getJSON fetches a URL expected to answer with JSON.
func getJSON(ctx context.Context, client *http.Client, target, referer string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return body, resp.StatusCode, err
}

func tmbcloudGenLink(ctx context.Context, client *http.Client, base, referer, pageID, tokenB, cfToken string) (string, error) {
	form := url.Values{
		"type":                  {tmbcloudLinkType()},
		"page_id":               {pageID},
		"token_b":               {tokenB},
		"cf_turnstile_response": {cfToken},
	}
	body, err := postForm(ctx, client, base+"/gen-link.php", referer, form)
	if err != nil {
		return "", fmt.Errorf("gen-link.php: %w", err)
	}
	var out tmbcloudGenLinkResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("gen-link.php: bad response: %s", string(body))
	}
	if out.URL == "" {
		return "", fmt.Errorf("gen-link.php: %s", firstNonEmptyStr(out.Error, "no url returned"))
	}
	return out.URL, nil
}

// postForm is a small helper distinct from submitForm (http_util.go): that
// one drives HTML <form> scraping hand-offs, this one talks to TMBCloud's
// JSON XHR endpoints directly. Origin/Referer are required: gen-link.php was
// confirmed live to 403 "Access denied" on an otherwise-valid request when
// they're missing, matching the fetch() calls the real page's JS makes.
func postForm(ctx context.Context, client *http.Client, target, referer string, form url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if referer != "" {
		req.Header.Set("Referer", referer)
		req.Header.Set("Origin", schemeHost(referer))
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}
