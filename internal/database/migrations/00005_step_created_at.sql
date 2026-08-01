-- +goose Up
ALTER TABLE step ADD COLUMN created_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- +goose Down
ALTER TABLE step DROP COLUMN created_at;
