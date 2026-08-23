-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS TOKENS (
    id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    token TEXT NOT NULL,
    type TEXT NOT NULL,
    user_id INTEGER NOT NULL,
    expires_at DATETIME NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_id ON TOKENS(id);
CREATE INDEX IF NOT EXISTS idx_user_id ON TOKENS(user_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_id;
DROP INDEX IF EXISTS idx_user_id;
DROP TABLE IF EXISTS TOKENS;
-- +goose StatementEnd
