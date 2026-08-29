// Package torznabcats is the single source of truth for the Torznab/Newznab
// category groups Tellarr offers — both the <categories> block advertised
// by the Torznab caps endpoint and the category picker shown on the
// Indexers page when registering a channel with Prowlarr. Ids follow the
// standard Newznab numbering that Sonarr/Radarr/Lidarr/Readarr/Prowlarr all
// recognize.
package torznabcats

import "strings"

// SubCat is one representative subcategory under a parent Category, e.g.
// Movies/HD under Movies.
type SubCat struct {
	ID    int
	Title string
}

// Category is one selectable category group: a parent id/title plus a few
// representative subcategory ids (for the types where a common convention
// exists), so results survive category-intersection filtering in the *Arr
// apps regardless of which subcategory they actually picked.
type Category struct {
	Key     string // form value / map key, e.g. "movies"
	ID      int    // parent Torznab category id
	Title   string // display title, e.g. "Movies"
	SubCats []SubCat
}

// IDs returns the parent id followed by every subcat id.
func (c Category) IDs() []int {
	ids := make([]int, 0, 1+len(c.SubCats))
	ids = append(ids, c.ID)
	for _, sc := range c.SubCats {
		ids = append(ids, sc.ID)
	}
	return ids
}

// All lists every category Tellarr offers, in standard Newznab numeric
// order (1000 Console .. 8000 Other).
// SubCat titles are bare leaf names (e.g. "MP3", "HD") per the Torznab/
// Newznab spec convention — Torznab clients (Prowlarr included) prefix the
// parent category's name themselves when displaying a subcat. A
// fully-qualified name here (e.g. "Audio/MP3") won't match the client's own
// canonical name for that id, so it gets treated as an unrecognized custom
// category instead of the standard one.
var All = []Category{
	{Key: "console", ID: 1000, Title: "Console"},
	{Key: "movies", ID: 2000, Title: "Movies", SubCats: []SubCat{{2030, "HD"}, {2040, "SD"}}},
	{Key: "audio", ID: 3000, Title: "Audio", SubCats: []SubCat{{3010, "MP3"}, {3040, "Lossless"}}},
	{Key: "pc", ID: 4000, Title: "PC/Software"},
	{Key: "tv", ID: 5000, Title: "TV", SubCats: []SubCat{{5030, "HD"}, {5040, "SD"}}},
	{Key: "xxx", ID: 6000, Title: "XXX"},
	{Key: "books", ID: 7000, Title: "Books"},
	{Key: "other", ID: 8000, Title: "Other"},
}

// DefaultKey is used when a caller submits an empty or unrecognized key.
const DefaultKey = "movies"

// Selected returns the Category entries matching keys, in All's canonical
// order. Unknown keys are ignored; if none of the keys resolve, falls back
// to a single-element slice for DefaultKey so callers always get a
// non-empty result.
func Selected(keys []string) []Category {
	want := make(map[string]bool, len(keys))
	for _, k := range keys {
		want[k] = true
	}
	var out []Category
	for _, c := range All {
		if want[c.Key] {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		for _, c := range All {
			if c.Key == DefaultKey {
				return []Category{c}
			}
		}
	}
	return out
}

// IDsFor resolves a single category key to its Torznab category ids,
// falling back to DefaultKey's ids for an empty/unknown key.
func IDsFor(key string) []int {
	return IDsForKeys([]string{key})
}

// IDsForKeys resolves multiple category keys — an indexer can legitimately
// belong to more than one (e.g. a channel with both movies and TV) — to a
// combined, deduplicated list of Torznab category ids, in All's order.
// Unknown keys are ignored; if none of the keys resolve, falls back to
// DefaultKey's ids so callers always get a non-empty result.
func IDsForKeys(keys []string) []int {
	var out []int
	seen := make(map[int]bool)
	for _, c := range Selected(keys) {
		for _, id := range c.IDs() {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

// ParseKeys splits a persisted "audio,movies" categories string (see
// JoinKeys) back into keys. Empty/blank input yields no keys — callers
// resolve that through Selected/IDsForKeys, which fall back to DefaultKey.
func ParseKeys(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// JoinKeys renders keys back to the persisted comma-separated form stored
// against a channel's Dialog row.
func JoinKeys(keys []string) string {
	return strings.Join(keys, ",")
}
