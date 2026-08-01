-- name: PutTranscript :exec
-- Idempotent: an activity retry persisting the same Attempt's transcript
-- again is a no-op, not a constraint violation — the same reasoning as
-- RecordStep's own ON CONFLICT DO NOTHING.
INSERT INTO transcript (
    run_id, stage, turn, attempt_no,
    compressed_bytes, compression, uncompressed_size_bytes, checksum
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (run_id, stage, turn, attempt_no) DO NOTHING;

-- name: Transcript :one
SELECT * FROM transcript
WHERE run_id = $1 AND stage = $2 AND turn = $3 AND attempt_no = $4;

-- name: TranscriptKeysForRun :many
-- The console's run detail view needs to know which Attempts have a
-- transcript to download, without paying for every compressed blob just to
-- render that flag.
SELECT stage, turn, attempt_no FROM transcript WHERE run_id = $1;
