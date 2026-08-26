package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type fakeProwlarr struct {
	mu       sync.Mutex
	methods  []string
	bodies   []map[string]any
	indexers []map[string]any
}

func (f *fakeProwlarr) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("X-Api-Key") != "secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		f.methods = append(f.methods, r.Method+" "+r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/indexer/schema"):
			w.Write([]byte(`[
				{"implementation":"Cardigann","configContract":"CardigannSettings","fields":[]},
				{"implementation":"Torznab","configContract":"TorznabSettings","fields":[
					{"name":"baseUrl","value":""},
					{"name":"apiPath","value":"/api"},
					{"name":"apiKey","value":""},
					{"name":"categories","value":[{"id":9000}]}
				]}
			]`))
		case strings.HasSuffix(r.URL.Path, "/appProfile") && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode([]map[string]any{{"id": 3, "name": "Standard"}})
		case r.URL.Path == "/api/v1/indexer" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(f.indexers)
		case r.URL.Path == "/api/v1/indexer" && r.Method == http.MethodPost:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("bad post body: %v", err)
			}
			f.bodies = append(f.bodies, body)
			f.indexers = append(f.indexers, map[string]any{"id": float64(len(f.indexers) + 1), "name": body["name"]})
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte("{}"))
		case strings.HasPrefix(r.URL.Path, "/api/v1/indexer/") && r.Method == http.MethodPut:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("bad put body: %v", err)
			}
			f.bodies = append(f.bodies, body)
			w.Write([]byte("{}"))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}
}

func TestProwlarrAddTorznabCreatesThenUpdates(t *testing.T) {
	fake := &fakeProwlarr{}
	srv := httptest.NewServer(fake.handler(t))
	defer srv.Close()
	p := NewProwlarrClientFromEnv()
	p.baseURL = srv.URL
	p.apiKey = "secret"

	feed := "http://tellarr.local:8080/torznab/My%20Channel/api?apikey=tell123"
	if err := p.AddTorznabIndexer(context.Background(), "Tellarr - My Channel", feed); err != nil {
		t.Fatalf("add failed: %v", err)
	}
	if len(fake.bodies) != 1 || fake.bodies[0]["name"] != "Tellarr - My Channel" {
		t.Fatalf("expected one create, got %+v", fake.bodies)
	}
	if id, _ := fake.bodies[0]["appProfileId"].(float64); id != 3 {
		t.Errorf("appProfileId = %v, want 3", fake.bodies[0]["appProfileId"])
	}
	fields := fake.bodies[0]["fields"].([]any)
	get := func(n string) any {
		for _, f := range fields {
			m := f.(map[string]any)
			if m["name"] == n {
				return m["value"]
			}
		}
		return nil
	}
	if get("baseUrl") != "http://tellarr.local:8080" {
		t.Errorf("baseUrl = %v", get("baseUrl"))
	}
	if get("apiPath") != "/torznab/My%20Channel/api" {
		t.Errorf("apiPath = %v", get("apiPath"))
	}
	if get("apiKey") != "tell123" {
		t.Errorf("apiKey = %v", get("apiKey"))
	}

	// Second add must update the existing entry, not create a duplicate.
	if err := p.AddTorznabIndexer(context.Background(), "Tellarr - My Channel", feed); err != nil {
		t.Fatalf("second add failed: %v", err)
	}
	puts := 0
	for _, m := range fake.methods {
		if strings.HasPrefix(m, "PUT ") {
			puts++
		}
	}
	if puts != 1 {
		t.Fatalf("expected exactly one PUT update, methods: %v", fake.methods)
	}
}

func TestTorznabCardigannYAMLContent(t *testing.T) {
	yml := string(TorznabCardigannYAML(
		"tellarr-mychannel",
		"Tellarr - MyChannel",
		"MyChannel",
		"http://base:8080/torznab/MyChannel/api?apikey=k1",
	))
	for _, want := range []string{
		"id: tellarr-mychannel",
		"type: torznab",
		`name: "Tellarr - MyChannel"`,
		"- name: apikey",
		"default: \"k1\"",
		"/torznab/MyChannel/api?apikey={{ .Config.apikey }}&t={{ .Query.Type }}&q={{ .Query.Q }}",
		"response:\n        type: xml",
		"selector: item",
		"{id: all, cat: Movies}",
		"{id: all, cat: TV/SD}",
		"movie-search: [q]",
		"tv-search: [q, season, ep]",
		"text: \"all\"",
	} {
		if !strings.Contains(yml, want) {
			t.Errorf("yaml missing %q\n%s", want, yml)
		}
	}
	for _, banned := range []string{
		"{{ if ", // Prowlarr's engine requires {{else}}, conditionals break silently
		"{{ end }}",
	} {
		if strings.Contains(yml, banned) {
			t.Errorf("yaml must not contain %q\n%s", banned, yml)
		}
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Hindi Movies 2024": "hindi-movies-2024",
		"  ##Weird!!Name##": "weird-name",
		"":                  "channel",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q want %q", in, got, want)
		}
	}
}
