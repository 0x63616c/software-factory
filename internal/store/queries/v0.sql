-- name: TicketForTargetClaim :one
SELECT * FROM ticket WHERE id = $1 FOR UPDATE;

-- name: CountLegacyTicketStates :one
SELECT COUNT(*) FROM ticket WHERE state IN ('working', 'review');

-- name: InsertTargetRun :one
INSERT INTO run (id, ticket_id, started_at)
VALUES ($1, $2, $3)
ON CONFLICT (id) DO UPDATE SET id = run.id
RETURNING *;

-- name: ActivateTargetTicket :one
UPDATE ticket SET state = 'active', active_run_id = $2, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND state = 'open'
RETURNING *;

-- name: TargetRunForUpdate :one
SELECT * FROM run WHERE id = $1 FOR UPDATE;

-- name: TargetTicketForUpdate :one
SELECT * FROM ticket WHERE id = $1 FOR UPDATE;

-- name: TargetRunOwned :one
SELECT EXISTS (
    SELECT 1 FROM run
    JOIN ticket ON ticket.id = run.ticket_id
    WHERE run.id = $1 AND run.target_outcome IS NULL
      AND ticket.state = 'active' AND ticket.active_run_id = run.id
);

-- name: StartTargetStep :one
INSERT INTO run_step (run_id, ordinal, kind, iteration, reason, state, started_at)
SELECT $1, $2, $3, $4, $5, 'running', $6
WHERE EXISTS (
    SELECT 1 FROM run
    JOIN ticket ON ticket.id = run.ticket_id
    WHERE run.id = $1 AND run.target_outcome IS NULL
      AND ticket.state = 'active' AND ticket.active_run_id = run.id
)
ON CONFLICT (run_id, ordinal) DO UPDATE SET run_id = run_step.run_id
WHERE run_step.kind = EXCLUDED.kind
  AND run_step.iteration = EXCLUDED.iteration
  AND run_step.reason = EXCLUDED.reason
  AND run_step.started_at = EXCLUDED.started_at
RETURNING *;

-- name: CompleteTargetStep :one
UPDATE run_step SET
    state = 'completed',
    ended_at = CASE WHEN run_step.state = 'running' THEN $3 ELSE run_step.ended_at END,
    result = CASE WHEN run_step.state = 'running' THEN $4 ELSE run_step.result END
WHERE run_id = $1 AND ordinal = $2
  AND EXISTS (
      SELECT 1 FROM run
      JOIN ticket ON ticket.id = run.ticket_id
      WHERE run.id = $1 AND run.target_outcome IS NULL
        AND ticket.state = 'active' AND ticket.active_run_id = run.id
  )
  AND (state = 'running' OR (state = 'completed' AND result = $4))
RETURNING *;

-- name: FailTargetStep :one
UPDATE run_step SET state = 'failed', ended_at = $3, result = $4
WHERE run_id = $1 AND ordinal = $2 AND state = 'running'
RETURNING *;

-- name: FailRunningTargetAgentAttempts :many
UPDATE run_agent_attempt SET state = 'failed', failure_kind = $3, ended_at = $4
WHERE run_id = $1 AND step_ordinal = $2 AND state = 'running'
RETURNING *;

-- name: CompleteTargetMergeStep :one
UPDATE run_step SET state = 'completed', ended_at = $3, result = $4
WHERE run_id = $1 AND ordinal = $2 AND kind = 'merge_pull_request'
  AND (state = 'running' OR (state = 'completed' AND result = $4))
RETURNING *;

-- name: TargetStepForRun :many
SELECT * FROM run_step WHERE run_id = $1 ORDER BY ordinal;

-- name: TargetStep :one
SELECT * FROM run_step WHERE run_id = $1 AND ordinal = $2;

-- name: TargetStepForUpdate :one
SELECT * FROM run_step WHERE run_id = $1 AND ordinal = $2 FOR UPDATE;

-- name: StartTargetAgentAttempt :one
INSERT INTO run_agent_attempt (
    run_id, step_ordinal, attempt_no, agent_stage, model, effort, state,
    usage_state, started_at
) SELECT $1, $2, $3, $4, $5, $6, 'running', $7, $8
FROM run_step
WHERE run_id = $1 AND ordinal = $2 AND kind = $4 AND state = 'running'
  AND EXISTS (
      SELECT 1 FROM run
      JOIN ticket ON ticket.id = run.ticket_id
      WHERE run.id = $1 AND run.target_outcome IS NULL
        AND ticket.state = 'active' AND ticket.active_run_id = run.id
  )
ON CONFLICT (run_id, step_ordinal, attempt_no) DO UPDATE SET run_id = run_agent_attempt.run_id
WHERE run_agent_attempt.agent_stage = EXCLUDED.agent_stage
  AND run_agent_attempt.model = EXCLUDED.model
  AND run_agent_attempt.effort = EXCLUDED.effort
  AND run_agent_attempt.usage_state = EXCLUDED.usage_state
  AND run_agent_attempt.started_at = EXCLUDED.started_at
RETURNING *;

-- name: TargetAgentAttemptsForRun :many
SELECT * FROM run_agent_attempt WHERE run_id = $1 ORDER BY step_ordinal, attempt_no;

-- name: TargetAgentAttemptForUpdate :one
SELECT * FROM run_agent_attempt
WHERE run_id = $1 AND step_ordinal = $2 AND attempt_no = $3 FOR UPDATE;

-- name: TargetAgentAttempt :one
SELECT * FROM run_agent_attempt
WHERE run_id = $1 AND step_ordinal = $2 AND attempt_no = $3;

-- name: CheckpointTargetAgentAttempt :one
UPDATE run_agent_attempt SET
    execution_id = $4,
    state = $5,
    failure_kind = $6,
    usage_state = $7,
    input_tokens = $8,
    cached_input_tokens = $9,
    output_tokens = $10,
    reasoning_tokens = $11,
    ended_at = $12,
    result = $13
WHERE run_id = $1 AND step_ordinal = $2 AND attempt_no = $3
RETURNING *;

-- name: PutTargetAgentTranscript :exec
INSERT INTO run_agent_transcript (
    run_id, step_ordinal, attempt_no, compressed_bytes, compression,
    uncompressed_size_bytes, checksum
) VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (run_id, step_ordinal, attempt_no) DO UPDATE SET
    compressed_bytes = EXCLUDED.compressed_bytes,
    compression = EXCLUDED.compression,
    uncompressed_size_bytes = EXCLUDED.uncompressed_size_bytes,
    checksum = EXCLUDED.checksum;

-- name: TargetTranscriptKeysForRun :many
SELECT run_id, step_ordinal, attempt_no FROM run_agent_transcript
WHERE run_id = $1 ORDER BY step_ordinal, attempt_no;

-- name: TargetAgentTranscript :one
SELECT * FROM run_agent_transcript
WHERE run_id = $1 AND step_ordinal = $2 AND attempt_no = $3;

-- name: BindTargetAttemptCapability :one
UPDATE run_agent_attempt SET checkpoint_capability_hash = $4
WHERE run_id = $1 AND step_ordinal = $2 AND attempt_no = $3 AND state = 'running'
  AND (checkpoint_capability_hash IS NULL OR checkpoint_capability_hash = $4)
  AND EXISTS (
      SELECT 1 FROM run
      JOIN ticket ON ticket.id = run.ticket_id
      WHERE run.id = $1 AND run.target_outcome IS NULL
        AND ticket.state = 'active' AND ticket.active_run_id = run.id
  )
RETURNING *;

-- name: TargetGitCheckpoint :one
SELECT * FROM run_git_checkpoint WHERE run_id = $1;

-- name: StartRecoveredTargetMergeStep :one
INSERT INTO run_step (run_id, ordinal, kind, iteration, reason, state, started_at)
SELECT $1, COALESCE(MAX(ordinal), 0) + 1, 'merge_pull_request', 0,
       'reconcile confirmed external merge', 'running', $2
FROM run_step
WHERE run_id = $1
RETURNING *;

-- name: LatestCanceledRunGitCheckpoint :one
SELECT checkpoint.*, COALESCE(merge_step.ordinal, 0)::integer AS merge_step_ordinal
FROM run AS predecessor
JOIN run_git_checkpoint AS checkpoint ON checkpoint.run_id = predecessor.id
LEFT JOIN run_step AS merge_step
  ON merge_step.run_id = predecessor.id
  AND merge_step.kind = 'merge_pull_request'
  AND merge_step.state = 'running'
WHERE predecessor.ticket_id = $1
  AND predecessor.id <> $2
  AND predecessor.target_outcome = 'canceled'
  AND checkpoint.pushed_head <> ''
ORDER BY predecessor.ended_at DESC, checkpoint.step_ordinal DESC
LIMIT 1;

-- name: BindTargetRepositoryCapability :one
INSERT INTO run_repository_capability (run_id, generation, capability_hash)
SELECT $1, $2, $3
WHERE EXISTS (
    SELECT 1 FROM run
    JOIN ticket ON ticket.id = run.ticket_id
    WHERE run.id = $1 AND run.target_outcome IS NULL
      AND ticket.state = 'active' AND ticket.active_run_id = run.id
)
ON CONFLICT (run_id) DO UPDATE SET
    generation = EXCLUDED.generation,
    capability_hash = EXCLUDED.capability_hash
WHERE run_repository_capability.generation < EXCLUDED.generation
   OR (run_repository_capability.generation = EXCLUDED.generation
       AND run_repository_capability.capability_hash = EXCLUDED.capability_hash)
RETURNING *;

-- name: TargetRepositoryCapabilityForUpdate :one
SELECT * FROM run_repository_capability WHERE run_id = $1 FOR UPDATE;

-- name: PutTargetGitCheckpoint :one
INSERT INTO run_git_checkpoint (
    run_id, step_ordinal, branch, pushed_head, observed_base,
    pull_request_number, pull_request_node_id, step_result
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (run_id) DO UPDATE SET
    step_ordinal = EXCLUDED.step_ordinal,
    branch = EXCLUDED.branch,
    pushed_head = EXCLUDED.pushed_head,
    observed_base = EXCLUDED.observed_base,
    pull_request_number = EXCLUDED.pull_request_number,
    pull_request_node_id = EXCLUDED.pull_request_node_id,
    step_result = EXCLUDED.step_result
WHERE run_git_checkpoint.step_ordinal < EXCLUDED.step_ordinal
   OR (run_git_checkpoint.step_ordinal = EXCLUDED.step_ordinal
       AND run_git_checkpoint.branch = EXCLUDED.branch
       AND run_git_checkpoint.pushed_head = EXCLUDED.pushed_head
       AND run_git_checkpoint.observed_base = EXCLUDED.observed_base
       AND run_git_checkpoint.pull_request_number = EXCLUDED.pull_request_number
       AND run_git_checkpoint.pull_request_node_id = EXCLUDED.pull_request_node_id
       AND run_git_checkpoint.step_result = EXCLUDED.step_result)
RETURNING *;

-- name: CompleteTargetRunSuccess :one
UPDATE run SET target_outcome = 'succeeded', target_failure_kind = '',
    reviewed_head = $2, merge_sha = $3, ended_at = $4
WHERE id = $1 AND target_outcome IS NULL
RETURNING *;

-- name: ReconcileCanceledTargetRunSuccess :one
UPDATE run SET target_outcome = 'succeeded', target_failure_kind = '',
    reviewed_head = $2, merge_sha = $3, ended_at = $4
WHERE id = $1 AND target_outcome = 'canceled'
RETURNING *;

-- name: CompleteTargetRunCanceled :one
UPDATE run SET target_outcome = 'canceled', target_failure_kind = '', ended_at = $2
WHERE id = $1 AND target_outcome IS NULL
RETURNING *;

-- name: CompleteTargetRunTerminal :one
UPDATE run SET target_outcome = $2, target_failure_kind = $3, ended_at = $4
WHERE id = $1 AND target_outcome IS NULL
RETURNING *;

-- name: CompleteTargetTicket :one
UPDATE ticket SET state = 'done', active_run_id = NULL, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND state = 'active' AND active_run_id = $2
RETURNING *;

-- name: CompleteCanceledTargetTicket :one
UPDATE ticket SET state = 'done', active_run_id = NULL, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND state = 'open' AND active_run_id IS NULL
RETURNING *;

-- name: ReopenTargetTicket :one
UPDATE ticket SET state = 'open', active_run_id = NULL, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND state = 'active' AND active_run_id = $2
RETURNING *;

-- name: FailTargetTicket :one
UPDATE ticket SET state = 'failed', active_run_id = NULL, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND state = 'active' AND active_run_id = $2
RETURNING *;
