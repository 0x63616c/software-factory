-- name: AddTicketDependency :exec
-- Cycle rejection is application-level, owned by the API ticket that creates
-- edges (ADR-0012) -- this query only records the edge.
INSERT INTO ticket_edge (blocker_ticket_id, blocked_ticket_id)
VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: RemoveTicketDependency :exec
DELETE FROM ticket_edge
WHERE blocker_ticket_id = $1 AND blocked_ticket_id = $2;

-- name: TicketBlockers :many
-- Every ticket that blocks the given ticket.
SELECT blocker.* FROM ticket_edge e
JOIN ticket blocker ON blocker.id = e.blocker_ticket_id
WHERE e.blocked_ticket_id = $1
ORDER BY blocker.id;

-- name: TicketBlocks :many
-- Every ticket the given ticket blocks.
SELECT blocked.* FROM ticket_edge e
JOIN ticket blocked ON blocked.id = e.blocked_ticket_id
WHERE e.blocker_ticket_id = $1
ORDER BY blocked.id;

-- name: TicketDependencyPath :one
-- Follows blocker -> blocked edges from start to target. A path means adding
-- target -> start would close a dependency cycle.
WITH RECURSIVE reachable(node_id, path) AS (
    SELECT $1::bigint, ARRAY[$1::bigint]
    UNION ALL
    SELECT e.blocked_ticket_id, r.path || e.blocked_ticket_id
    FROM reachable r
    JOIN ticket_edge e ON e.blocker_ticket_id = r.node_id
    WHERE NOT e.blocked_ticket_id = ANY(r.path)
)
SELECT array_to_string(path, ',')
FROM reachable
WHERE path @> ARRAY[$2::bigint]
LIMIT 1;

-- name: AddTicketDependencyIfAcyclic :one
-- A transaction-scoped advisory lock serializes edge writers while this
-- statement checks and writes, so concurrent requests cannot interleave into
-- a cycle. The graph is small and edge writes are rare.
WITH RECURSIVE graph_lock AS (
    SELECT pg_advisory_xact_lock(547)
), reachable(node_id, path) AS (
    SELECT $1::bigint, ARRAY[$1::bigint] FROM graph_lock
    UNION ALL
    SELECT e.blocked_ticket_id, r.path || e.blocked_ticket_id
    FROM reachable r
    JOIN ticket_edge e ON e.blocker_ticket_id = r.node_id
    WHERE NOT e.blocked_ticket_id = ANY(r.path)
), inserted AS (
    INSERT INTO ticket_edge (blocker_ticket_id, blocked_ticket_id)
    SELECT $2, $1
    WHERE NOT EXISTS (SELECT 1 FROM reachable WHERE path @> ARRAY[$2::bigint])
    ON CONFLICT DO NOTHING
)
SELECT COALESCE((SELECT array_to_string(path, ',') FROM reachable WHERE path @> ARRAY[$2::bigint] LIMIT 1), '')::text;
