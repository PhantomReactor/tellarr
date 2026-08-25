package server

import (
	"testing"

	"github.com/gotd/td/tg"
)

func TestProviderLinksInMessageMultiQuality(t *testing.T) {
	msg := &tg.Message{
		Message: "Movie Name 2024\n" +
			"480p HDRip => https://gdflix.top/file/abc1\n" +
			"720p WEB-DL => https://gdflix.top/file/abc2",
	}
	links := providerLinksInMessage(msg)
	if len(links) != 2 {
		t.Fatalf("expected 2 links, got %d: %+v", len(links), links)
	}
	if links[0].URL != "https://gdflix.top/file/abc1" || links[0].Title != "Movie Name 2024 480p HDRip" {
		t.Errorf("link0 = %+v", links[0])
	}
	if links[1].URL != "https://gdflix.top/file/abc2" || links[1].Title != "Movie Name 2024 720p WEB-DL" {
		t.Errorf("link1 = %+v", links[1])
	}
}

func TestProviderLinksEntityLabelBecomesTitle(t *testing.T) {
	text := "EP01 720p HEVC"
	msg := &tg.Message{
		Message: text,
		Entities: []tg.MessageEntityClass{
			&tg.MessageEntityTextURL{Offset: 0, Length: len(text), URL: "https://hubcloud.hub/file/ep1"},
		},
	}
	links := providerLinksInMessage(msg)
	if len(links) != 1 || links[0].Title != "EP01 720p HEVC" {
		t.Fatalf("unexpected links: %+v", links)
	}
}

func TestProviderLinksGenericLabelIgnored(t *testing.T) {
	text := "Season 1 Pack"
	msg := &tg.Message{
		Message: text,
		Entities: []tg.MessageEntityClass{
			&tg.MessageEntityTextURL{Offset: 0, Length: len(text), URL: "https://gdflix.top/file/s01"},
		},
	}
	// "Season 1 Pack" is not in the generic blocklist so it is kept.
	links := providerLinksInMessage(msg)
	if len(links) != 1 || links[0].Title != "Season 1 Pack" {
		t.Fatalf("unexpected links: %+v", links)
	}

	generic := &tg.Message{
		Message: "Download",
		Entities: []tg.MessageEntityClass{
			&tg.MessageEntityTextURL{Offset: 0, Length: 8, URL: "https://gdflix.top/file/s02"},
		},
	}
	links = providerLinksInMessage(generic)
	if len(links) != 1 || links[0].Title == "Download" {
		t.Fatalf("generic label should fall back to slug/header, got %+v", links)
	}
}

func TestProviderLinksDuplicateTitlesGetSuffixes(t *testing.T) {
	msg := &tg.Message{
		Message: "Movie X 2024\n" +
			"https://hubcloud.hub/drive/index.php?id=aaa\n" +
			"https://hubcloud.hub/drive/index.php?id=bbb",
	}
	links := providerLinksInMessage(msg)
	if len(links) != 2 {
		t.Fatalf("expected 2 links, got %d", len(links))
	}
	if links[0].Title == links[1].Title {
		t.Fatalf("titles must differ to avoid hash collisions: %q", links[0].Title)
	}
	if links[1].Title != links[0].Title+" [2]" {
		t.Errorf("expected suffix ordering, got %q and %q", links[0].Title, links[1].Title)
	}
}

func TestTitleForLinkMatchesSearchTime(t *testing.T) {
	msg := &tg.Message{
		Message: "Show S01\n" +
			"EP01 => https://gdflix.top/file/e1\n" +
			"EP02 => https://gdflix.top/file/e2",
	}
	for _, ref := range providerLinksInMessage(msg) {
		got := titleForLink(msg, ref.URL)
		if got != ref.Title {
			t.Errorf("titleForLink(%s) = %q want %q", ref.URL, got, ref.Title)
		}
	}
	if got := titleForLink(msg, "https://gdflix.top/file/missing"); got == "" {
		t.Error("fallback title must not be empty")
	}
}

func TestProviderURLsOrderStable(t *testing.T) {
	msg := &tg.Message{
		Message: "T\nA => https://gdflix.top/file/a\nB => https://hubcloud.hub/file/b",
	}
	first := providerURLsInMessage(msg)
	second := providerURLsInMessage(msg)
	if len(first) != 2 || first[0] != second[0] || first[1] != second[1] {
		t.Fatalf("unstable order: %v vs %v", first, second)
	}
}

func TestLinkPostTitleDeterministic(t *testing.T) {
	msg := &tg.Message{Message: "Movie.Name.2024.1080p.WEB-DL\nhttps://hubcloud.hub/file/abc"}
	a := linkPostTitle(msg, "https://hubcloud.hub/file/abc")
	b := linkPostTitle(msg, "https://hubcloud.hub/file/abc")
	if a == "" || a != b {
		t.Fatalf("title not deterministic: %q vs %q", a, b)
	}
	if a != "Movie.Name.2024.1080p.WEB-DL" {
		t.Fatalf("unexpected title: %q", a)
	}
}

func TestLinkPostTitleFromSlug(t *testing.T) {
	if got := linkPostTitle(nil, "https://hubcloud.hub/file/Movie.Name.2024.html"); got != "Movie.Name.2024" {
		t.Fatalf("unexpected slug title: %q", got)
	}
	if got := linkPostTitle(nil, "https://hubcloud.hub/drive/index.php?id=zzz"); got != "external-download" {
		t.Fatalf("generic slug should fall through: %q", got)
	}
}

func TestParseTellarrURL(t *testing.T) {
	m := magnetFor("http://x", "h1", "n", 5, 11, 22, "https://hubcloud.hub/file/q")
	got := parseTellarrURL(m)
	if got != "https://hubcloud.hub/file/q" {
		t.Fatalf("got %q", got)
	}
	m2 := magnetFor("http://x", "h1", "n", 5, 11, 22, "")
	if parseTellarrURL(m2) != "" {
		t.Fatal("magnet without link must yield empty")
	}
}

func TestExternalHashStable(t *testing.T) {
	u := "https://hubcloud.hub/file/abc"
	if externalHash(u) != externalHash(u) || externalHash(u) == SyntheticHash(1, 2, "x") {
		t.Fatal("external hash unstable or collides with synthetic hash")
	}
}

func TestGuessSizeFromTitleAndMessage(t *testing.T) {
	perLink := &tg.Message{
		Message: "Movie Name 2024\n" +
			"480p HDRip 1.4GB => https://gdflix.top/file/abc1\n" +
			"720p WEB-DL => https://gdflix.top/file/abc2",
	}
	links := providerLinksInMessage(perLink)
	if len(links) != 2 {
		t.Fatalf("expected 2 links, got %d", len(links))
	}
	gib := float64(1 << 30)
	want := int64(1.4 * gib)
	if got := guessSize(perLink, links[0].Title); got < want-(1<<20) || got > want+(1<<20) {
		t.Errorf("link0 size = %d, want ~%d", got, want)
	}
	// No size on this line: falls back to the message-wide value.
	fallback := &tg.Message{Message: "Movie Name 2024\n1080p => https://gdflix.top/file/abc1"}
	links = providerLinksInMessage(fallback)
	got := guessSize(fallback, links[0].Title)
	if got != 0 {
		t.Errorf("message without any size should yield 0, got %d", got)
	}
	withSize := &tg.Message{Message: "Movie Name 2024 [2.5 GB]\n1080p => https://gdflix.top/file/abc1"}
	links = providerLinksInMessage(withSize)
	got = guessSize(withSize, links[0].Title)
	want = int64(2.5 * gib)
	if got < want-(1<<20) || got > want+(1<<20) {
		t.Errorf("fallback size = %d, want ~%d", got, want)
	}
	if guessSize(nil, "") != 0 {
		t.Error("nil message with empty title should yield 0")
	}
}
