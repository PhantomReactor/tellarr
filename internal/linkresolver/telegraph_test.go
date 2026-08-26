package linkresolver

import "testing"

func TestIsTelegraph(t *testing.T) {
	yes := []string{
		"https://telegra.ph/some-post-01-15",
		"http://graph.org/Another-Post-03-02",
		"https://www.telegra.ph/x",
	}
	for _, u := range yes {
		if !IsTelegraph(u) {
			t.Errorf("IsTelegraph(%q) = false, want true", u)
		}
	}
	no := []string{
		"https://example.com/post",
		"https://telegra.ph.evil.io/phish",
		"magnet:?xt=urn:btih:abc",
		"",
	}
	for _, u := range no {
		if IsTelegraph(u) {
			t.Errorf("IsTelegraph(%q) = true, want false", u)
		}
	}
}

func TestClassifyTarget(t *testing.T) {
	cases := []struct {
		raw  string
		kind TargetKind
	}{
		{"https://hubcloud.example.org/drive/index.php?id=abc", TargetProvider},
		{"https://gdflix.example.org/file/abc123", TargetProvider},
		{"https://cdn.example.org/Movie.2024.1080p.mkv", TargetFile},
		{"https://tracker.example.org/release.torrent", TargetTorrent},
		{"magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567", TargetMagnet},
	}
	for _, c := range cases {
		got := classifyTarget(c.raw, "")
		if got == nil {
			t.Errorf("classifyTarget(%q) = nil, want %s", c.raw, c.kind)
			continue
		}
		if got.Kind != c.kind {
			t.Errorf("classifyTarget(%q).Kind = %s, want %s", c.raw, got.Kind, c.kind)
		}
	}

	// Junk (ads/socials/nav) never classifies as a file.
	for _, junk := range []string{
		"https://t.me/somechannel",
		"https://bit.ly/xyz",
		"https://facebook.com/page",
		"https://example.com/",
	} {
		if classifyTarget(junk, "") != nil {
			t.Errorf("junk target %q unexpectedly classified", junk)
		}
	}
}

func TestPickTargetPriority(t *testing.T) {
	targets := []Target{
		{URL: "https://cdn.example.org/a.mkv", Kind: TargetFile},
		{URL: "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567", Kind: TargetMagnet},
		{URL: "https://gdflix.example.org/file/xyz", Kind: TargetProvider},
		{URL: "https://t.example.org/b.torrent", Kind: TargetTorrent},
	}
	pick := PickTarget(targets)
	if pick == nil || pick.Kind != TargetProvider {
		t.Fatalf("provider should win, got %+v", pick)
	}

	noProvider := []Target{targets[0], targets[1], targets[3]}
	pick = PickTarget(noProvider)
	if pick == nil || pick.Kind != TargetTorrent {
		t.Fatalf("torrent should beat magnet when no provider, got %+v", pick)
	}

	pick = PickTarget([]Target{targets[1], targets[0]})
	if pick == nil || pick.Kind != TargetMagnet {
		t.Fatalf("magnet should come before file, got %+v", pick)
	}
}

func TestLooksLikeFileURL(t *testing.T) {
	if !LooksLikeFileURL("https://cdn.example.org/movie.mp4") {
		t.Fatal("mp4 not detected as file")
	}
	if !LooksLikeFileURL("https://x.example.org/f.torrent") {
		t.Fatal(".torrent not detected as file")
	}
	if LooksLikeFileURL("https://telegra.ph/some-post") {
		t.Fatal("html page misdetected as file")
	}
}
