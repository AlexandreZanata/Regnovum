-- +goose Up
-- 00036 anchors every economy custody to its holder (P32-T06):
-- owner_account_id ties a custody to one app account, so private
-- statements can prove the caller owns the journal it reads.
--
-- Notes:
-- 1. Additive only: one nullable column, one foreign key, one index. No
--    row is touched and no immutability trigger fires on DDL.
-- 2. System custodies (Treasury) keep a NULL owner: the private
--    statement query refuses them, and reconciliation reads the journal
--    at a coarser level instead.
-- 3. The owner is set once at INSERT and never rewritten: UPDATE on the
--    row is already refused by the 00033 trigger, so ownership cannot
--    drift to another holder. Suspension lives on app.accounts.status,
--    which the statement query rechecks on every read.

ALTER TABLE app.economy_custodies
    ADD COLUMN IF NOT EXISTS owner_account_id uuid NULL REFERENCES app.accounts(id) ON DELETE RESTRICT;

CREATE INDEX IF NOT EXISTS economy_custodies_owner_idx ON app.economy_custodies (owner_account_id);

COMMENT ON COLUMN app.economy_custodies.owner_account_id IS 'Holder account of this custody, set once at creation; NULL marks a system custody no private statement may read';

-- +goose Down
DROP INDEX IF EXISTS app.economy_custodies_owner_idx;
ALTER TABLE app.economy_custodies DROP COLUMN IF EXISTS owner_account_id;
