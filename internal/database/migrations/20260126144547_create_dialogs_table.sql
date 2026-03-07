-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS DIALOGS (
    id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    session_id INTEGER NOT NULL,
    phone_number TEXT NOT NULL,
    name TEXT NOT NULL UNIQUE, 
    type TEXT NOT NULL,
    dialog_id INTEGER NOT NULL,
    access_hash INTEGER NOT NULL,
    indexer BOOLEAN,
    active BOOLEAN NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_id ON DIALOGS(id);
CREATE INDEX IF NOT EXISTS idx_dialog_name ON DIALOGS(name);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_id;
DROP INDEX IF EXISTS idx_dialog_name;
DROP TABLE IF EXISTS DIALOGS;
-- +goose StatementEnd
