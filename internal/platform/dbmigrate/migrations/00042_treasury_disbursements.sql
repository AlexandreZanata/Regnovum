-- +goose Up
-- 00042 records governed Treasury disbursements (P34-T05): one row per
-- disbursement act with its allowlisted purpose, origin vault,
-- beneficiary, two distinct approvers and settling transfer. The row is
-- the audit trail the single disbursement port writes in the same
-- transaction as the legs, so money never moves without its governors
-- named beside it.
--
-- Invariants (docs/reino/RESPOSTAS.md item 23, Q23 proposta):
-- 1. Additive only: one new table reusing the append-only trigger
--    function from 00033. No existing row is touched. Amounts stay
--    explicit per act: Q23 is PENDENTE, so no budget, share or default
--    purpose amount lives here.
-- 2. One act settles at most once: disbursement_key is UNIQUE, so a
--    duplicated approval replays instead of paying twice.
-- 3. Two governors, neither the beneficiary: approver_one and
--    approver_two are distinct accounts and both differ from the
--    beneficiary, enforced by CHECKs, so partial credentials (NULL) and
--    self-approval die at 23514/23502 before any leg.
-- 4. Closed purpose and vault vocabularies: purpose admits only the
--    obligation classes the phase file names, and origin_vault admits
--    only Treasury vault labels, so an invalid purpose or a non-vault
--    origin never reaches the journal.
-- 5. History is retained and immutable: the 00033 trigger function
--    refuses UPDATE and DELETE for every role including the owner, and
--    transfers reference the row with ON DELETE RESTRICT.
-- 6. Least privilege: arena_owner owns the object; arena_app may read
--    and append disbursement rows, never rewrite them.

CREATE TABLE IF NOT EXISTS app.treasury_disbursements (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    disbursement_key text NOT NULL,
    origin_vault text NOT NULL,
    beneficiary_account_id uuid NOT NULL REFERENCES app.accounts(id) ON DELETE RESTRICT,
    purpose text NOT NULL,
    amount_milli bigint NOT NULL,
    approver_one uuid NOT NULL REFERENCES app.accounts(id) ON DELETE RESTRICT,
    approver_two uuid NOT NULL REFERENCES app.accounts(id) ON DELETE RESTRICT,
    transfer_id uuid NOT NULL,
    decided_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT treasury_disbursements_key_check CHECK (trim(disbursement_key) <> ''),
    CONSTRAINT treasury_disbursements_key_unique UNIQUE (disbursement_key),
    CONSTRAINT treasury_disbursements_vault_check CHECK (origin_vault IN (
        'main',
        'sovereign_reserve',
        'commercial_stock',
        'operating_cash',
        'free_treasury'
    )),
    CONSTRAINT treasury_disbursements_purpose_check CHECK (purpose IN (
        'sale_settlement',
        'crumb_distribution',
        'compensation',
        'due_payment'
    )),
    CONSTRAINT treasury_disbursements_amount_check CHECK (amount_milli BETWEEN 1 AND 2100000000000),
    CONSTRAINT treasury_disbursements_two_governors_check CHECK (approver_one <> approver_two),
    CONSTRAINT treasury_disbursements_no_self_approval_check CHECK (
        beneficiary_account_id <> approver_one AND beneficiary_account_id <> approver_two
    ),
    CONSTRAINT treasury_disbursements_transfer_unique UNIQUE (transfer_id)
);

COMMENT ON TABLE app.treasury_disbursements IS 'Governed Treasury disbursement acts: allowlisted purpose, origin vault, beneficiary, two distinct approvers and settling transfer, append-only';
COMMENT ON COLUMN app.treasury_disbursements.disbursement_key IS 'Idempotency anchor of the act: a duplicated approval replays instead of paying twice';
COMMENT ON COLUMN app.treasury_disbursements.transfer_id IS 'Journal transfer that moved the amount: every row carries its legs';

CREATE INDEX IF NOT EXISTS treasury_disbursements_beneficiary_idx ON app.treasury_disbursements (beneficiary_account_id, decided_at DESC);

DROP TRIGGER IF EXISTS treasury_disbursements_immutable ON app.treasury_disbursements;
CREATE TRIGGER treasury_disbursements_immutable
    BEFORE UPDATE OR DELETE ON app.treasury_disbursements
    FOR EACH ROW EXECUTE FUNCTION app.economy_forbid_mutation();

ALTER TABLE app.treasury_disbursements OWNER TO arena_owner;

GRANT SELECT, INSERT ON app.treasury_disbursements TO arena_app;

-- +goose Down
DROP TRIGGER IF EXISTS treasury_disbursements_immutable ON app.treasury_disbursements;
DROP TABLE IF EXISTS app.treasury_disbursements;
