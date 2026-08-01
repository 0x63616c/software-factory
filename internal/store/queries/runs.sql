-- name: StartRun :one
-- Idempotent: a retried call starting the same run id again overwrites the
-- row with the same values (an activity retry always carries what the first
-- attempt did) rather than violating the primary key.
INSERT INTO run (id, ticket_id, started_at)
VALUES ($1, $2, $3)
ON CONFLICT (id) DO UPDATE SET
    ticket_id = EXCLUDED.ticket_id,
    started_at = EXCLUDED.started_at
RETURNING *;

-- name: EndRun :one
UPDATE run SET ended_at = $2, outcome = $3, failure_kind = $4
WHERE id = $1
RETURNING *;

-- name: Run :one
SELECT * FROM run WHERE id = $1;

-- name: RunsForTicket :many
-- Most recent first: the console's ticket detail view leads with the
-- current or latest Run.
SELECT * FROM run WHERE ticket_id = $1 ORDER BY started_at DESC;

-- name: OpenLegacyRuns :many
SELECT run.* FROM run
JOIN ticket ON ticket.id = run.ticket_id
WHERE run.ended_at IS NULL
  AND run.target_outcome IS NULL
  AND ticket.state IN ('working', 'review')
ORDER BY run.started_at, run.id;

-- name: CloseLegacyRun :one
UPDATE run SET ended_at = $2, outcome = 'failed', failure_kind = 'other'
WHERE id = $1
  AND ended_at IS NULL
  AND target_outcome IS NULL
RETURNING *;
