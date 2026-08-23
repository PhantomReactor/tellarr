package linkresolver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
)

// resolveGDFlix walks the typical flow:
//
//	file page (/file/slug) -> "drive download" anchor -> intermediate
//	page that may POST a generate-link form -> JSON/HTML with the direct link
//
// Some variants expose direct anchors right on the landing page.
func resolveGDFlix(ctx context.Context, client *http.Client, rawURL string) (*Result, error) {
	landing, err := fetchPage(ctx, client, rawURL)
	if err != nil {
		return nil, err
	}
	if err := deadFileErr(landing); err != nil {
		return nil, err
	}

	var cands []candidate
	collect := func(p *page) {
		for _, u := range jsonURLs(p.Body) {
			cands = append(cands, candidate{url: u})
		}
		cands = append(cands, extractCandidates(p.FinalURL, p.Body)...)
	}
	collect(landing)

	hops := 0
	for _, link := range gdflixIntermediateLinks(landing.FinalURL, landing.Body) {
		if hops >= 4 {
			break
		}
		sub, err := fetchPage(ctx, client, link)
		if err != nil {
			slog.Debug("gdflix: hop failed", "url", link, "err", err)
			continue
		}
		hops++
		collect(sub)

		if best := pickBest(copyCands(cands)); best != nil && scoreCandidate(*best) >= 90 {
			break
		}

		for _, f := range findForms(sub.FinalURL, sub.Body) {
			if !strings.Contains(strings.ToLower(f.Action), "generate") &&
				!strings.Contains(strings.ToLower(f.Action), "link") {
				continue
			}
			resp, err := submitForm(ctx, client, f, sub.FinalURL)
			if err != nil {
				continue
			}
			collect(resp)
			if best := pickBest(copyCands(cands)); best != nil && scoreCandidate(*best) >= 90 {
				break
			}
		}
	}

	best := pickBest(cands)
	if best == nil {
		return nil, fmt.Errorf("no direct links found")
	}
	res := &Result{
		URL:  best.url,
		Size: best.size,
		Headers: map[string]string{
			"Referer": landing.FinalURL,
		},
	}
	filenameForResult(res, landing.Body)
	if res.Filename == "" && best.label != "" && !strings.Contains(best.label, "http") {
		res.Filename = sanitizeFilename(best.label)
	}
	return res, nil
}

var driveHintRe = regexp.MustCompile(`(?i)(?:drive|download|direct|instant|server)`)

// gdflixIntermediateLinks picks anchors on the file page worth following.
func gdflixIntermediateLinks(baseURL, html string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range anchorRe.FindAllStringSubmatch(html, -1) {
		u := absURL(baseURL, m[1])
		if u == "" || seen[u] || !strings.Contains(u, "http") {
			continue
		}
		text := strings.ToLower(m[1] + " " + stripTags(m[2]))
		if strings.Contains(u, schemeHost(baseURL)) || strings.Contains(text, "drive") || driveHintRe.MatchString(stripTags(m[2])) {
			if hasExt(u, pageExt) || strings.Contains(u, "/link") || strings.Contains(u, "drive") || strings.Contains(text, "download") {
				seen[u] = true
				out = append(out, u)
			}
		}
	}
	return out
}

func copyCands(in []candidate) []candidate {
	out := make([]candidate, len(in))
	copy(out, in)
	return out
}
