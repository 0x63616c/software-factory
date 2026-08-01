-- +goose Up
CREATE TABLE migration_probe (
    present BOOLEAN NOT NULL DEFAULT TRUE
);

-- +goose Down
DROP TABLE migration_probe;
