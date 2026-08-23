-- +goose Up
-- +goose StatementBegin
-- No-op: historical one-time repair of a typo'd update_at column on pre-prod
-- developer databases, which have all already applied this version. Fresh
-- databases are created with updated_at directly by 20260307084526, so a
-- rename here would fail.
SELECT 1;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SELECT 1;
-- +goose StatementEnd
