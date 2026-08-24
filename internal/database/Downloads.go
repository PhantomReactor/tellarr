package database

import (
	"database/sql"
	"errors"
	"tellarr/internal/database/models"

	"github.com/jmoiron/sqlx"
)

type DownloadsRepository struct {
	db *sqlx.DB
}

func NewDownloadsRepository(db *sqlx.DB) DownloadsRepository {
	return DownloadsRepository{db: db}
}

func (r *DownloadsRepository) Create(d models.TorrentDownload) error {
	d.CreatedAt = d.CreatedAt.UTC()
	d.UpdatedAt = d.UpdatedAt.UTC()
	_, err := r.db.NamedExec(`INSERT INTO DOWNLOADS (id, session_id, dialog_id, message_id, filename, total, written, state, origin, category, save_path, content_path, error, remote_gid, source_url, created_at, updated_at)
	VALUES (:id, :session_id, :dialog_id, :message_id, :filename, :total, :written, :state, :origin, :category, :save_path, :content_path, :error, :remote_gid, :source_url, :created_at, :updated_at)
	ON CONFLICT(id) DO UPDATE SET
		session_id = excluded.session_id,
		dialog_id = excluded.dialog_id,
		message_id = excluded.message_id,
		filename = excluded.filename,
		total = excluded.total,
		written = excluded.written,
		state = excluded.state,
		origin = excluded.origin,
		category = excluded.category,
		save_path = excluded.save_path,
		content_path = excluded.content_path,
		error = excluded.error,
		remote_gid = excluded.remote_gid,
		source_url = excluded.source_url,
		updated_at = excluded.updated_at`, d)
	return err
}

func (r *DownloadsRepository) UpdateProgress(id string, written int64, state models.DownloadState, errMsg string) error {
	_, err := r.db.Exec(`UPDATE DOWNLOADS SET written = ?, state = ?, error = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		written, state, errMsg, id)
	return err
}

func (r *DownloadsRepository) SetState(id string, state models.DownloadState) error {
	_, err := r.db.Exec(`UPDATE DOWNLOADS SET state = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, state, id)
	return err
}

// UpdateAriaProgress persists the full status of an aria2-backed download.
func (r *DownloadsRepository) UpdateAriaProgress(id string, written, total int64, state models.DownloadState, contentPath, filename, errMsg string) error {
	_, err := r.db.Exec(`UPDATE DOWNLOADS SET written = ?, total = ?, state = ?, content_path = ?, filename = ?, error = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		written, total, state, contentPath, filename, errMsg, id)
	return err
}

// SetRemoteGid stores the aria2 gid for an in-flight external download.
func (r *DownloadsRepository) SetRemoteGid(id, gid string) error {
	_, err := r.db.Exec(`UPDATE DOWNLOADS SET remote_gid = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, gid, id)
	return err
}

// SetCategory re-labels a download (the arrs use torrents/setCategory to keep
// their grabs separated by category).
func (r *DownloadsRepository) SetCategory(id, category string) error {
	_, err := r.db.Exec(`UPDATE DOWNLOADS SET category = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, category, id)
	return err
}

func (r *DownloadsRepository) Get(id string) (*models.TorrentDownload, error) {
	var d models.TorrentDownload
	err := r.db.Get(&d, "SELECT * FROM DOWNLOADS WHERE id = ?", id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &d, nil
}

func (r *DownloadsRepository) List() ([]models.TorrentDownload, error) {
	var out []models.TorrentDownload
	err := r.db.Select(&out, "SELECT * FROM DOWNLOADS ORDER BY created_at DESC")
	return out, err
}

func (r *DownloadsRepository) ListActive() ([]models.TorrentDownload, error) {
	var out []models.TorrentDownload
	err := r.db.Select(&out, `SELECT * FROM DOWNLOADS WHERE state IN (?, ?)`, models.StateDownloading, models.StatePaused)
	return out, err
}

func (r *DownloadsRepository) Delete(id string) error {
	_, err := r.db.Exec(`DELETE FROM DOWNLOADS WHERE id = ?`, id)
	return err
}
