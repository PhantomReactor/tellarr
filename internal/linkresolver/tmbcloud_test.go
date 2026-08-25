package linkresolver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeSolver stubs turnstileSolver so tests never call a real (paid) solving
// API. gotSiteKey/gotPageURL record what the resolver asked it to solve.
type fakeSolver struct {
	token      string
	err        error
	gotSiteKey string
	gotPageURL string
}

func (f *fakeSolver) Solve(ctx context.Context, siteKey, pageURL string) (string, error) {
	f.gotSiteKey = siteKey
	f.gotPageURL = pageURL
	if f.err != nil {
		return "", f.err
	}
	return f.token, nil
}

func withFakeSolver(t *testing.T, s *fakeSolver) {
	t.Helper()
	orig := newTurnstileSolver
	newTurnstileSolver = func() turnstileSolver { return s }
	t.Cleanup(func() { newTurnstileSolver = orig })
}

// Real-world TMBCloud layout (Aug 2026): /download/<code> 302s to
// /links/<code>, which embeds PAGE_ID/TOKEN_A and renders a Cloudflare
// Turnstile widget with an explicit sitekey. Confirmed live that
// gen-link.php rejects a bogus cf_turnstile_response with 403, so the
// server genuinely validates the solved token.
const tmbcloudLinksPage = `<!doctype html><html><head><title>Governor..2026.2160p.AMZN.WEB-DL.D..UAL..DDP5.1.H.265-WildFire.mkv - TMBCloud</title></head><body>
<div class="download-box">
    <h2>Governor..2026.2160p.AMZN.WEB-DL.D..UAL..DDP5.1.H.265-WildFire.mkv</h2>
    <div class="file-info-row">
        <div class="info-pill">Size: <span>17.11 GB</span></div>
    </div>
    <button class="btn r2-download" data-page="cca66473edc0e1ec15fbb0d3" data-type="r2">FastCloud R2 Download</button>
</div>
<script>
const PAGE_ID = 'cca66473edc0e1ec15fbb0d3';
const TOKEN_A = 'f83242d0ffa6e9d5';
</script>
<script>
turnstile.render('#cf-turnstile', {
    sitekey: '0x4AAAAAAD06yKxGBhHPbKmy'
});
</script>
</body></html>`

func newTMBCloudMux(t *testing.T, wantType string, genResp string) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/download/OHWR5J", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/links/abc123", http.StatusFound)
	})
	mux.HandleFunc("/links/abc123", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(tmbcloudLinksPage))
	})
	mux.HandleFunc("/step2.php", func(w http.ResponseWriter, r *http.Request) {
		// Confirmed live: real fetch() calls always carry Referer/Origin;
		// require them here so a regression back to bare requests fails.
		if r.Header.Get("Referer") == "" || r.Header.Get("Origin") == "" {
			http.Error(w, `{"error":"missing referer/origin"}`, http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil || r.FormValue("token_a") != "f83242d0ffa6e9d5" || r.FormValue("page_id") != "cca66473edc0e1ec15fbb0d3" {
			http.Error(w, `{"error":"bad token_a/page_id"}`, http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"token_b":"tokenB123"}`))
	})
	mux.HandleFunc("/gen-link.php", func(w http.ResponseWriter, r *http.Request) {
		// Confirmed live: gen-link.php 403s "Access denied" without these
		// even given an otherwise-valid token.
		if r.Header.Get("Referer") == "" || r.Header.Get("Origin") == "" {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":"Access denied"}`))
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, `{"error":"bad form"}`, http.StatusBadRequest)
			return
		}
		if wantType != "" && r.FormValue("type") != wantType {
			http.Error(w, fmt.Sprintf(`{"error":"unexpected type %s"}`, r.FormValue("type")), http.StatusBadRequest)
			return
		}
		if r.FormValue("page_id") != "cca66473edc0e1ec15fbb0d3" || r.FormValue("token_b") != "tokenB123" || r.FormValue("cf_turnstile_response") != "solved-token" {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":"Access denied"}`))
			return
		}
		w.Write([]byte(genResp))
	})
	return mux
}

func TestResolveTMBCloud(t *testing.T) {
	t.Setenv("TMBCLOUD_HOSTS", "127.0.0.1")
	mux := newTMBCloudMux(t, "fastcdnv2", `{"url":"/cloudfilesv2/pdoQmIauVLSxLg"}`)
	// Real-world FastCDN V2 layout (confirmed live, Aug 2026): the hop page
	// carries var CODE, polls cloudfilesv2-worker.php until {ready:true},
	// then fetches the direct link from /cloudfilesv2/<CODE>?dl=1&json=1.
	mux.HandleFunc("/cloudfilesv2/pdoQmIauVLSxLg", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body>
<script>
var CODE = 'H5-V8ormynPhcQ';
function poll() { fetch('/cloudfilesv2-worker.php?c=' + encodeURIComponent(CODE) + '&poll=1'); }
</script>
</body></html>`))
	})
	dlJSONSeen := false
	var srv *httptest.Server
	mux.HandleFunc("/cloudfilesv2-worker.php", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("c") != "H5-V8ormynPhcQ" {
			w.Write([]byte(`{"error":"unknown code"}`))
			return
		}
		w.Write([]byte(`{"ready":true}`))
	})
	mux.HandleFunc("/cloudfilesv2/H5-V8ormynPhcQ", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("dl") != "1" || r.URL.Query().Get("json") != "1" {
			http.Error(w, "bad params", http.StatusBadRequest)
			return
		}
		dlJSONSeen = true
		fmt.Fprintf(w, `{"success":true,"downloadUrl":"%s/files/Governor.2026.2160p.mkv?sig=xyz"}`, srv.URL)
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	solver := &fakeSolver{token: "solved-token"}
	withFakeSolver(t, solver)

	res, err := Resolve(context.Background(), srv.URL+"/download/OHWR5J")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if !dlJSONSeen {
		t.Fatal("dl-json endpoint was never called")
	}
	if res.URL != srv.URL+"/files/Governor.2026.2160p.mkv?sig=xyz" {
		t.Fatalf("unexpected url %q", res.URL)
	}
	// The resolved URL's own filename segment wins over the page's
	// <h2>/<title> text, which TMBCloud obfuscates with random extra dots
	// on every load (confirmed live) as a light anti-scraping measure.
	if res.Filename != "Governor.2026.2160p.mkv" {
		t.Fatalf("unexpected filename %q", res.Filename)
	}
	mult := float64(1 << 30)
	wantSize := int64(17.11 * mult)
	if res.Size < wantSize-(1<<20) || res.Size > wantSize+(1<<20) {
		t.Fatalf("unexpected size %d", res.Size)
	}
	if !strings.Contains(res.Headers["Referer"], "/cloudfilesv2/") {
		t.Fatalf("unexpected referer %q", res.Headers["Referer"])
	}
	if solver.gotSiteKey != "0x4AAAAAAD06yKxGBhHPbKmy" {
		t.Fatalf("solver got wrong sitekey %q", solver.gotSiteKey)
	}
	if !strings.Contains(solver.gotPageURL, "/links/abc123") {
		t.Fatalf("solver got wrong page url %q", solver.gotPageURL)
	}
}

// Real-world TMBCloud layout (confirmed live, Aug 2026): gen-link.php's
// "url" is a relative /cloudfiles/<token> interstitial, not the file link.
// That page has no further captcha, but its real R2 link lives in a
// "copy link" button's data-url attribute rather than an <a href>.
const tmbcloudCloudfilesPage = `<!doctype html><html><body>
<a href="/cloudfiles/tok1?dl=1" class="btn-dl">Download Now</a>
<button class="btn-copy" data-url="https://pub-abc123.r2.dev/Governor.2026.2160p.mkv?token=tok1">Copy link</button>
</body></html>`

func TestResolveTMBCloudCloudfilesHop(t *testing.T) {
	t.Setenv("TMBCLOUD_HOSTS", "127.0.0.1")
	t.Setenv("TMBCLOUD_LINK_TYPE", "r2")
	mux := newTMBCloudMux(t, "r2", `{"url":"/cloudfiles/tok1"}`)
	mux.HandleFunc("/cloudfiles/tok1", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(tmbcloudCloudfilesPage))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	withFakeSolver(t, &fakeSolver{token: "solved-token"})

	res, err := Resolve(context.Background(), srv.URL+"/download/OHWR5J")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if res.URL != "https://pub-abc123.r2.dev/Governor.2026.2160p.mkv?token=tok1" {
		t.Fatalf("unexpected url %q", res.URL)
	}
	if !strings.Contains(res.Headers["Referer"], "/cloudfiles/tok1") {
		t.Fatalf("referer should point at the hop page, got %q", res.Headers["Referer"])
	}
}

// When the resolved URL's own basename doesn't look like a real filename
// (no recognized extension, no path), the resolver should fall back to the
// page's <h2> title rather than leaving Filename empty.
func TestResolveTMBCloudFallsBackToTitleWithoutURLExt(t *testing.T) {
	t.Setenv("TMBCLOUD_HOSTS", "127.0.0.1")
	t.Setenv("TMBCLOUD_LINK_TYPE", "r2")
	mux := newTMBCloudMux(t, "r2", `{"url":"/cloudfiles/tok2"}`)
	mux.HandleFunc("/cloudfiles/tok2", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body>
<button class="btn-copy" data-url="https://r2.tmbcloud.lol/?token=abc123def456">Copy link</button>
</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	withFakeSolver(t, &fakeSolver{token: "solved-token"})

	res, err := Resolve(context.Background(), srv.URL+"/download/OHWR5J")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if res.Filename != "Governor..2026.2160p.AMZN.WEB-DL.D..UAL..DDP5.1.H.265-WildFire.mkv" {
		t.Fatalf("unexpected filename %q", res.Filename)
	}
}

// When the resolved URL's own basename doesn't name a file (no recognized
// extension), the resolver should ask the final URL for its
// Content-Disposition filename before falling back to the obfuscated page
// title. Confirmed live: FastCDN V2's googleusercontent links carry the
// clean name there.
func TestResolveTMBCloudFilenameFromContentDisposition(t *testing.T) {
	t.Setenv("TMBCLOUD_HOSTS", "127.0.0.1")
	mux := newTMBCloudMux(t, "fastcdnv2", `{"url":"/cloudfilesv2/pdoQmIauVLSxLg"}`)
	mux.HandleFunc("/cloudfilesv2/pdoQmIauVLSxLg", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body><script>var CODE = 'H5-V8ormynPhcQ';</script></body></html>`))
	})
	mux.HandleFunc("/cloudfilesv2-worker.php", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ready":true}`))
	})
	var srv *httptest.Server
	headSeen := false
	mux.HandleFunc("/cloudfilesv2/H5-V8ormynPhcQ", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"success":true,"downloadUrl":"%s/blob"}`, srv.URL)
	})
	mux.HandleFunc("/blob", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			http.Error(w, "expected HEAD", http.StatusMethodNotAllowed)
			return
		}
		headSeen = true
		w.Header().Set("Content-Disposition", `attachment; filename="Governor.2026.2160p.AMZN.WEB-DL.DUAL.DDP5.1.H.265-WildFire.mkv"`)
		w.WriteHeader(http.StatusOK)
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	withFakeSolver(t, &fakeSolver{token: "solved-token"})

	res, err := Resolve(context.Background(), srv.URL+"/download/OHWR5J")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if !headSeen {
		t.Fatal("Content-Disposition HEAD was never issued")
	}
	if res.Filename != "Governor.2026.2160p.AMZN.WEB-DL.DUAL.DDP5.1.H.265-WildFire.mkv" {
		t.Fatalf("unexpected filename %q", res.Filename)
	}
}

// The cloudfilesv2 worker can report an error instead of becoming ready
// (e.g. file pulled from the CDN); surface it instead of timing out.
func TestResolveTMBCloudWorkerError(t *testing.T) {
	t.Setenv("TMBCLOUD_HOSTS", "127.0.0.1")
	mux := newTMBCloudMux(t, "fastcdnv2", `{"url":"/cloudfilesv2/badcode"}`)
	mux.HandleFunc("/cloudfilesv2/badcode", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body><script>var CODE = 'XX-broken';</script></body></html>`))
	})
	mux.HandleFunc("/cloudfilesv2-worker.php", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"error":"link expired"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	withFakeSolver(t, &fakeSolver{token: "solved-token"})

	_, err := Resolve(context.Background(), srv.URL+"/download/OHWR5J")
	if err == nil {
		t.Fatal("expected error when worker reports failure")
	}
	if !strings.Contains(err.Error(), "link expired") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveTMBCloudLinkTypeOverride(t *testing.T) {
	t.Setenv("TMBCLOUD_HOSTS", "127.0.0.1")
	t.Setenv("TMBCLOUD_LINK_TYPE", "fastcdnv2")
	mux := newTMBCloudMux(t, "fastcdnv2", `{"url":"https://cdn.tmbcloud.lol/file/abc/Governor.mkv"}`)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	withFakeSolver(t, &fakeSolver{token: "solved-token"})

	res, err := Resolve(context.Background(), srv.URL+"/download/OHWR5J")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if res.URL != "https://cdn.tmbcloud.lol/file/abc/Governor.mkv" {
		t.Fatalf("unexpected url %q", res.URL)
	}
}

func TestResolveTMBCloudSolverFailure(t *testing.T) {
	t.Setenv("TMBCLOUD_HOSTS", "127.0.0.1")
	mux := newTMBCloudMux(t, "r2", `{"url":"https://r2.tmbcloud.lol/file/abc/Governor.mkv"}`)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	withFakeSolver(t, &fakeSolver{err: fmt.Errorf("capsolver: insufficient balance")})

	_, err := Resolve(context.Background(), srv.URL+"/download/OHWR5J")
	if err == nil {
		t.Fatal("expected error when solver fails")
	}
	if !strings.Contains(err.Error(), "insufficient balance") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveTMBCloudGenLinkRejectsBadToken(t *testing.T) {
	t.Setenv("TMBCLOUD_HOSTS", "127.0.0.1")
	mux := newTMBCloudMux(t, "fastcdnv2", `{"url":"https://r2.tmbcloud.lol/file/abc/Governor.mkv"}`)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Simulates a still-invalid/expired token: gen-link.php's real behavior
	// (confirmed live) is a 403 {"error":"Access denied"}.
	withFakeSolver(t, &fakeSolver{token: "wrong-token"})

	_, err := Resolve(context.Background(), srv.URL+"/download/OHWR5J")
	if err == nil {
		t.Fatal("expected error for rejected turnstile token")
	}
	if !strings.Contains(err.Error(), "Access denied") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveTMBCloudDeadFile(t *testing.T) {
	t.Setenv("TMBCLOUD_HOSTS", "127.0.0.1")
	mux := http.NewServeMux()
	mux.HandleFunc("/download/DEAD", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body>The file you are trying to download is no longer available!</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	withFakeSolver(t, &fakeSolver{token: "solved-token"})

	_, err := Resolve(context.Background(), srv.URL+"/download/DEAD")
	if err == nil {
		t.Fatal("dead file must fail")
	}
	if !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveTMBCloudMissingLayout(t *testing.T) {
	t.Setenv("TMBCLOUD_HOSTS", "127.0.0.1")
	mux := http.NewServeMux()
	mux.HandleFunc("/download/BROKEN", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body>totally different page, no page_id/token_a/sitekey here</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	withFakeSolver(t, &fakeSolver{token: "solved-token"})

	_, err := Resolve(context.Background(), srv.URL+"/download/BROKEN")
	if err == nil {
		t.Fatal("expected error when layout markers are missing")
	}
	if !strings.Contains(err.Error(), "layout may have changed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCapSolverConfigured(t *testing.T) {
	t.Setenv("CAPSOLVER_API_KEY", "")
	if newCapSolverFromEnv().Configured() {
		t.Fatal("must not be configured with empty key")
	}
	t.Setenv("CAPSOLVER_API_KEY", "abc123")
	if !newCapSolverFromEnv().Configured() {
		t.Fatal("must be configured with a key set")
	}
}

func TestTMBCloudLinkTypeDefault(t *testing.T) {
	t.Setenv("TMBCLOUD_LINK_TYPE", "")
	if got := tmbcloudLinkType(); got != "fastcdnv2" {
		t.Fatalf("default link type = %q, want fastcdnv2", got)
	}
	t.Setenv("TMBCLOUD_LINK_TYPE", "fslv2new")
	if got := tmbcloudLinkType(); got != "fslv2new" {
		t.Fatalf("override link type = %q, want fslv2new", got)
	}
}
