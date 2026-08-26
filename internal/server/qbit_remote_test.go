package server

import (
	"crypto/sha1"
	"fmt"
	"testing"

	dbm "tellarr/internal/database/models"
)

func TestQBitUIState(t *testing.T) {
	cases := map[string]string{
		"downloading":   "downloading",
		"stalledDL":     "downloading",
		"forcedDL":      "downloading",
		"metaDL":        "downloading",
		"queuedDL":      "downloading",
		"moving":        "downloading",
		"pausedDL":      "paused",
		"stoppedDL":     "paused", // qBittorrent 5.x
		"uploading":     "done",
		"stalledUP":     "done",
		"pausedUP":      "done",
		"stoppedUP":     "done", // qBittorrent 5.x
		"completed":     "done",
		"error":         "error",
		"errored":       "error",
		"missingFiles":  "error",
		"somethingElse": "downloading",
	}
	for in, want := range cases {
		if got := qbitUIState(in); got != want {
			t.Errorf("qbitUIState(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRemoteTorrentVM(t *testing.T) {
	rt := RemoteTorrent{
		Hash:     "abc123",
		Name:     "Show.S01E01.mkv",
		Size:     1 << 30,
		Progress: 0.5,
		State:    "stalledDL",
		Category: "tv",
		DlSpeed:  2 << 20,
		Eta:      90,
	}
	vm := remoteTorrentVM(rt)
	if vm.State != "downloading" || vm.Origin != "qbittorrent" {
		t.Fatalf("unexpected state/origin: %s/%s", vm.State, vm.Origin)
	}
	if vm.Written != 1<<29 || vm.Percent != 50 {
		t.Fatalf("unexpected written/percent: %d/%v", vm.Written, vm.Percent)
	}
	if vm.Speed != rt.DlSpeed || vm.ETA != 90 {
		t.Fatalf("unexpected speed/eta: %d/%d", vm.Speed, vm.ETA)
	}

	// qBittorrent's "unknown" ETA sentinel must map to -1.
	rt.Eta = qBitEtaUnknown
	if got := remoteTorrentVM(rt).ETA; got != -1 {
		t.Errorf("unknown eta mapped to %d, want -1", got)
	}
}

func TestTorrentInfoHash(t *testing.T) {
	sample := []byte("d4:infod4:name5:hello6:lengthi5eed6:lengthi42e4:name3:tweee")
	want := fmt.Sprintf("%x", sha1.Sum([]byte("d4:name5:hello6:lengthi5ee")))
	if got := torrentInfoHash(sample); got != want {
		t.Errorf("torrentInfoHash = %q, want %q", got, want)
	}
	for _, bad := range [][]byte{nil, []byte("garbage"), []byte("d4:name5:helloe"), []byte("d3:foo")} {
		if got := torrentInfoHash(bad); got != "" {
			t.Errorf("torrentInfoHash(%q) = %q, want empty", bad, got)
		}
	}
}

func TestFilterKnownRemotes(t *testing.T) {
	local := []dbm.TorrentDownload{
		{ID: "aaa", Origin: dbm.OriginExternalQb},
		{ID: "bbb", Origin: dbm.OriginTelegram},
		{ID: "ddd", Origin: dbm.OriginAria2},
	}
	remotes := []RemoteTorrent{{Hash: "aaa"}, {Hash: "ccc"}}
	got := filterKnownRemotes(local, remotes)
	if len(got) != 1 || got[0].Hash != "aaa" {
		t.Fatalf("expected only aaa, got %+v", got)
	}
	if r := filterKnownRemotes(nil, remotes); len(r) != 0 {
		t.Fatalf("no recorded rows must filter everything, got %+v", r)
	}
}
