-- +goose Up
-- +goose StatementBegin
ALTER TABLE DOWNLOADS ADD COLUMN remote_gid TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE DOWNLOADS DROP COLUMN remote_gid;
-- +goose StatementEnd
