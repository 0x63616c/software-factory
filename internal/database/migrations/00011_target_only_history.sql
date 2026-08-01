-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM ticket WHERE state IN ('working', 'review')) THEN
        RAISE EXCEPTION 'target activation requires every legacy Ticket to be reconciled';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM run
        WHERE ended_at IS NULL
          AND target_outcome IS NULL
          AND (outcome IS NOT NULL OR EXISTS (SELECT 1 FROM step WHERE step.run_id = run.id))
    ) THEN
        RAISE EXCEPTION 'target activation requires every legacy Run to be terminal';
    END IF;
    IF EXISTS (
        SELECT 1 FROM step
        JOIN run_step ON run_step.run_id = step.run_id
    ) THEN
        RAISE EXCEPTION 'target activation cannot merge mixed legacy and ordinal history for one Run';
    END IF;
END $$;
-- +goose StatementEnd

CREATE TEMPORARY TABLE target_history_step_map ON COMMIT DROP AS
SELECT
    step.run_id,
    step.stage,
    step.turn,
    ROW_NUMBER() OVER (
        PARTITION BY step.run_id
        ORDER BY
            step.created_at,
            CASE step.stage WHEN 'plan' THEN 1 WHEN 'implement' THEN 2 WHEN 'review' THEN 3 END,
            step.turn
    )::INTEGER AS ordinal,
    step.created_at
FROM step;

INSERT INTO run_step (
    run_id, ordinal, kind, iteration, reason, state, started_at, ended_at, result
)
SELECT
    mapped.run_id,
    mapped.ordinal,
    mapped.stage,
    mapped.turn,
    'legacy_history_backfill',
    CASE
        WHEN COALESCE(last_attempt.result, 'succeeded') = 'succeeded' THEN 'completed'
        ELSE 'failed'
    END,
    COALESCE(first_attempt.started_at, mapped.created_at),
    COALESCE(last_attempt.ended_at, legacy_run.ended_at, mapped.created_at),
    NULL
FROM target_history_step_map AS mapped
JOIN run AS legacy_run ON legacy_run.id = mapped.run_id
LEFT JOIN LATERAL (
    SELECT attempt.started_at
    FROM attempt
    WHERE attempt.run_id = mapped.run_id
      AND attempt.stage = mapped.stage
      AND attempt.turn = mapped.turn
    ORDER BY attempt.attempt_no
    LIMIT 1
) AS first_attempt ON TRUE
LEFT JOIN LATERAL (
    SELECT attempt.result, attempt.ended_at
    FROM attempt
    WHERE attempt.run_id = mapped.run_id
      AND attempt.stage = mapped.stage
      AND attempt.turn = mapped.turn
    ORDER BY attempt.attempt_no DESC
    LIMIT 1
) AS last_attempt ON TRUE
ON CONFLICT (run_id, ordinal) DO NOTHING;

INSERT INTO run_agent_attempt (
    run_id, step_ordinal, attempt_no, agent_stage, model, effort, state,
    failure_kind, provider_thread_id, usage_state,
    input_tokens, cached_input_tokens, output_tokens, reasoning_tokens,
    started_at, ended_at, result
)
SELECT
    attempt.run_id,
    mapped.ordinal,
    attempt.attempt_no,
    attempt.stage,
    attempt.model,
    attempt.effort,
    CASE WHEN attempt.result = 'succeeded' THEN 'succeeded' ELSE 'failed' END,
    CASE WHEN attempt.result = 'succeeded' THEN '' ELSE 'infrastructure' END,
    '',
    CASE WHEN attempt.measured THEN 'measured' ELSE 'unknown' END,
    attempt.input_tokens,
    attempt.cached_input_tokens,
    attempt.output_tokens,
    attempt.reasoning_tokens,
    attempt.started_at,
    COALESCE(attempt.ended_at, legacy_run.ended_at, attempt.started_at),
    NULL
FROM attempt
JOIN target_history_step_map AS mapped
  ON mapped.run_id = attempt.run_id
 AND mapped.stage = attempt.stage
 AND mapped.turn = attempt.turn
JOIN run AS legacy_run ON legacy_run.id = attempt.run_id
ON CONFLICT (run_id, step_ordinal, attempt_no) DO NOTHING;

INSERT INTO run_agent_transcript (
    run_id, step_ordinal, attempt_no, compressed_bytes, compression,
    uncompressed_size_bytes, checksum
)
SELECT
    transcript.run_id,
    mapped.ordinal,
    transcript.attempt_no,
    transcript.compressed_bytes,
    transcript.compression,
    transcript.uncompressed_size_bytes,
    transcript.checksum
FROM transcript
JOIN target_history_step_map AS mapped
  ON mapped.run_id = transcript.run_id
 AND mapped.stage = transcript.stage
 AND mapped.turn = transcript.turn
ON CONFLICT (run_id, step_ordinal, attempt_no) DO NOTHING;

-- +goose StatementBegin
DO $$
BEGIN
    IF (SELECT COUNT(*) FROM step) <> (
        SELECT COUNT(*) FROM target_history_step_map AS mapped
        JOIN run_step AS target
          ON target.run_id = mapped.run_id
         AND target.ordinal = mapped.ordinal
         AND target.kind = mapped.stage
         AND target.iteration = mapped.turn
    ) THEN
        RAISE EXCEPTION 'legacy Step backfill count or ordering mismatch';
    END IF;
    IF (SELECT COUNT(*) FROM attempt) <> (
        SELECT COUNT(*) FROM attempt AS legacy
        JOIN target_history_step_map AS mapped
          ON mapped.run_id = legacy.run_id
         AND mapped.stage = legacy.stage
         AND mapped.turn = legacy.turn
        JOIN run_agent_attempt AS target
          ON target.run_id = legacy.run_id
         AND target.step_ordinal = mapped.ordinal
         AND target.attempt_no = legacy.attempt_no
         AND target.agent_stage = legacy.stage
         AND target.model = legacy.model
         AND target.effort = legacy.effort
         AND target.input_tokens = legacy.input_tokens
         AND target.cached_input_tokens = legacy.cached_input_tokens
         AND target.output_tokens = legacy.output_tokens
         AND target.reasoning_tokens = legacy.reasoning_tokens
         AND target.usage_state = CASE WHEN legacy.measured THEN 'measured' ELSE 'unknown' END
    ) THEN
        RAISE EXCEPTION 'legacy Attempt backfill count or value mismatch';
    END IF;
    IF (SELECT COUNT(*) FROM transcript) <> (
        SELECT COUNT(*) FROM transcript AS legacy
        JOIN target_history_step_map AS mapped
          ON mapped.run_id = legacy.run_id
         AND mapped.stage = legacy.stage
         AND mapped.turn = legacy.turn
        JOIN run_agent_transcript AS target
          ON target.run_id = legacy.run_id
         AND target.step_ordinal = mapped.ordinal
         AND target.attempt_no = legacy.attempt_no
         AND target.compressed_bytes = legacy.compressed_bytes
         AND target.compression = legacy.compression
         AND target.uncompressed_size_bytes = legacy.uncompressed_size_bytes
         AND target.checksum = legacy.checksum
    ) THEN
        RAISE EXCEPTION 'legacy transcript backfill count or value mismatch';
    END IF;
END $$;
-- +goose StatementEnd

UPDATE run
SET
    target_outcome = CASE outcome
        WHEN 'proposed' THEN 'canceled'
        WHEN 'blocked' THEN 'failed'
        WHEN 'exhausted' THEN 'exhausted'
        WHEN 'failed' THEN 'failed'
    END,
    target_failure_kind = CASE
        WHEN outcome IN ('proposed', 'exhausted') THEN ''
        WHEN failure_kind = 'auth' THEN 'github_auth'
        WHEN failure_kind = 'rate-limit' THEN 'github_unavailable'
        ELSE 'infrastructure'
    END
WHERE target_outcome IS NULL
  AND outcome IS NOT NULL;

DELETE FROM transcript;
DELETE FROM attempt;
DELETE FROM step;

ALTER TABLE transcript ADD CONSTRAINT transcript_retired_check CHECK (FALSE);
ALTER TABLE attempt ADD CONSTRAINT attempt_retired_check CHECK (FALSE);
ALTER TABLE step ADD CONSTRAINT step_retired_check CHECK (FALSE);

ALTER TABLE ticket
    DROP CONSTRAINT ticket_state_check,
    ADD CONSTRAINT ticket_state_check CHECK (state IN ('open', 'active', 'done', 'failed'));

ALTER TABLE run_agent_attempt RENAME COLUMN provider_thread_id TO execution_id;

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    RAISE EXCEPTION '00011_target_only_history is an irreversible forward cutover';
END $$;
-- +goose StatementEnd
