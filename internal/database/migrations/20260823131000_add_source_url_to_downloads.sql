-- +goose Up
-- +goose StatementBegin
ALTER TABLE DOWNLOADS ADD COLUMN source_url TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE DOWNLOADS DROP COLUMN source_url;
-- +goose StatementEnd
