-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS REFRESH_TOKENS (
    id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    token TEXT NOT NULL,
    user_id int64 NOT NULL,
    expires_at 
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
);

CREATE INDEX IF NOT EXISTS idx_id ON REFRESH_TOKENS(id);
CREATE INDEX IF NOT EXISTS idx_user_id ON REFRESH_TOKENS(user_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_id;
DROP INDEX IF EXISTS idx_user_id;
DROP TABLE REFRESH_TOKENS IF EXISTS;
-- +goose StatementEnd
