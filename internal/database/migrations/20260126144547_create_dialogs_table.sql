-- +goose Up
-- +goose StatementBegin
CREATE TABLE DIALOGS (
    id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE, 
    type TEXT NOT NULL,
    channel_id INTEGER NOT NULL,
    access_hash INTEGER NOT NULL,
    active BOOLEAN NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_id ON DIALOGS(id);
CREATE INDEX idx_channel_id ON DIALOGS(channel_id);
-- +gooe StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_id;
DROP INDEX IF EXISTS idx_channel_id;
DROP TABLE IF EXISTS DIALOGS;
-- +goose StatementEnd
