-- name: RecordAttempt :one
-- Idempotent: a retried call recording the same (run, stage, turn,
-- attempt_no) again overwrites the row with the same values (an activity
-- retry always carries what the first attempt did) rather than violating the
-- primary key.
INSERT INTO attempt (
    run_id, stage, turn, attempt_no,
    model, effort,
    input_tokens, cached_input_tokens, output_tokens, reasoning_tokens,
    measured, started_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (run_id, stage, turn, attempt_no) DO UPDATE SET
    model = EXCLUDED.model,
    effort = EXCLUDED.effort,
    input_tokens = EXCLUDED.input_tokens,
    cached_input_tokens = EXCLUDED.cached_input_tokens,
    output_tokens = EXCLUDED.output_tokens,
    reasoning_tokens = EXCLUDED.reasoning_tokens,
    measured = EXCLUDED.measured,
    started_at = EXCLUDED.started_at
RETURNING *;

-- name: EndAttempt :one
UPDATE attempt SET ended_at = $5, result = $6
WHERE run_id = $1 AND stage = $2 AND turn = $3 AND attempt_no = $4
RETURNING *;

-- name: AttemptsForStep :many
SELECT * FROM attempt WHERE run_id = $1 AND stage = $2 AND turn = $3 ORDER BY attempt_no;

-- name: AttemptsForRun :many
SELECT * FROM attempt WHERE run_id = $1 ORDER BY started_at, attempt_no;
