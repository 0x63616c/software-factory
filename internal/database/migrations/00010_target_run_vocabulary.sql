-- +goose Up
-- PR5 writes the final target vocabulary. Keep prepare_run_worker readable
-- until PR8 performs the quiesced history cleanup.
ALTER TABLE run
    DROP CONSTRAINT run_target_failure_kind_check,
    ADD CONSTRAINT run_target_failure_kind_check
    CHECK (target_failure_kind IN ('', 'invalid_input', 'agent_unrecoverable', 'agent_attempt_budget',
        'review_budget', 'ci_unobserved', 'github_auth', 'github_ruleset', 'github_unavailable',
        'run_worker_unavailable', 'persistence_unavailable', 'semantic_deadline', 'infrastructure'));

ALTER TABLE run_step
    DROP CONSTRAINT run_step_kind_check,
    ADD CONSTRAINT run_step_kind_check
    CHECK (kind IN ('prepare_run_worker', 'create_run_worker', 'acquire_run_worker_session',
        'clone_repository', 'plan', 'implement', 'sync_pull_request', 'await_ci', 'review',
        'mark_pull_request_ready', 'merge_pull_request'));

-- +goose Down
ALTER TABLE run_step
    DROP CONSTRAINT run_step_kind_check,
    ADD CONSTRAINT run_step_kind_check
    CHECK (kind IN ('prepare_run_worker', 'acquire_run_worker_session',
        'clone_repository', 'plan', 'implement', 'sync_pull_request', 'await_ci', 'review',
        'mark_pull_request_ready', 'merge_pull_request'));

ALTER TABLE run
    DROP CONSTRAINT run_target_failure_kind_check,
    ADD CONSTRAINT run_target_failure_kind_check
    CHECK (target_failure_kind IN ('', 'invalid_input', 'agent_unrecoverable', 'agent_attempt_budget',
        'review_budget', 'ci_unobserved', 'github_auth', 'github_ruleset', 'github_unavailable',
        'run_worker_unavailable', 'persistence_unavailable', 'infrastructure'));
