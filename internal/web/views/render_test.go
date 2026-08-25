package views

import (
	"strings"
	"testing"
)

func TestDownloadsPageRenders(t *testing.T) {
	rows := []DownloadRowVM{
		{ID: "abc", Name: "Show.S01E01.mkv", State: "downloading", Percent: 42.5, Origin: "telegram", Category: "tv", Written: 100, Total: 200, Speed: 1024, ETA: 60},
		{ID: "def", Name: "Movie.mkv", State: "done", Percent: 100, Origin: "qbittorrent"},
	}
	var sb strings.Builder
	if err := DownloadsPage(rows, "download started", "").Render(t.Context(), &sb); err != nil {
		t.Fatal(err)
	}
	out := sb.String()
	for _, want := range []string{
		`id="modal-add-download"`, `id="modal-delete"`, `id="del-form-record"`,
		`data-target="downloads-table"`, `data-search="show.s01e01.mkv tv telegram downloading"`,
		`askDelete(this, event)`, `data-del-id="abc"`, `flash auto-dismiss ok-msg`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in downloads page", want)
		}
	}
}

func TestIndexersPageRenders(t *testing.T) {
	channels := []ChannelVM{{Name: "Movies & TV", IsIndex: true}, {Name: "Stuff", IsIndex: false}}
	var sb strings.Builder
	if err := IndexersPage(channels, "", "boom").Render(t.Context(), &sb); err != nil {
		t.Fatal(err)
	}
	out := sb.String()
	for _, want := range []string{
		`id="modal-yml"`, `id="yml-viewer"`, `data-target="indexers-table"`,
		`data-search="movies &amp; tv enabled"`, `data-search="stuff disabled"`,
		`indexers-table-noresults`, `flash auto-dismiss error-msg`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in indexers page", want)
		}
	}
}

func TestEmptyStatesRender(t *testing.T) {
	var sb strings.Builder
	if err := DownloadsPage(nil, "", "").Render(t.Context(), &sb); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sb.String(), `data-target="downloads-table"`) {
		t.Error("search box should not render when there are no rows")
	}
	if err := IndexersPage(nil, "", "").Render(t.Context(), &sb); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sb.String(), `id="modal-yml"`) {
		t.Error("yml modal should not render when there are no channels")
	}
}
