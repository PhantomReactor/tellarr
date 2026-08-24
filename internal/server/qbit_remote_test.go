package server

import "testing"

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
