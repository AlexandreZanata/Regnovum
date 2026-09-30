-- +goose Up
-- 00041 constrains Treasury custody labels to the closed vault vocabulary
-- (P34-T01): the Genesis home and the four exclusive vaults — Sovereign
-- Reserve, Commercial Stock, Operating Cash and Free Treasury — are
-- structural names, so the same unit can never be counted in two vaults
-- under two spellings.
--
-- Invariants (docs/reino/RESPOSTAS.md item 23, Q23 proposta):
-- 1. Additive only: one CHECK swapped by DROP+ADD (the expand/contract
--    swap the compatibility matrix allows, as in 00040), because CHECKs
--    are metadata, not data: no row is touched and no history is
--    rewritten. Amounts and percentages stay out of this migration:
--    Q23 is PENDENTE, so no reserve share is vigente here.
-- 2. Exclusivity is structural: custodies stay (kind, label) UNIQUE and
--    journal legs stay keyed by custody_id, so each unit lives in exactly
--    one vault by construction, never by convention.
-- 3. Obligations and escrows keep their identified custodies
--    (escrow/contract/title kinds from 00033): this CHECK touches the
--    treasury kind only, so user, escrow, contract and title labels —
--    including account-bound ones — are unaffected.
-- 4. Least privilege unchanged: no GRANT is added or removed.

ALTER TABLE app.economy_custodies
    DROP CONSTRAINT IF EXISTS economy_treasury_label_check;
ALTER TABLE app.economy_custodies
    ADD CONSTRAINT economy_treasury_label_check CHECK (kind <> 'treasury' OR label IN (
        'main',
        'sovereign_reserve',
        'commercial_stock',
        'operating_cash',
        'free_treasury'
    ));

-- +goose Down
ALTER TABLE app.economy_custodies
    DROP CONSTRAINT IF EXISTS economy_treasury_label_check;
