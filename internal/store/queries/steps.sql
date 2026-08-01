-- name: RecordStep :exec
-- Idempotent: an activity retry recording the same step again is a no-op, not
-- a constraint violation.
INSERT INTO step (run_id, stage, turn)
VALUES ($1, $2, $3)
ON CONFLICT (run_id, stage, turn) DO NOTHING;

-- name: StepsForRun :many
SELECT * FROM step WHERE run_id = $1 ORDER BY created_at;
