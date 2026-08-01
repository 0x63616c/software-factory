-- +goose Up
-- One capability owns repository checkpoint writes for one active Run Worker
-- generation. Only the hash is durable; the clear value lives in that
-- generation's projected Kubernetes Secret.
CREATE TABLE run_repository_capability (
    run_id UUID PRIMARY KEY REFERENCES run (id) ON DELETE RESTRICT,
    generation BIGINT NOT NULL,
    capability_hash TEXT NOT NULL,
    CONSTRAINT run_repository_capability_generation_check CHECK (generation >= 1),
    CONSTRAINT run_repository_capability_hash_check CHECK (capability_hash <> '')
);

-- Clone and CI are repository checkpoints before a pull request exists. Zero
-- is their explicit "not created yet" value; positive numbers identify a PR.
ALTER TABLE run_git_checkpoint
    DROP CONSTRAINT run_git_checkpoint_pr_check,
    ADD CONSTRAINT run_git_checkpoint_pr_check CHECK (pull_request_number >= 0);

-- +goose Down
DELETE FROM run_git_checkpoint WHERE pull_request_number = 0;
ALTER TABLE run_git_checkpoint
    DROP CONSTRAINT run_git_checkpoint_pr_check,
    ADD CONSTRAINT run_git_checkpoint_pr_check CHECK (pull_request_number >= 1);
DROP TABLE run_repository_capability;
