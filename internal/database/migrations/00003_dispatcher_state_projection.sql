-- +goose Up
-- #551: the dispatcher writes its own state every tick, not just paused/
-- max_in_flight. Everything variable-shaped (the operator's config, the
-- breaker) moves into one JSONB column each rather than growing a flat
-- column per field forever, and candidates/free_slots are new: the
-- eligible-ticket order and slot count nothing records today.
--
-- next_ticket_id (BIGINT REFERENCES ticket) is dropped, not kept alongside
-- candidates: it was scaffolded for the ADR-0012 Ticket-reading dispatcher,
-- but #551 is explicit that THIS dispatcher still reads GitHub issues, so a
-- foreign key into the ticket table is the wrong shape for what it claims
-- next. candidates below holds GitHub issue numbers instead.
ALTER TABLE dispatcher_state
    ADD COLUMN config JSONB NOT NULL DEFAULT '{}'::JSONB,
    ADD COLUMN config_error TEXT NOT NULL DEFAULT '',
    ADD COLUMN breaker JSONB NOT NULL DEFAULT '{}'::JSONB,
    ADD COLUMN candidates JSONB NOT NULL DEFAULT '[]'::JSONB,
    ADD COLUMN free_slots INTEGER NOT NULL DEFAULT 0;

ALTER TABLE dispatcher_state
    ADD CONSTRAINT dispatcher_state_candidates_array_check CHECK (JSONB_TYPEOF(candidates) = 'array'),
    ADD CONSTRAINT dispatcher_state_free_slots_check CHECK (free_slots >= 0);

ALTER TABLE dispatcher_state DROP CONSTRAINT dispatcher_state_max_in_flight_check;
ALTER TABLE dispatcher_state DROP COLUMN paused;
ALTER TABLE dispatcher_state DROP COLUMN max_in_flight;
ALTER TABLE dispatcher_state DROP COLUMN breaker_open_until;
ALTER TABLE dispatcher_state DROP COLUMN breaker_reason;
ALTER TABLE dispatcher_state DROP COLUMN next_ticket_id;

-- +goose Down
ALTER TABLE dispatcher_state
    ADD COLUMN paused BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN max_in_flight INTEGER NOT NULL DEFAULT 3,
    ADD COLUMN breaker_open_until TIMESTAMPTZ,
    ADD COLUMN breaker_reason TEXT,
    ADD COLUMN next_ticket_id BIGINT REFERENCES ticket (id) ON DELETE SET NULL;

ALTER TABLE dispatcher_state
    ADD CONSTRAINT dispatcher_state_max_in_flight_check CHECK (max_in_flight >= 1);

ALTER TABLE dispatcher_state
    DROP CONSTRAINT dispatcher_state_candidates_array_check,
    DROP CONSTRAINT dispatcher_state_free_slots_check;

ALTER TABLE dispatcher_state
    DROP COLUMN config,
    DROP COLUMN config_error,
    DROP COLUMN breaker,
    DROP COLUMN candidates,
    DROP COLUMN free_slots;
