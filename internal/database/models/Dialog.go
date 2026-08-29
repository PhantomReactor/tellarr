package models

import "time"

type Dialog struct {
	ID          int    `db:"id"`
	SessionId   int64  `db:"session_id"`
	PhoneNumber string `db:"phone_number"`
	Name        string `db:"name"`
	Type        string `db:"type"`
	DialogId    int64  `db:"dialog_id"`
	AccessHash  int64  `db:"access_hash"`
	Indexer     bool   `db:"indexer"`
	// Categories is the comma-separated torznabcats.Category keys (e.g.
	// "audio" or "movies,tv") this channel was registered under — see
	// torznabcats.ParseKeys/JoinKeys. Empty means "never chosen", which
	// falls back to torznabcats.DefaultKey.
	Categories string    `db:"categories"`
	Active     bool      `db:"active"`
	CreatedAt  time.Time `db:"created_at"`
	UpdatedAt  time.Time `db:"updated_at"`
}
