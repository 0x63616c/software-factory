-- +goose Up
-- Target Run history is additive while legacy workflows still write
-- (stage, turn) rows. PR 8 performs the final, quiesced backfill.
ALTER TABLE ticket
    ADD COLUMN active_run_id UUID;

ALTER TABLE ticket
    ADD CONSTRAINT ticket_active_run_id_fkey
    FOREIGN KEY (active_run_id) REFERENCES run (id) ON DELETE RESTRICT;

ALTER TABLE ticket
    DROP CONSTRAINT ticket_state_check,
    ADD CONSTRAINT ticket_state_check
    CHECK (state IN ('open', 'working', 'review', 'active', 'done', 'failed'));

ALTER TABLE run
    ADD COLUMN target_outcome TEXT,
    ADD COLUMN target_failure_kind TEXT NOT NULL DEFAULT '',
    ADD COLUMN reviewed_head TEXT,
    ADD COLUMN merge_sha TEXT;

ALTER TABLE run
    ADD CONSTRAINT run_target_outcome_check
    CHECK (target_outcome IS NULL OR target_outcome IN ('succeeded', 'canceled', 'exhausted', 'failed')),
    ADD CONSTRAINT run_target_failure_kind_check
    CHECK (target_failure_kind IN ('', 'invalid_input', 'agent_unrecoverable', 'agent_attempt_budget',
        'review_budget', 'ci_unobserved', 'github_auth', 'github_ruleset', 'github_unavailable',
        'run_worker_unavailable', 'persistence_unavailable', 'infrastructure')),
    ADD CONSTRAINT run_confirmed_merge_check
    CHECK ((target_outcome = 'succeeded') = (reviewed_head IS NOT NULL AND merge_sha IS NOT NULL));

CREATE TABLE run_step (
    run_id UUID NOT NULL REFERENCES run (id) ON DELETE RESTRICT,
    ordinal INTEGER NOT NULL,
    kind TEXT NOT NULL,
    iteration INTEGER NOT NULL DEFAULT 0,
    reason TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL,
    ended_at TIMESTAMPTZ,
    result JSONB,
    PRIMARY KEY (run_id, ordinal),
    CONSTRAINT run_step_ordinal_check CHECK (ordinal >= 1),
    CONSTRAINT run_step_kind_check CHECK (kind IN ('prepare_run_worker', 'acquire_run_worker_session',
        'clone_repository', 'plan', 'implement', 'sync_pull_request', 'await_ci', 'review',
        'mark_pull_request_ready', 'merge_pull_request')),
    CONSTRAINT run_step_state_check CHECK (state IN ('running', 'completed', 'failed')),
    CONSTRAINT run_step_terminal_time_check CHECK ((state = 'running') = (ended_at IS NULL))
);

CREATE TABLE run_agent_attempt (
    run_id UUID NOT NULL,
    step_ordinal INTEGER NOT NULL,
    attempt_no INTEGER NOT NULL,
    agent_stage TEXT NOT NULL,
    model TEXT NOT NULL,
    effort TEXT NOT NULL,
    state TEXT NOT NULL,
    failure_kind TEXT NOT NULL DEFAULT '',
    provider_thread_id TEXT NOT NULL DEFAULT '',
    usage_state TEXT NOT NULL,
    input_tokens BIGINT NOT NULL DEFAULT 0,
    cached_input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    reasoning_tokens BIGINT NOT NULL DEFAULT 0,
    started_at TIMESTAMPTZ NOT NULL,
    ended_at TIMESTAMPTZ,
    result JSONB,
    checkpoint_capability_hash TEXT,
    PRIMARY KEY (run_id, step_ordinal, attempt_no),
    FOREIGN KEY (run_id, step_ordinal) REFERENCES run_step (run_id, ordinal) ON DELETE RESTRICT,
    CONSTRAINT run_agent_attempt_number_check CHECK (attempt_no >= 1),
    CONSTRAINT run_agent_attempt_stage_check CHECK (agent_stage IN ('plan', 'implement', 'review')),
    CONSTRAINT run_agent_attempt_state_check CHECK (state IN ('running', 'succeeded', 'failed')),
    CONSTRAINT run_agent_attempt_usage_state_check CHECK (usage_state IN ('unknown', 'measured')),
    CONSTRAINT run_agent_attempt_terminal_time_check CHECK ((state = 'running') = (ended_at IS NULL)),
    CONSTRAINT run_agent_attempt_input_tokens_check CHECK (input_tokens >= 0),
    CONSTRAINT run_agent_attempt_cached_input_tokens_check CHECK (cached_input_tokens >= 0),
    CONSTRAINT run_agent_attempt_output_tokens_check CHECK (output_tokens >= 0),
    CONSTRAINT run_agent_attempt_reasoning_tokens_check CHECK (reasoning_tokens >= 0)
);

CREATE TABLE run_agent_transcript (
    run_id UUID NOT NULL,
    step_ordinal INTEGER NOT NULL,
    attempt_no INTEGER NOT NULL,
    compressed_bytes BYTEA NOT NULL,
    compression TEXT NOT NULL,
    uncompressed_size_bytes BIGINT NOT NULL,
    checksum BYTEA NOT NULL,
    PRIMARY KEY (run_id, step_ordinal, attempt_no),
    FOREIGN KEY (run_id, step_ordinal, attempt_no)
        REFERENCES run_agent_attempt (run_id, step_ordinal, attempt_no) ON DELETE RESTRICT,
    CONSTRAINT run_agent_transcript_size_check CHECK (uncompressed_size_bytes >= 0)
);

CREATE TABLE run_git_checkpoint (
    run_id UUID PRIMARY KEY REFERENCES run (id) ON DELETE RESTRICT,
    step_ordinal INTEGER NOT NULL,
    branch TEXT NOT NULL,
    pushed_head TEXT NOT NULL,
    observed_base TEXT NOT NULL,
    pull_request_number INTEGER NOT NULL,
    pull_request_node_id TEXT NOT NULL,
    step_result JSONB NOT NULL,
    CONSTRAINT run_git_checkpoint_step_check CHECK (step_ordinal >= 1),
    CONSTRAINT run_git_checkpoint_pr_check CHECK (pull_request_number >= 1)
);

-- +goose Down
DROP TABLE run_git_checkpoint;
DROP TABLE run_agent_transcript;
DROP TABLE run_agent_attempt;
DROP TABLE run_step;
ALTER TABLE run
    DROP CONSTRAINT run_confirmed_merge_check,
    DROP CONSTRAINT run_target_failure_kind_check,
    DROP CONSTRAINT run_target_outcome_check,
    DROP COLUMN merge_sha,
    DROP COLUMN reviewed_head,
    DROP COLUMN target_failure_kind,
    DROP COLUMN target_outcome;
ALTER TABLE ticket
    DROP CONSTRAINT ticket_state_check,
    ADD CONSTRAINT ticket_state_check CHECK (state IN ('open', 'working', 'review', 'done', 'failed')),
    DROP CONSTRAINT ticket_active_run_id_fkey,
    DROP COLUMN active_run_id;
