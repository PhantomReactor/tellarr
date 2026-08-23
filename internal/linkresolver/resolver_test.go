package linkresolver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const hubLanding = `<!doctype html><html><head><title>Movie.2024.1080p.WEB-DL.mkv | HubCloud</title></head>
<body>
<div class="card">Movie.2024.1080p.WEB-DL.mkv (2GB)</div>
<a class="button" href="/download?id=abc123"><span>Download</span></a>
<a href="https://t.me/somechannel">Telegram</a>
</body></html>`

const hubDownloadPage = `<html><body>
<script>var v = 'abc123';</script>
<a href="https://drive.usercontent.google.com/download?id=FILE123&export=download&confirm=t">FSL Server 2.1 GB</a>
<a href="https://cdn.example.com/files/Movie.2024.1080p.WEB-DL.mkv">Direct Server 1.8 GB</a>
<a href="/index.html">Home</a>
</body></html>`

func TestResolveHubCloud(t *testing.T) {
	t.Setenv("HUBCLOUD_HOSTS", "127.0.0.1")
	var landingPath string
	mux := http.NewServeMux()
	mux.HandleFunc("/file/movie", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.ReplaceAll(hubLanding, "/download?id=abc123", landingPath)))
	})
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "abc123" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(hubDownloadPage))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	landingPath = srv.URL + "/download?id=abc123"

	res, err := Resolve(context.Background(), srv.URL+"/file/movie")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if res.URL != "https://cdn.example.com/files/Movie.2024.1080p.WEB-DL.mkv" {
		t.Fatalf("unexpected url %q", res.URL)
	}
	if res.Filename != "Movie.2024.1080p.WEB-DL.mkv" {
		t.Fatalf("unexpected filename %q", res.Filename)
	}
	mult := float64(1 << 30)
	wantSize := int64(1.8 * mult)
	if res.Size < wantSize || res.Size > wantSize+(1<<20) {
		t.Fatalf("unexpected size %d", res.Size)
	}
	if res.Headers["Referer"] == "" {
		t.Fatal("missing referer header")
	}
}

const gdflixFilePage = `<!doctype html><html><head><title>Show.S01E01.720p.HDRip.mkv - GDFlix</title></head>
<body>
<a href="/link/abc.html"><b>Drive Download</b></a>
</body></html>`

// Real-world HubCloud layout (Aug 2026): landing marks the hand-off with
// id="download" and a var url = '...' script; the intermediate page lists
// signed object-storage and dedicated-dl mirrors.
func TestResolveHubCloudCurrentLayout(t *testing.T) {
	t.Setenv("HUBCLOUD_HOSTS", "127.0.0.1")
	mux := http.NewServeMux()
	mux.HandleFunc("/drive/xyz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><head><title>Movie.2024.2160p.WEB-DL.H.265-Telly.mkv</title></head><body>
<a id="download" href="` + srvURL + `/hubcloud.php?host=hubcloud&id=xyz&token=TOK==">Download</a>
<script>var url = '` + srvURL + `/hubcloud.php?host=hubcloud&id=xyz&token=TOK==';</script>
<a href="https://winexch.com/?btag=1">Ad</a>
</body></html>`))
	})
	mux.HandleFunc("/hubcloud.php", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><head><title>Movie.2024.2160p.WEB-DL.H.265-Telly.mkv</title></head><body>
<a href="https://abc123.r2.cloudflarestorage.com/hub/deadbeef?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Expires=28800&response-content-disposition=attachment">Download [FSL Server]</a>
<a href="https://gpdl.hubcloud.cx/?id=fe805f">Download [Server : 10Gbps]</a>
<a href="https://t.me/hubcloudreport">@Telegram Group</a>
<a href="https://tinyurl.com/x">Tutorial</a>
</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL = srv.URL

	res, err := Resolve(context.Background(), srv.URL+"/drive/xyz")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if !strings.Contains(res.URL, "r2.cloudflarestorage.com") {
		t.Fatalf("expected signed R2 link, got %q", res.URL)
	}
	if res.Filename != "Movie.2024.2160p.WEB-DL.H.265-Telly.mkv" {
		t.Fatalf("unexpected filename %q", res.Filename)
	}
}

// Real-world GDFlix layout (Aug 2026): gdflix.dev 302s to new3.gdflix.io;
// live pages expose a direct CDN link whose path ends in the true filename,
// while the page title is prefixed with the site name.
func TestResolveGDFlixCurrentLayout(t *testing.T) {
	t.Setenv("GDFLIX_HOSTS", "127.0.0.1")
	mux := http.NewServeMux()
	mux.HandleFunc("/file/live", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><head><title>GDFlix | Show.S01E01.1080p.WEB-DL.H.265-RGP.mkv</title></head><body>
<a href="https://new3.gdflix.test">GDFlix</a><a href="/login">Log in</a>
<a href="https://cdn.indexserver.site/TOKEN123/new3.gdflix.test/Show.S01E01.1080p.WEB-DL.H.265-RGP.mkv">Download</a>
</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	res, err := Resolve(context.Background(), srv.URL+"/file/live")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if !strings.Contains(res.URL, "cdn.indexserver.site") || !strings.HasSuffix(res.URL, ".mkv") {
		t.Fatalf("unexpected url %q", res.URL)
	}
	if res.Filename != "Show.S01E01.1080p.WEB-DL.H.265-RGP.mkv" {
		t.Fatalf("unexpected filename %q (site prefix must be stripped)", res.Filename)
	}
}

var srvURL string

// Real-world GDFlix layout: gdflix.dev 302s to new3.gdflix.io; dead files
// render a "no longer available" shell whose only anchor is the site logo.
func TestResolveGDFlixDeadFile(t *testing.T) {
	t.Setenv("GDFLIX_HOSTS", "127.0.0.1")
	mux := http.NewServeMux()
	mux.HandleFunc("/file/dead", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><head><title>GDFlix | Google Drive Files Sharing Platform</title></head><body>
<a href="https://gdflix.test">GDFlix</a><a href="/login">Log in</a>
<div>404! Page Not Found</div>
<div>The file you are trying to download is no longer available!</div>
</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	res, err := Resolve(context.Background(), srv.URL+"/file/dead")
	if err == nil {
		t.Fatalf("dead file must fail, got %+v", res)
	}
	if !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestScoreCandidateRejectsBareOrigin(t *testing.T) {
	if scoreCandidate(candidate{url: "https://new3.gdflix.io"}) >= 0 {
		t.Error("bare origin must be rejected")
	}
	if scoreCandidate(candidate{url: "https://new3.gdflix.io/"}) >= 0 {
		t.Error("origin root must be rejected")
	}
	if scoreCandidate(candidate{url: "https://cdn.example.com/file/Movie.mkv"}) < 0 {
		t.Error("real file must not be rejected")
	}
}

func TestScoreCandidateRejectsJunkAndPrefersDirect(t *testing.T) {
	r2 := candidate{url: "https://x.r2.cloudflarestorage.com/hub/beef?X-Amz-Algorithm=A", label: "Download [FSL]"}
	ad := candidate{url: "https://winexch.com/?btag=1", label: "Download"}
	page := candidate{url: "https://example.com/file/index.html"}
	gpdl := candidate{url: "https://gpdl.hubcloud.cx/?id=fe805f", label: "Download [10Gbps]"}
	video := candidate{url: "https://cdn.example.com/Movie.2024.mkv"}

	if scoreCandidate(ad) >= 0 {
		t.Error("ad host must be rejected")
	}
	if !(scoreCandidate(r2) > scoreCandidate(gpdl) && scoreCandidate(gpdl) > scoreCandidate(page)) {
		t.Errorf("unexpected ordering: r2=%d gpdl=%d page=%d",
			scoreCandidate(r2), scoreCandidate(gpdl), scoreCandidate(page))
	}
	if best := pickBest([]candidate{ad, page, gpdl, video}); best.url != video.url {
		t.Fatalf("video must win: %+v", best)
	}
}

const gdflixLinkPage = `<html><body>
<form action="/link/generate" method="post">
<input type="hidden" name="token" value="xyz789">
<button>Generate Link</button>
</form>
</body></html>`

const gdflixGenerateResp = `{"status":200,"gd_link":"https://cdn.test/file/Show.S01E01.720p.HDRip.mkv","msg":"ok"}`

func TestResolveGDFlix(t *testing.T) {
	t.Setenv("GDFLIX_HOSTS", "127.0.0.1")
	mux := http.NewServeMux()
	mux.HandleFunc("/file/show", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(gdflixFilePage))
	})
	mux.HandleFunc("/link/abc.html", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(gdflixLinkPage))
	})
	mux.HandleFunc("/link/generate", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.FormValue("token") != "xyz789" {
			http.Error(w, "bad token", http.StatusBadRequest)
			return
		}
		w.Write([]byte(gdflixGenerateResp))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	res, err := Resolve(context.Background(), srv.URL+"/file/show")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if res.URL != "https://cdn.test/file/Show.S01E01.720p.HDRip.mkv" {
		t.Fatalf("unexpected url %q", res.URL)
	}
	if res.Filename != "Show.S01E01.720p.HDRip.mkv" {
		t.Fatalf("unexpected filename %q", res.Filename)
	}
}

func TestProviderDetection(t *testing.T) {
	cases := []struct {
		url  string
		name string
	}{
		{"https://hubcloud.hub/file/x", "hubcloud"},
		{"https://hubcloud1.driveuser.com/drive/index.php?id=zzz", "hubcloud"},
		{"https://gdflix.top/file/y", "gdflix"},
		{"https://example.com/file/z", ""},
	}
	for _, c := range cases {
		if got := Name(c.url); got != c.name {
			t.Errorf("Name(%s) = %q want %q", c.url, got, c.name)
		}
	}
}

func TestEnvHostOverride(t *testing.T) {
	t.Setenv("HUBCLOUD_HOSTS", "myhubmirror.example")
	if got := Name("https://myhubmirror.example/file/1"); got != "hubcloud" {
		t.Errorf("env override not applied: %q", got)
	}
	if got := Name("https://other.example"); got != "" {
		t.Errorf("unexpected match: %q", got)
	}
}

func TestPickBestSkipsTelegram(t *testing.T) {
	cands := []candidate{
		{url: "https://t.me/channel"},
		{url: "https://x.com/a.mp4"},
	}
	best := pickBest(cands)
	if best == nil || best.url != "https://x.com/a.mp4" {
		t.Fatalf("wrong best: %+v", best)
	}
}
