// Package torznabcats is the single source of truth for the Torznab/Newznab
// category groups Tellarr offers — both the <categories> block advertised
// by the Torznab caps endpoint and the category picker shown on the
// Indexers page when registering a channel with Prowlarr. Ids follow the
// standard Newznab numbering that Sonarr/Radarr/Lidarr/Readarr/Prowlarr all
// recognize.
package torznabcats

// Category is one selectable category group: a parent id plus a few
// representative subcategory ids (for the types where a common convention
// exists), so results survive category-intersection filtering in the *Arr
// apps regardless of which subcategory they actually picked.
type Category struct {
	Key   string // form value / map key, e.g. "movies"
	Title string // display title, e.g. "Movies"
	IDs   []int  // parent id first, then subcat ids
}

// All lists every category Tellarr offers, in standard Newznab numeric
// order (1000 Console .. 8000 Other).
var All = []Category{
	{Key: "console", Title: "Console", IDs: []int{1000}},
	{Key: "movies", Title: "Movies", IDs: []int{2000, 2030, 2040}},
	{Key: "audio", Title: "Audio", IDs: []int{3000, 3010, 3040}},
	{Key: "pc", Title: "PC/Software", IDs: []int{4000}},
	{Key: "tv", Title: "TV", IDs: []int{5000, 5030, 5040}},
	{Key: "xxx", Title: "XXX", IDs: []int{6000}},
	{Key: "books", Title: "Books", IDs: []int{7000}},
	{Key: "other", Title: "Other", IDs: []int{8000}},
}

// DefaultKey is used when a caller submits an empty or unrecognized key.
const DefaultKey = "movies"

// IDsFor resolves a category key to its Torznab category ids, falling back
// to DefaultKey's ids for an empty/unknown key.
func IDsFor(key string) []int {
	for _, c := range All {
		if c.Key == key {
			return c.IDs
		}
	}
	for _, c := range All {
		if c.Key == DefaultKey {
			return c.IDs
		}
	}
	return All[0].IDs
}

// IDsForKeys resolves multiple category keys — an indexer can legitimately
// belong to more than one (e.g. a channel with both movies and TV) — to a
// combined, deduplicated list of Torznab category ids, in All's order.
// Unknown keys are ignored; if none of the keys resolve, falls back to
// DefaultKey's ids so callers always get a non-empty result.
func IDsForKeys(keys []string) []int {
	want := make(map[string]bool, len(keys))
	for _, k := range keys {
		want[k] = true
	}
	var out []int
	seen := make(map[int]bool)
	for _, c := range All {
		if !want[c.Key] {
			continue
		}
		for _, id := range c.IDs {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	if len(out) == 0 {
		return IDsFor(DefaultKey)
	}
	return out
}
