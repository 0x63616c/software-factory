-- +goose Up
CREATE TABLE ticket (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title TEXT NOT NULL,
    body TEXT NOT NULL,
    state TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT ticket_state_check CHECK (state IN ('open', 'working', 'review', 'done', 'failed'))
);

CREATE INDEX ticket_state_idx ON ticket (state);

CREATE TABLE ticket_edge (
    blocker_ticket_id BIGINT NOT NULL REFERENCES ticket (id) ON DELETE RESTRICT,
    blocked_ticket_id BIGINT NOT NULL REFERENCES ticket (id) ON DELETE RESTRICT,
    PRIMARY KEY (blocker_ticket_id, blocked_ticket_id),
    CONSTRAINT ticket_edge_not_self_check CHECK (blocker_ticket_id <> blocked_ticket_id)
);

CREATE INDEX ticket_edge_blocked_ticket_id_idx ON ticket_edge (blocked_ticket_id);

CREATE TABLE run (
    id UUID PRIMARY KEY,
    ticket_id BIGINT NOT NULL REFERENCES ticket (id) ON DELETE RESTRICT,
    started_at TIMESTAMPTZ NOT NULL,
    ended_at TIMESTAMPTZ,
    outcome TEXT,
    failure_kind TEXT NOT NULL DEFAULT '',
    CONSTRAINT run_outcome_check CHECK (outcome IS NULL OR outcome IN ('proposed', 'blocked', 'exhausted', 'failed')),
    CONSTRAINT run_failure_kind_check CHECK (failure_kind IN ('', 'auth', 'rate-limit', 'other'))
);

CREATE INDEX run_ticket_id_started_at_idx ON run (ticket_id, started_at DESC);

CREATE TABLE step (
    run_id UUID NOT NULL REFERENCES run (id) ON DELETE RESTRICT,
    stage TEXT NOT NULL,
    turn INTEGER NOT NULL,
    PRIMARY KEY (run_id, stage, turn),
    CONSTRAINT step_stage_check CHECK (stage IN ('plan', 'implement', 'review')),
    CONSTRAINT step_turn_check CHECK (turn >= 1)
);

CREATE TABLE attempt (
    run_id UUID NOT NULL,
    stage TEXT NOT NULL,
    turn INTEGER NOT NULL,
    attempt_no INTEGER NOT NULL,
    model TEXT NOT NULL,
    effort TEXT NOT NULL,
    input_tokens BIGINT NOT NULL,
    cached_input_tokens BIGINT NOT NULL,
    output_tokens BIGINT NOT NULL,
    reasoning_tokens BIGINT NOT NULL,
    measured BOOLEAN NOT NULL,
    started_at TIMESTAMPTZ NOT NULL,
    ended_at TIMESTAMPTZ,
    result TEXT,
    PRIMARY KEY (run_id, stage, turn, attempt_no),
    FOREIGN KEY (run_id, stage, turn) REFERENCES step (run_id, stage, turn) ON DELETE RESTRICT,
    CONSTRAINT attempt_number_check CHECK (attempt_no >= 1),
    CONSTRAINT attempt_input_tokens_check CHECK (input_tokens >= 0),
    CONSTRAINT attempt_cached_input_tokens_check CHECK (cached_input_tokens >= 0),
    CONSTRAINT attempt_output_tokens_check CHECK (output_tokens >= 0),
    CONSTRAINT attempt_reasoning_tokens_check CHECK (reasoning_tokens >= 0),
    CONSTRAINT attempt_result_check CHECK (result IS NULL OR result IN ('succeeded', 'failed'))
);

COMMENT ON COLUMN attempt.input_tokens IS 'Includes cached_input_tokens.';
COMMENT ON COLUMN attempt.output_tokens IS 'Includes reasoning_tokens.';

CREATE TABLE transcript (
    run_id UUID NOT NULL,
    stage TEXT NOT NULL,
    turn INTEGER NOT NULL,
    attempt_no INTEGER NOT NULL,
    compressed_bytes BYTEA NOT NULL,
    compression TEXT NOT NULL,
    uncompressed_size_bytes BIGINT NOT NULL,
    checksum BYTEA NOT NULL,
    PRIMARY KEY (run_id, stage, turn, attempt_no),
    FOREIGN KEY (run_id, stage, turn, attempt_no)
        REFERENCES attempt (run_id, stage, turn, attempt_no) ON DELETE RESTRICT,
    CONSTRAINT transcript_uncompressed_size_bytes_check CHECK (uncompressed_size_bytes >= 0)
);

CREATE TABLE dispatcher_state (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE,
    paused BOOLEAN NOT NULL,
    max_in_flight INTEGER NOT NULL,
    breaker_open_until TIMESTAMPTZ,
    breaker_reason TEXT,
    in_flight JSONB NOT NULL DEFAULT '[]'::JSONB,
    next_ticket_id BIGINT REFERENCES ticket (id) ON DELETE SET NULL,
    written_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT dispatcher_state_singleton_check CHECK (singleton),
    CONSTRAINT dispatcher_state_max_in_flight_check CHECK (max_in_flight >= 1),
    CONSTRAINT dispatcher_state_in_flight_array_check CHECK (JSONB_TYPEOF(in_flight) = 'array')
);

INSERT INTO dispatcher_state (singleton, paused, max_in_flight)
VALUES (TRUE, FALSE, 3);

-- +goose Down
DROP TABLE dispatcher_state;
DROP TABLE transcript;
DROP TABLE attempt;
DROP TABLE step;
DROP TABLE run;
DROP TABLE ticket_edge;
DROP TABLE ticket;
