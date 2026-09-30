-- +goose Up
-- 00053 records disguised-trade reviews without automatic seizure
-- (P37-T06): one row per flagged gift with the caller review token,
-- the closed reason, the minimized evidence hash and the reporter,
-- plus one row per review step (contest, dismissal, confirmation).
-- Flagging and contesting move no value: even a confirmed fraud
-- records an act with the contractual basis, the trail hash, the
-- explicit floor(10%) charge owed and the appeal window, never an
-- automatic debit. A dismissed flag is the false-positive path and
-- moves nothing at all.
--
-- Invariants:
-- 1. Additive only: two new tables reusing the contract immutability
--    trigger. No existing row is touched. No ledger leg is written
--    by any review step: journals stay identical, so S never moves
--    and no present is taxed by flagging.
-- 2. One key flags once per transfer: (transfer_id, review_key) is
--    UNIQUE, so a retried flag replays instead of reviewing twice.
--    Divergent terms under one key conflict.
-- 3. Gifts only: only settled 'gift' transfers flag. Formal trades
--    already bear tithe at settlement, refunds compensate through
--    linked entries and treasury movements are not payments.
-- 4. Minimized proof: evidence and trail travel as 64 lowercase hex
--    digests, never PII content; the reason admits exactly the four
--    closed signals (contract, announcement, delivery, recurrence).
-- 5. Contest by parties only: enforced in the settling transaction
--    (payer or payee of the flagged gift); terminal steps refuse a
--    second outcome and terminal reviews never reopen.
-- 6. History is retained and immutable as rows: the contract
--    registry trigger function refuses UPDATE and DELETE for every
--    role including the owner. Status derives from steps, never by
--    rewriting a flag.
-- 7. Least privilege: arena_owner owns the objects; arena_app may
--    read and append review and step rows, never rewrite them.

CREATE TABLE IF NOT EXISTS app.commerce_disguise_reviews (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    transfer_id uuid NOT NULL REFERENCES app.commerce_transfers(id) ON DELETE RESTRICT,
    review_key text NOT NULL,
    reason text NOT NULL,
    evidence_hash text NOT NULL,
    reporter text NOT NULL,
    posted_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT commerce_disguise_reviews_key_check CHECK (trim(review_key) <> ''),
    CONSTRAINT commerce_disguise_reviews_reason_check CHECK (reason IN ('contract', 'announcement', 'delivery', 'recurrence')),
    CONSTRAINT commerce_disguise_reviews_evidence_check CHECK (evidence_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT commerce_disguise_reviews_reporter_check CHECK (trim(reporter) <> ''),
    CONSTRAINT commerce_disguise_reviews_transfer_key_unique UNIQUE (transfer_id, review_key)
);

COMMENT ON TABLE app.commerce_disguise_reviews IS 'Flagged gifts under review for disguised trade: caller token, closed reason, minimized evidence digest and reporter, append-only, never moves value';
COMMENT ON COLUMN app.commerce_disguise_reviews.review_key IS 'Caller operation token scoped by transfer: a retry replays instead of reviewing twice';
COMMENT ON COLUMN app.commerce_disguise_reviews.evidence_hash IS 'Minimized proof digest (64 lowercase hex): no PII content ever stored';

CREATE TABLE IF NOT EXISTS app.commerce_disguise_steps (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    review_id uuid NOT NULL REFERENCES app.commerce_disguise_reviews(id) ON DELETE RESTRICT,
    action text NOT NULL,
    decided_by text NOT NULL,
    basis text NULL,
    trail_hash text NULL,
    charge_milli bigint NULL,
    appeal_until timestamptz NULL,
    posted_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT commerce_disguise_steps_action_check CHECK (action IN ('contest', 'dismiss', 'confirm')),
    CONSTRAINT commerce_disguise_steps_decided_check CHECK (trim(decided_by) <> ''),
    CONSTRAINT commerce_disguise_steps_basis_check CHECK (basis IS NULL OR trim(basis) <> ''),
    CONSTRAINT commerce_disguise_steps_trail_check CHECK (trail_hash IS NULL OR trail_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT commerce_disguise_steps_charge_check CHECK (charge_milli IS NULL OR charge_milli BETWEEN 0 AND 9223372036854775807),
    CONSTRAINT commerce_disguise_steps_shape_check CHECK (
        (action = 'contest' AND basis IS NULL AND trail_hash IS NULL AND charge_milli IS NULL AND appeal_until IS NULL) OR
        (action = 'dismiss' AND basis IS NULL AND trail_hash IS NULL AND charge_milli IS NULL AND appeal_until IS NOT NULL) OR
        (action = 'confirm' AND basis IS NOT NULL AND trail_hash IS NOT NULL AND charge_milli IS NOT NULL AND appeal_until IS NOT NULL)
    )
);

COMMENT ON TABLE app.commerce_disguise_steps IS 'Review lifecycle steps: party contest, false-positive dismissal and confirmed act with basis, trail, explicit charge and appeal window, append-only, never moves value';
COMMENT ON COLUMN app.commerce_disguise_steps.basis IS 'Contractual basis of a confirmed act: required only for confirm, never for contest or dismissal';
COMMENT ON COLUMN app.commerce_disguise_steps.charge_milli IS 'Explicit floor(10%) charge owed on confirmation: recorded debt with appeal, never an automatic debit';

CREATE INDEX IF NOT EXISTS commerce_disguise_reviews_transfer_idx ON app.commerce_disguise_reviews (transfer_id, posted_at);
CREATE INDEX IF NOT EXISTS commerce_disguise_steps_review_idx ON app.commerce_disguise_steps (review_id, posted_at);

-- One contest per review and one terminal outcome per review: a
-- contested flag still settles once, and two conflicting resolutions
-- never both commit even under concurrency.
CREATE UNIQUE INDEX IF NOT EXISTS commerce_disguise_steps_contest_unique
    ON app.commerce_disguise_steps (review_id)
    WHERE action = 'contest';

CREATE UNIQUE INDEX IF NOT EXISTS commerce_disguise_steps_terminal_unique
    ON app.commerce_disguise_steps (review_id)
    WHERE action IN ('dismiss', 'confirm');

DROP TRIGGER IF EXISTS commerce_disguise_reviews_immutable ON app.commerce_disguise_reviews;
CREATE TRIGGER commerce_disguise_reviews_immutable
    BEFORE UPDATE OR DELETE ON app.commerce_disguise_reviews
    FOR EACH ROW EXECUTE FUNCTION app.commerce_contract_forbid();

DROP TRIGGER IF EXISTS commerce_disguise_steps_immutable ON app.commerce_disguise_steps;
CREATE TRIGGER commerce_disguise_steps_immutable
    BEFORE UPDATE OR DELETE ON app.commerce_disguise_steps
    FOR EACH ROW EXECUTE FUNCTION app.commerce_contract_forbid();

ALTER TABLE app.commerce_disguise_reviews OWNER TO arena_owner;
ALTER TABLE app.commerce_disguise_steps OWNER TO arena_owner;

GRANT SELECT, INSERT ON app.commerce_disguise_reviews TO arena_app;
GRANT SELECT, INSERT ON app.commerce_disguise_steps TO arena_app;

-- +goose Down
DROP TRIGGER IF EXISTS commerce_disguise_steps_immutable ON app.commerce_disguise_steps;
DROP TRIGGER IF EXISTS commerce_disguise_reviews_immutable ON app.commerce_disguise_reviews;
DROP TABLE IF EXISTS app.commerce_disguise_steps;
DROP TABLE IF EXISTS app.commerce_disguise_reviews;
