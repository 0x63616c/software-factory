-- name: CreateTicket :one
INSERT INTO ticket (title, body, state)
VALUES ($1, $2, $3)
RETURNING *;

-- name: Ticket :one
SELECT * FROM ticket WHERE id = $1;

-- name: TicketsByState :many
SELECT * FROM ticket WHERE state = $1 ORDER BY id;

-- name: Tickets :many
SELECT * FROM ticket ORDER BY id;

-- name: ReopenLegacyTicket :one
UPDATE ticket SET state = 'open', updated_at = CURRENT_TIMESTAMP
WHERE id = $1
  AND state = $2
  AND updated_at = $3
  AND state IN ('working', 'review')
RETURNING *;

-- name: UpdateTicketState :one
UPDATE ticket SET state = $2, updated_at = CURRENT_TIMESTAMP
WHERE id = $1
  AND state <> 'active'
  AND (state <> 'done' OR $2 = 'done')
RETURNING *;

-- name: TransitionTicketState :one
UPDATE ticket SET state = $3, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND state = $2
  AND $3 <> 'active'
  AND state <> 'active'
  AND (state <> 'done' OR $3 = 'done')
RETURNING *;

-- name: ReadyTickets :many
-- A ticket is ready when it is open and every ticket it depends on is done.
-- Direct dependencies only, per ADR-0012 -- a recursive CTE would answer a
-- different question (transitive reachability) that nothing here asks.
SELECT t.* FROM ticket t
WHERE t.state = 'open'
  AND NOT EXISTS (
    SELECT 1 FROM ticket_edge e
    JOIN ticket blocker ON blocker.id = e.blocker_ticket_id
    WHERE e.blocked_ticket_id = t.id AND blocker.state <> 'done'
  )
ORDER BY t.id;
