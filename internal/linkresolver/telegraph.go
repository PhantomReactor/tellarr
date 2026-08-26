package linkresolver

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// TargetKind classifies a downloadable target scraped from an HTML page.
type TargetKind string

const (
	TargetProvider TargetKind = "provider" // hubcloud/gdflix/tmbcloud page
	TargetTorrent  TargetKind = "torrent"  // direct .torrent file URL
	TargetMagnet   TargetKind = "magnet"   // magnet: URI
	TargetFile     TargetKind = "file"     // direct http(s) file URL
)

// Target is one downloadable candidate found on a page, in document order.
type Target struct {
	URL   string
	Label string
	Kind  TargetKind
}

// IsTelegraph reports whether the URL points at an Instant View page
// (telegra.ph or graph.org), which posters use to bundle download links.
func IsTelegraph(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Host)
	return host == "telegra.ph" || host == "graph.org" ||
		strings.HasSuffix(host, ".telegra.ph") || strings.HasSuffix(host, ".graph.org")
}

var magnetRe = regexp.MustCompile(`magnet:\?xt=urn:btih:[A-Za-z0-9]+(?:[&?][^\s<>"']*)?`)

// ScrapeTargets fetches any HTML page and extracts downloadable targets:
// links to supported provider pages, .torrent files, magnet URIs embedded in
// text, and direct media/archive URLs. Order follows the document so callers
// can apply stable pick policies.
func ScrapeTargets(ctx context.Context, rawURL string) ([]Target, error) {
	client := newFetchClient()
	p, err := fetchPage(ctx, client, rawURL)
	if err != nil {
		return nil, err
	}
	if p.Status != 200 || len(p.Body) < 50 {
		return nil, fmt.Errorf("page fetch failed (status %d)", p.Status)
	}

	var out []Target
	seen := map[string]bool{}
	add := func(raw, label string) {
		raw = strings.TrimSpace(strings.TrimRight(raw, ".,);]"))
		if raw == "" {
			return
		}
		key := strings.ToLower(strings.TrimRight(raw, "/"))
		if seen[key] {
			return
		}
		t := classifyTarget(absURL(p.FinalURL, raw), stripTags(label))
		if t == nil {
			return
		}
		seen[key] = true
		out = append(out, *t)
	}

	for _, m := range anchorRe.FindAllStringSubmatch(p.Body, -1) {
		add(m[1], m[2])
	}
	for _, m := range jsURLRes {
		for _, mm := range m.FindAllStringSubmatch(p.Body, -1) {
			add(mm[1], "")
		}
	}
	for _, m := range magnetRe.FindAllString(p.Body, -1) {
		add(m, "")
	}
	return out, nil
}

// classifyTarget maps a raw URL onto a Target; nil when it is not
// downloadable (nav links, ads, socials, plain pages).
func classifyTarget(raw, label string) *Target {
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(strings.ToLower(raw), "magnet:") {
		return &Target{URL: raw, Label: label, Kind: TargetMagnet}
	}
	if Name(raw) != "" {
		return &Target{URL: raw, Label: label, Kind: TargetProvider}
	}
	lower := strings.ToLower(raw)
	if hasExt(lower, []string{".torrent"}) {
		return &Target{URL: raw, Label: label, Kind: TargetTorrent}
	}
	if hasExt(lower, directNameExts) {
		c := candidate{url: raw, label: label}
		// Reuse the provider-page junk filter so ads/socials never pass.
		if scoreCandidate(c) < 0 {
			return nil
		}
		return &Target{URL: raw, Label: label, Kind: TargetFile}
	}
	return nil
}

// PickTarget applies the add-download policy over scraped targets: provider
// mirrors first (they are the curated sources), then torrents, then magnets,
// then bare files. Returns nil when nothing is usable.
func PickTarget(targets []Target) *Target {
	for _, kind := range []TargetKind{TargetProvider, TargetTorrent, TargetMagnet, TargetFile} {
		for i := range targets {
			if targets[i].Kind == kind {
				return &targets[i]
			}
		}
	}
	return nil
}

// LooksLikeFileURL reports whether the URL itself points at a file (by
// extension) so it can skip page scraping entirely.
func LooksLikeFileURL(rawURL string) bool {
	return hasExt(strings.ToLower(rawURL), append(append([]string{}, directNameExts...), ".torrent"))
}

// FileNameFromURL extracts a sanitized basename from the URL path.
func FileNameFromURL(rawURL string) string {
	return filenameFromURL(rawURL)
}
