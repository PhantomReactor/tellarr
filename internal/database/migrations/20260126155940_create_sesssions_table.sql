-- +goose Up
-- +goose StatementBegin
CREATE TABLE SESSIONS (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    phone_number INTEGER NOT NULL, 
    token TEXT,
    active BOOLEAN NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_id ON SESSIONS(id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_id;
DROP TABLE IF EXISTS SESSIONS;
-- +goose StatementEnd
