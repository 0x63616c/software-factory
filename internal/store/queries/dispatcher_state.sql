-- name: DispatcherState :one
SELECT * FROM dispatcher_state WHERE singleton = TRUE;

-- name: PutDispatcherState :exec
UPDATE dispatcher_state
SET config = $1,
    config_error = $2,
    breaker = $3,
    in_flight = $4,
    candidates = $5,
    free_slots = $6,
    written_at = $7
WHERE singleton = TRUE;
