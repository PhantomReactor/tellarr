package database

import (
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"
)

// SettingsRepository persists simple runtime-tunable key/value settings
// edited from the web UI.
type SettingsRepository struct {
	db *sqlx.DB
}

func NewSettingsRepository(db *sqlx.DB) SettingsRepository {
	return SettingsRepository{db: db}
}

// Get returns the stored value for key, or nil when unset.
func (r *SettingsRepository) Get(key string) (*string, error) {
	var value string
	if err := r.db.Get(&value, "select value from settings where key = ?", key); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &value, nil
}

// Set upserts the value for key.
func (r *SettingsRepository) Set(key, value string) error {
	_, err := r.db.Exec(`insert into settings (key, value) values (?, ?)
		on conflict (key) do update set value = excluded.value`, key, value)
	return err
}
