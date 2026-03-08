-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS USERS (
    id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    update_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_id ON USERS(id);
CREATE INDEX IF NOT EXISTS idx_username ON USERS(username);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_id;
DROP INDEX IF EXISTS idx_username;
DROP TABLE IF EXISTS USERS;
-- +goose StatementEnd
