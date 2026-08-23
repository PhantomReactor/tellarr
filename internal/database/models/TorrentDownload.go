package models

import "time"

type DownloadState string

const (
	StateDownloading DownloadState = "downloading"
	StatePaused      DownloadState = "paused"
	StateDone        DownloadState = "done"
	StateError       DownloadState = "error"
	StateRemote      DownloadState = "remote"
)

type DownloadOrigin string

const (
	OriginTelegram   DownloadOrigin = "telegram"
	OriginExternalQb DownloadOrigin = "external_qbit"
	OriginAria2      DownloadOrigin = "aria2"
)

type TorrentDownload struct {
	ID          string         `db:"id"`
	SessionId   int64          `db:"session_id"`
	DialogId    int64          `db:"dialog_id"`
	MessageId   int64          `db:"message_id"`
	Filename    string         `db:"filename"`
	Total       int64          `db:"total"`
	Written     int64          `db:"written"`
	State       DownloadState  `db:"state"`
	Origin      DownloadOrigin `db:"origin"`
	Category    string         `db:"category"`
	SavePath    string         `db:"save_path"`
	ContentPath string         `db:"content_path"`
	Error       string         `db:"error"`
	RemoteGid   string         `db:"remote_gid"`
	SourceURL   string         `db:"source_url"`
	CreatedAt   time.Time      `db:"created_at"`
	UpdatedAt   time.Time      `db:"updated_at"`
}

func (d *TorrentDownload) Percent() float64 {
	if d.Total <= 0 {
		return 0
	}
	p := (float64(d.Written) / float64(d.Total)) * 100
	if p > 100 {
		p = 100
	}
	return p
}
