package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ProwlarrClient talks to a Prowlarr instance over its REST API.
type ProwlarrClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func NewProwlarrClientFromEnv() *ProwlarrClient {
	return &ProwlarrClient{
		baseURL: strings.TrimRight(strings.TrimSpace(os.Getenv("PROWLARR_URL")), "/"),
		apiKey:  strings.TrimSpace(os.Getenv("PROWLARR_API_KEY")),
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (p *ProwlarrClient) Configured() bool { return p.baseURL != "" && p.apiKey != "" }

// Torznab category id sets, matching the <categories> block advertised in
// writeCaps. Callers pick between these (see inferProwlarrCategories) so a
// channel gets registered under a category Sonarr/Radarr/Lidarr will
// actually query against, instead of always defaulting to Movies/TV.
var (
	ProwlarrMovieTVCategories = []map[string]any{{"id": 2000}, {"id": 2030}, {"id": 2040}, {"id": 5000}, {"id": 5030}, {"id": 5040}}
	ProwlarrAudioCategories   = []map[string]any{{"id": 3000}, {"id": 3010}, {"id": 3040}}
)

type prowField struct {
	Name  string `json:"name"`
	Value any    `json:"value,omitempty"`
}

func (p *ProwlarrClient) do(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+"/api/v1/"+strings.TrimPrefix(path, "/"), rd)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("X-Api-Key", p.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

func setProwlarrField(fields []prowField, name string, value any) []prowField {
	for i := range fields {
		if fields[i].Name == name {
			fields[i].Value = value
			return fields
		}
	}
	return append(fields, prowField{Name: name, Value: value})
}

// firstAppProfileID returns the app profile to attach to new indexers.
// Prowlarr rejects indexer resources whose appProfileId is 0, so a real one
// must be resolved: PROWLARR_APP_PROFILE_ID if set, else the first profile
// from /api/v1/appProfile, else 1 (Prowlarr's default "Standard" profile).
func (p *ProwlarrClient) firstAppProfileID(ctx context.Context) int {
	if v := strings.TrimSpace(os.Getenv("PROWLARR_APP_PROFILE_ID")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	data, _, err := p.do(ctx, http.MethodGet, "appProfile", nil)
	if err == nil {
		var profiles []struct {
			ID int `json:"id"`
		}
		if json.Unmarshal(data, &profiles) == nil && len(profiles) > 0 && profiles[0].ID > 0 {
			return profiles[0].ID
		}
	}
	return 1
}

// AddTorznabIndexer creates (or updates) a Generic Torznab indexer in
// Prowlarr pointing at one of our feeds. Feed URL must look like
// {base}/torznab/{channel}/api?apikey={key}. categories is the set of
// Torznab category ids Prowlarr should list (and default-query) for this
// indexer — see inferProwlarrCategories for how callers pick it.
func (p *ProwlarrClient) AddTorznabIndexer(ctx context.Context, name, feedURL string, categories []map[string]any) error {
	fu, err := url.Parse(feedURL)
	if err != nil {
		return fmt.Errorf("bad feed url: %w", err)
	}
	q := fu.Query()
	apikey := q.Get("apikey")

	schemaData, _, err := p.do(ctx, http.MethodGet, "indexer/schema", nil)
	if err != nil {
		return fmt.Errorf("fetching schema: %w", err)
	}
	var schema []struct {
		Implementation string      `json:"implementation"`
		ConfigContract string      `json:"configContract"`
		Fields         []prowField `json:"fields"`
	}
	if err := json.Unmarshal(schemaData, &schema); err != nil {
		return fmt.Errorf("decoding schema: %w", err)
	}
	var tpl *struct {
		Implementation string      `json:"implementation"`
		ConfigContract string      `json:"configContract"`
		Fields         []prowField `json:"fields"`
	}
	for i := range schema {
		if schema[i].ConfigContract == "TorznabSettings" || schema[i].Implementation == "Torznab" {
			tpl = &schema[i]
			break
		}
	}
	if tpl == nil {
		return fmt.Errorf("Generic Torznab indexer not found in Prowlarr schema")
	}

	if len(categories) == 0 {
		categories = ProwlarrMovieTVCategories
	}
	fields := append([]prowField{}, tpl.Fields...)
	fields = setProwlarrField(fields, "baseUrl", fu.Scheme+"://"+fu.Host)
	fields = setProwlarrField(fields, "apiPath", fu.EscapedPath())
	fields = setProwlarrField(fields, "apiKey", apikey)
	fields = setProwlarrField(fields, "categories", categories)

	// Existing indexer with the same name -> update it, else create.
	listData, _, err := p.do(ctx, http.MethodGet, "indexer", nil)
	if err != nil {
		return fmt.Errorf("listing indexers: %w", err)
	}
	var existing []map[string]any
	_ = json.Unmarshal(listData, &existing)
	for _, res := range existing {
		if n, _ := res["name"].(string); strings.EqualFold(n, name) {
			res["fields"] = fields
			res["enable"] = true
			id, _ := res["id"].(float64)
			if _, _, err := p.do(ctx, http.MethodPut, fmt.Sprintf("indexer/%d", int64(id)), res); err != nil {
				return fmt.Errorf("updating indexer: %w", err)
			}
			return nil
		}
	}

	payload := map[string]any{
		"name":           name,
		"enable":         true,
		"implementation": tpl.Implementation,
		"configContract": tpl.ConfigContract,
		"appProfileId":   p.firstAppProfileID(ctx),
		"priority":       25,
		"tags":           []any{},
		"fields":         fields,
	}
	data, status, err := p.do(ctx, http.MethodPost, "indexer", payload)
	if err != nil {
		return fmt.Errorf("adding indexer: %w", err)
	}
	if status >= 400 {
		return fmt.Errorf("adding indexer failed (%d): %s", status, truncateRunes(string(data), 300))
	}
	return nil
}

// WriteProwlarrDefinition writes a Cardigann YML torznab wrapper for the feed
// into Prowlarr's custom definitions folder (<AppData>/Definitions/Custom).
// Prowlarr picks it up after a restart, then the indexer shows up under
// Settings > Indexers > Add Indexer > Custom.
func WriteProwlarrDefinition(dir, channel, feedURL string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		dir = filepath.Join(os.Getenv("PROWLARR_CONFIG_DIR"), "Definitions", "Custom")
	}
	defID := "tellarr-" + slugify(channel)
	path := filepath.Join(dir, defID+".yml")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	yaml := TorznabCardigannYAML(defID, "Tellarr - "+channel, channel, feedURL)
	if err := os.WriteFile(path, yaml, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func slugify(s string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "channel"
	}
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}

// TorznabCardigannYAML renders a minimal Cardigann torznab definition that
// proxies one Tellarr feed. Only the apikey is user-configurable in Prowlarr;
// everything else is baked in.
//
// Constraints of Prowlarr/Jackett's Cardigann engine that shape this output:
//   - template conditionals REQUIRE an {{else}} branch, so we never use if/end
//     and instead always send q/season/ep params (tellarr treats empty as absent)
//   - the category field must output a TRACKER id from categorymappings, so we
//     register one synthetic tracker id "all" mapped to every newznab category
//   - path-level "categories" are matched against tracker ids too, so they are
//     omitted entirely (a mismatch silently skips the search path)
func TorznabCardigannYAML(id, name, channel, feedURL string) []byte {
	base := ""
	apiPath := "/"
	apikey := ""
	if fu, err := url.Parse(feedURL); err == nil && fu.Host != "" {
		base = fu.Scheme + "://" + fu.Host
		apiPath = fu.EscapedPath()
		apikey = fu.Query().Get("apikey")
	}
	pathLine := apiPath +
		"?apikey={{ .Config.apikey }}" +
		"&t={{ .Query.Type }}" +
		"&q={{ .Query.Q }}" +
		"&season={{ .Query.Season }}" +
		"&ep={{ .Query.Episode }}"

	var b strings.Builder
	fmt.Fprintf(&b, "---\n")
	fmt.Fprintf(&b, "id: %s\n", id)
	// Quoted so channel names with unicode, colons or other YAML-hostile
	// characters survive Prowlarr's Cardigann parser.
	fmt.Fprintf(&b, "name: %s\n", yamlQuote(name))
	fmt.Fprintf(&b, "description: %s\n", yamlQuote(fmt.Sprintf("Tellarr Torznab proxy for Telegram channel '%s'", channel)))
	fmt.Fprintf(&b, "type: torznab\n")
	fmt.Fprintf(&b, "language: en-us\n")
	fmt.Fprintf(&b, "encoding: utf-8\n")
	fmt.Fprintf(&b, "followredirect: true\n")
	fmt.Fprintf(&b, "links:\n  - %s/\n", base)
	fmt.Fprintf(&b, "caps:\n")
	fmt.Fprintf(&b, "  categorymappings:\n")
	for _, cat := range []string{"Movies", "Movies/HD", "Movies/SD", "TV", "TV/HD", "TV/SD"} {
		fmt.Fprintf(&b, "    - {id: all, cat: %s}\n", cat)
	}
	fmt.Fprintf(&b, "  modes:\n")
	fmt.Fprintf(&b, "    search: [q]\n")
	fmt.Fprintf(&b, "    movie-search: [q]\n")
	fmt.Fprintf(&b, "    tv-search: [q, season, ep]\n")
	fmt.Fprintf(&b, "settings:\n")
	fmt.Fprintf(&b, "  - name: apikey\n")
	fmt.Fprintf(&b, "    type: text\n")
	fmt.Fprintf(&b, "    label: ApiKey\n")
	if apikey != "" {
		fmt.Fprintf(&b, "    default: \"%s\"\n", apikey)
	}
	fmt.Fprintf(&b, "search:\n")
	fmt.Fprintf(&b, "  paths:\n")
	fmt.Fprintf(&b, "    - path: \"%s\"\n", pathLine)
	fmt.Fprintf(&b, "      method: get\n")
	fmt.Fprintf(&b, "      response:\n")
	fmt.Fprintf(&b, "        type: xml\n")
	fmt.Fprintf(&b, "  rows:\n")
	fmt.Fprintf(&b, "    selector: item\n")
	fmt.Fprintf(&b, "  fields:\n")
	fmt.Fprintf(&b, "    title:\n")
	fmt.Fprintf(&b, "      selector: title\n")
	fmt.Fprintf(&b, "    download:\n")
	fmt.Fprintf(&b, "      selector: enclosure\n")
	fmt.Fprintf(&b, "      attribute: url\n")
	fmt.Fprintf(&b, "    infohash:\n")
	fmt.Fprintf(&b, "      selector: guid\n")
	fmt.Fprintf(&b, "    date:\n")
	fmt.Fprintf(&b, "      selector: pubDate\n")
	fmt.Fprintf(&b, "    size:\n")
	fmt.Fprintf(&b, "      selector: enclosure\n")
	fmt.Fprintf(&b, "      attribute: length\n")
	fmt.Fprintf(&b, "    seeders:\n")
	fmt.Fprintf(&b, "      text: \"1\"\n")
	fmt.Fprintf(&b, "    peers:\n")
	fmt.Fprintf(&b, "      text: \"1\"\n")
	fmt.Fprintf(&b, "    downloadvolumefactor:\n")
	fmt.Fprintf(&b, "      text: \"0\"\n")
	fmt.Fprintf(&b, "    uploadvolumefactor:\n")
	fmt.Fprintf(&b, "      text: \"1\"\n")
	fmt.Fprintf(&b, "    category:\n")
	fmt.Fprintf(&b, "      text: \"all\"\n")
	return []byte(b.String())
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

// yamlQuote renders s as a double-quoted YAML scalar, escaping backslashes
// and double quotes so arbitrary channel names parse cleanly.
func yamlQuote(s string) string {
	q := strings.ReplaceAll(s, `\`, `\\`)
	q = strings.ReplaceAll(q, `"`, `\"`)
	return `"` + q + `"`
}
