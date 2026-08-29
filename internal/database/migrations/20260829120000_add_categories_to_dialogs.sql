-- +goose Up
-- +goose StatementBegin
ALTER TABLE DIALOGS ADD COLUMN categories TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE DIALOGS DROP COLUMN categories;
-- +goose StatementEnd
