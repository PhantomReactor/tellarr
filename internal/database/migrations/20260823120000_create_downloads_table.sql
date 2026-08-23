-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS DOWNLOADS (
    id TEXT NOT NULL PRIMARY KEY,
    session_id INTEGER NOT NULL,
    dialog_id INTEGER NOT NULL,
    message_id INTEGER NOT NULL DEFAULT 0,
    filename TEXT NOT NULL,
    total INTEGER NOT NULL DEFAULT 0,
    written INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL DEFAULT 'downloading',
    origin TEXT NOT NULL DEFAULT 'telegram',
    category TEXT NOT NULL DEFAULT '',
    save_path TEXT NOT NULL DEFAULT '',
    content_path TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_downloads_state ON DOWNLOADS(state);
CREATE INDEX IF NOT EXISTS idx_downloads_origin ON DOWNLOADS(origin);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_downloads_state;
DROP INDEX IF EXISTS idx_downloads_origin;
DROP TABLE IF EXISTS DOWNLOADS;
-- +goose StatementEnd
