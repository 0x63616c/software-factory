-- +goose Up
-- Keep target ownership referentially exact: an active Ticket may point only
-- at a Run whose ticket_id names that same Ticket. The previous UUID-only
-- foreign key allowed a valid Run belonging to a different Ticket.
ALTER TABLE run
    ADD CONSTRAINT run_ticket_id_id_key UNIQUE (ticket_id, id);

ALTER TABLE ticket
    DROP CONSTRAINT ticket_active_run_id_fkey,
    ADD CONSTRAINT ticket_active_run_same_ticket_fkey
    FOREIGN KEY (id, active_run_id) REFERENCES run (ticket_id, id) ON DELETE RESTRICT;

-- +goose Down
ALTER TABLE ticket
    DROP CONSTRAINT ticket_active_run_same_ticket_fkey,
    ADD CONSTRAINT ticket_active_run_id_fkey
    FOREIGN KEY (active_run_id) REFERENCES run (id) ON DELETE RESTRICT;

ALTER TABLE run
    DROP CONSTRAINT run_ticket_id_id_key;
