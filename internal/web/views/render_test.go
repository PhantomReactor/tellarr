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

func TestLoginPageRegisterLink(t *testing.T) {
	var open, closed strings.Builder
	if err := LoginPage("", true).Render(t.Context(), &open); err != nil {
		t.Fatal(err)
	}
	if err := LoginPage("boom", false).Render(t.Context(), &closed); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(open.String(), `href="/ui/register"`) {
		t.Error("register link should render when registration is open")
	}
	if strings.Contains(closed.String(), `href="/ui/register"`) {
		t.Error("register link should be hidden when a user already exists")
	}
}

func TestRegisterPageClosed(t *testing.T) {
	var sb strings.Builder
	if err := RegisterPage("", false).Render(t.Context(), &sb); err != nil {
		t.Fatal(err)
	}
	out := sb.String()
	if strings.Contains(out, `action="/ui/register"`) {
		t.Error("register form should not render when registration is closed")
	}
	if !strings.Contains(out, "Registration is closed") {
		t.Error("closed registration notice missing")
	}
}

func TestAccountPageRenders(t *testing.T) {
	var sb strings.Builder
	if err := AccountPage("admin", "password updated", "").Render(t.Context(), &sb); err != nil {
		t.Fatal(err)
	}
	out := sb.String()
	for _, want := range []string{
		`action="/ui/account/password"`, `name="current_password"`,
		`name="new_password"`, `minlength="6"`, `<strong>admin</strong>`,
		`flash auto-dismiss ok-msg`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in account page", want)
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
