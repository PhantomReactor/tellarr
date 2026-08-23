package linkresolver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
)

// resolveHubCloud walks the typical two-hop flow:
//
//	landing page (e.g. /file/x or /drive/index.php?id=y)
//	  -> "download" anchor (/download?id=TOKEN or /download/TOKEN)
//	    -> page listing mirror links (gdrive, direct servers)
//
// Layouts change often; each step has multiple fallback patterns.
func resolveHubCloud(ctx context.Context, client *http.Client, rawURL string) (*Result, error) {
	page, err := fetchPage(ctx, client, rawURL)
	if err != nil {
		return nil, err
	}
	if err := deadFileErr(page); err != nil {
		return nil, err
	}

	dlURL := hubcloudDownloadAnchor(page.Body, page.FinalURL)
	if dlURL == "" {
		dlURL = scriptURLVar(page.Body)
	}
	if dlURL == "" {
		dlURL = hubcloudScriptToken(ctx, client, page.FinalURL, page.Body)
	}
	if dlURL == "" {
		return nil, fmt.Errorf("no download button found")
	}

	dlPage, err := fetchPage(ctx, client, dlURL)
	if err != nil {
		return nil, err
	}
	cands := extractCandidates(dlPage.FinalURL, dlPage.Body)
	best := pickBest(cands)
	if best == nil {
		slog.Debug("hubcloud: no candidates on download page", "url", dlPage.FinalURL)
		return nil, fmt.Errorf("no direct links found on download page")
	}

	res := &Result{
		URL:  best.url,
		Size: best.size,
		Headers: map[string]string{
			"Referer": dlPage.FinalURL,
		},
	}
	filenameForResult(res, page.Body)
	if res.Filename == "" && best.label != "" && !strings.Contains(best.label, "http") {
		res.Filename = sanitizeFilename(best.label)
	}
	return res, nil
}

var hubDownloadRe = regexp.MustCompile(`href\s*=\s*["']([^"']*(?:/download\?id=[^"']+|/download/[^"'?]+)[^"']*)["']`)
var dlIDAnchorRe = regexp.MustCompile(`(?is)<a\b([^>]*\bid\s*=\s*["']download["'][^>]*)>`)
var hrefAttrRe = regexp.MustCompile(`href\s*=\s*["']([^"']+)["']`)

// hubcloudDownloadAnchor finds the download hand-off link. Current layouts
// mark it with id="download" pointing at an intermediate resolver page;
// older ones use /download?id=TOKEN hrefs.
func hubcloudDownloadAnchor(html, baseURL string) string {
	for _, m := range dlIDAnchorRe.FindAllStringSubmatch(html, -1) {
		if h := hrefAttrRe.FindStringSubmatch(m[1]); h != nil {
			if u := absURL(baseURL, h[1]); u != "" {
				return u
			}
		}
	}
	for _, m := range anchorRe.FindAllStringSubmatch(html, -1) {
		href := strings.TrimSpace(m[1])
		lower := strings.ToLower(href + " " + stripTags(m[2]))
		if !strings.Contains(lower, "download") {
			continue
		}
		u := absURL(baseURL, href)
		if u != "" && (strings.Contains(u, "/download?id=") || strings.Contains(u, "/download/")) {
			return u
		}
	}
	if m := hubDownloadRe.FindStringSubmatch(html); m != nil {
		return absURL(baseURL, m[1])
	}
	return ""
}

// scriptURLVar catches pages that stash the hand-off URL in JS:
//
//	var url = 'https://resolver/...';
func scriptURLVar(html string) string {
	m := scriptURLVarRe.FindStringSubmatch(html)
	if m == nil {
		return ""
	}
	return m[1]
}

var scriptURLVarRe = regexp.MustCompile(`(?i)(?:var|let|const)\s+(?:url|link|dl)\s*=\s*['"](https?://[^"'\s]+)['"]`)

var scriptTokenRe = regexp.MustCompile(`(?:var|let|const)\s+(?:v|data|token)\s*=\s*['"]([A-Za-z0-9_-]{6,})['"]`)

// hubcloudScriptToken handles older layouts embedding the token in a JS var;
// probes both known download endpoints before trusting one.
func hubcloudScriptToken(ctx context.Context, client *http.Client, pageURL, html string) string {
	m := scriptTokenRe.FindStringSubmatch(html)
	if m == nil {
		return ""
	}
	base := schemeHost(pageURL)
	for _, pattern := range []string{"%s/download?id=%s", "%s/download/%s"} {
		cand := fmt.Sprintf(pattern, base, m[1])
		if probeOK(ctx, client, cand) {
			return cand
		}
	}
	return ""
}
