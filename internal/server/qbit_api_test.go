package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Prowlarr's qBittorrent client test creates the configured category and then
// reads /torrents/categories back; if the category is missing it fails with
// "Configuration of label failed".
func TestQBitCategoryRoundTrip(t *testing.T) {
	s := &Server{}

	form := url.Values{"category": {"prowlarr"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v2/torrents/createCategory", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.qbCreateCategory(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("createCategory status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v2/torrents/categories", nil)
	s.qbGetCategories(rec, req)
	if !strings.Contains(rec.Body.String(), `"prowlarr"`) {
		t.Fatalf("categories missing prowlarr: %s", rec.Body.String())
	}

	form = url.Values{"categories": {"prowlarr"}}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v2/torrents/deleteCategory", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.qbDeleteCategory(rec, req)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v2/torrents/categories", nil)
	s.qbGetCategories(rec, req)
	if strings.Contains(rec.Body.String(), "prowlarr") {
		t.Fatalf("prowlarr should be deleted: %s", rec.Body.String())
	}
}
