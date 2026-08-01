-- +goose Up
-- Target active ownership is established only by the claim transaction. Legacy
-- working and review rows remain valid without an active Run during cutover.
ALTER TABLE ticket
    ADD CONSTRAINT ticket_active_state_ownership_check
    CHECK ((state = 'active') = (active_run_id IS NOT NULL));

-- +goose Down
ALTER TABLE ticket
    DROP CONSTRAINT ticket_active_state_ownership_check;
