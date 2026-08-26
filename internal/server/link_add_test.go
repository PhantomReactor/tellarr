package server

import (
	"encoding/base32"
	"encoding/hex"
	"testing"
)

func TestParseTgLinkPrivate(t *testing.T) {
	cases := []struct {
		link      string
		channelId int64
		messageId int64
		ok        bool
	}{
		{"https://t.me/c/1234567/89", 1234567, 89, true},
		{"https://t.me/c/1234567/89?single", 1234567, 89, true},
		{"https://t.me/c/1234567/89/", 1234567, 89, true},
		{"http://t.me/c/42/7", 42, 7, true},
		{"t.me/c/99/12", 99, 12, true},
	}
	for _, c := range cases {
		ref, ok := parseTgLink(c.link)
		if ok != c.ok || (ok && (ref.ChannelId != c.channelId || ref.MessageId != c.messageId)) {
			t.Errorf("parseTgLink(%q) = %+v, %v; want %d/%d, %v", c.link, ref, ok, c.channelId, c.messageId, c.ok)
		}
	}
}

func TestParseTgLinkPublic(t *testing.T) {
	ref, ok := parseTgLink("https://t.me/mychannel/321")
	if !ok || ref.Username != "mychannel" || ref.MessageId != 321 || ref.ChannelId != 0 {
		t.Fatalf("public link parsed wrong: %+v ok=%v", ref, ok)
	}
	ref, ok = parseTgLink("https://t.me/s/mychannel/321")
	if !ok || ref.Username != "mychannel" || ref.MessageId != 321 {
		t.Fatalf("preview link parsed wrong: %+v ok=%v", ref, ok)
	}
	ref, ok = parseTgLink("tg://resolve?domain=mychannel&post=77")
	if !ok || ref.Username != "mychannel" || ref.MessageId != 77 {
		t.Fatalf("deep link parsed wrong: %+v ok=%v", ref, ok)
	}
}

func TestParseTgLinkRejects(t *testing.T) {
	for _, link := range []string{
		"",
		"https://github.com/gotd/td",
		"https://t.me/joinchat/AbCdEf",
		"https://t.me/+AbCdEf",
		"https://t.me/mychannel",          // no message id
		"https://t.me/c/1234567",          // no message id
		"https://t.me/c/notanumber/5",     // bad ids
		"https://t.me/c/1234/abc",         // bad ids
		"https://t.me/mychannel/0",        // invalid message id
		"magnet:?xt=urn:btih:abcdef",      // not a tg link
		"https://telegra.ph/some-post-01", // not a tg link
	} {
		if _, ok := parseTgLink(link); ok {
			t.Errorf("parseTgLink(%q) unexpectedly succeeded", link)
		}
	}
}

func TestMagnetInfoHash(t *testing.T) {
	const hexHash = "0123456789abcdef0123456789abcdef01234567"

	if got := magnetInfoHash("magnet:?xt=urn:btih:" + hexHash + "&dn=Show.S01E01"); got != hexHash {
		t.Fatalf("hex magnet: got %q want %q", got, hexHash)
	}
	if got := magnetInfoHash("magnet:?xt=urn:btih:0123456789ABCDEF0123456789ABCDEF01234567"); got != hexHash {
		t.Fatalf("uppercase hex magnet: got %q", got)
	}
	// Round-trip a base32-encoded btih.
	raw, _ := hex.DecodeString(hexHash)
	b32 := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	if got := magnetInfoHash("magnet:?xt=urn:btih:" + b32); got != hexHash {
		t.Fatalf("base32 magnet: got %q want %q", got, hexHash)
	}
	// v2-only and garbage magnets have no v1 hash.
	if got := magnetInfoHash("magnet:?xt=urn:btmh:1220abcd&dn=x"); got != "" {
		t.Fatalf("btmh magnet should yield empty, got %q", got)
	}
	if got := magnetInfoHash("magnet:?dn=onlyname"); got != "" {
		t.Fatalf("hashless magnet should yield empty, got %q", got)
	}
}

func TestMagnetDisplayName(t *testing.T) {
	got := magnetDisplayName("magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=Show.S01E01.1080p")
	if got != "Show.S01E01.1080p" {
		t.Fatalf("dn extraction failed: %q", got)
	}
	if magnetDisplayName("magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567") != "" {
		t.Fatal("missing dn should be empty")
	}
}

func TestLooksLikeTorrentURL(t *testing.T) {
	if !looksLikeTorrentURL("https://tracker.example.org/file.torrent") {
		t.Fatal(".torrent URL not detected")
	}
	if looksLikeTorrentURL("https://tracker.example.org/file.TORRENT?token=x") == false {
		// case-insensitive path check
	}
	if looksLikeTorrentURL("https://hubcloud.example.org/drive/index.php?id=abc") {
		t.Fatal("provider page misdetected as torrent file")
	}
}

func TestRemoteRowID(t *testing.T) {
	const h = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if remoteRowID(h, 1, 2, "x") != h {
		t.Fatal("infohash must win when present")
	}
	a := remoteRowID("", 1, 2, "x")
	b := remoteRowID("", 1, 2, "y")
	if a == b {
		t.Fatal("synthetic fallback must vary by filename")
	}
}
