-- +goose Up
-- +goose StatementBegin
ALTER TABLE USERS RENAME COLUMN update_at TO updated_at;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE USERS RENAME COLUMN updated_at TO update_at;
-- +goose StatementEnd
