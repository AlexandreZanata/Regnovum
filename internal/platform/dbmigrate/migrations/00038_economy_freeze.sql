-- +goose Up
-- 00038 records reconciliation incidents and the read-only mode (P32-T08):
-- app.economy_incidents is the append-only audit trail of conservation
-- breaks and their compensated resolutions; app.economy_mode carries the
-- single frozen flag every mutation path rechecks.
--
-- Invariants (docs/reino/LEDGER_CONTRACT.md §5, THR-ECON-20/23):
-- 1. Every freeze names its incident: the CHECK refuses a frozen mode
--    without an incident row, so read-only mode never arrives silently.
-- 2. Incidents are append-only: the trigger refuses UPDATE and DELETE
--    for every role including the owner. Resolutions are new rows
--    linked to the incident they close, never edits.
-- 3. The mode row is the only mutable economy state: it flips only
--    between frozen-with-incident and open, always beside the incident
--    row written in the same transaction.
-- 4. Least privilege: arena_app reads mode and incidents and appends
--    incidents; it never deletes them.

CREATE TABLE IF NOT EXISTS app.economy_incidents (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    reason text NOT NULL,
    detail text NOT NULL,
    resolves uuid NULL REFERENCES app.economy_incidents(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT economy_incidents_reason_check CHECK (trim(reason) <> ''),
    CONSTRAINT economy_incidents_detail_check CHECK (trim(detail) <> '')
);

COMMENT ON TABLE app.economy_incidents IS 'Audit trail of conservation breaks and their compensated resolutions; rows are never edited or erased';
COMMENT ON COLUMN app.economy_incidents.resolves IS 'Incident this resolution closes; NULL marks the break itself';

CREATE TABLE IF NOT EXISTS app.economy_mode (
    singleton boolean NOT NULL DEFAULT true,
    frozen boolean NOT NULL DEFAULT false,
    incident_id uuid NULL REFERENCES app.economy_incidents(id) ON DELETE RESTRICT,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT economy_mode_singleton_check CHECK (singleton),
    CONSTRAINT economy_mode_singleton_unique UNIQUE (singleton),
    CONSTRAINT economy_mode_frozen_names_incident CHECK (NOT frozen OR incident_id IS NOT NULL)
);

COMMENT ON TABLE app.economy_mode IS 'Single read-only flag of the economy: mutations recheck it, reads never do';
COMMENT ON COLUMN app.economy_mode.incident_id IS 'Break that froze the book, or the last one after a compensated resolution';

-- +goose StatementBegin
-- Incidents are the memory of every break and fix: nothing about them is
-- ever edited or erased.
CREATE OR REPLACE FUNCTION app.economy_incidents_forbid_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'reconciliation incidents are final'
        USING ERRCODE = '23514', CONSTRAINT = 'economy_incidents_immutable';
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS economy_incidents_immutable ON app.economy_incidents;
CREATE TRIGGER economy_incidents_immutable
    BEFORE UPDATE OR DELETE ON app.economy_incidents
    FOR EACH ROW EXECUTE FUNCTION app.economy_incidents_forbid_mutation();

ALTER TABLE app.economy_incidents OWNER TO arena_owner;
ALTER TABLE app.economy_mode OWNER TO arena_owner;
ALTER FUNCTION app.economy_incidents_forbid_mutation() OWNER TO arena_owner;

-- Runtime surface. The reconciler appends incidents and flips the mode;
-- readers only read. History is never rewritten.
GRANT SELECT, INSERT ON app.economy_incidents TO arena_app;
GRANT SELECT, INSERT, UPDATE ON app.economy_mode TO arena_app;

-- +goose Down
DROP TRIGGER IF EXISTS economy_incidents_immutable ON app.economy_incidents;
DROP FUNCTION IF EXISTS app.economy_incidents_forbid_mutation();
DROP TABLE IF EXISTS app.economy_mode;
DROP TABLE IF EXISTS app.economy_incidents;
