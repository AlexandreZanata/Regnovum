-- +goose Up
-- 00055 binds the economy core to season books without moving money
-- (P46-T04): custodies, journal legs, intentions and holds carry an
-- explicit season_key anchored to app.seasons, custody identity
-- becomes (kind, label, book), intentions replay per book, and
-- composite keys forbid legs or holds mixing a custody of another
-- book. Pre-season rows bind the explicitly inactive compat-legacy
-- namespace by column default, without rewriting history; ledgers
-- keep their counts and hashes byte for byte.
--
-- Invariants (docs/reino/TEMPORADAS_SUCESSAO.md §4):
-- 1. Additive only: four columns plus uniqueness swaps on tables the
--    phase owns. No leg, custody, intention or hold is rewritten;
--    the old diary keeps its counts and hashes, so S never moves
--    and no silent conversion or new mint appears.
-- 2. Custody identity is per book: UNIQUE(kind, label, season_key)
--    replaces the global pair unique. The same label in another
--    book is another custody; composite UNIQUE(id, season_key)
--    anchors the cross-table keys below.
-- 3. No mixed books: legs and holds reference
--    (custody_id, season_key) against custodies(id, season_key), so
--    a leg or hold mixing a custody of another book dies on the
--    foreign key. Intentions replay per book: the triple and the
--    transfer uniques gain the season, so the same key in another
--    book settles its own outcome instead of redirecting a replay.
-- 4. Compatibility namespace: pre-season rows bind compat-legacy
--    by default, explicitly inactive and never activated. Future
--    Genesises and mutations name their book explicitly at the
--    port; making the season mandatory there is the application
--    layer of this same task.
-- 5. Least privilege: no grant changes. arena_app keeps the exact
--    DML it already holds on these tables, never more.
--
-- Consumer tables outside the economy core (billing, pricing,
-- metering, commerce, crumbs, wallet, charter) keep their shape
-- here; their season references land in the family migrations of
-- P46-T05–T08 with per-family tests.

ALTER TABLE app.economy_custodies
    ADD COLUMN IF NOT EXISTS season_key text NOT NULL DEFAULT 'compat-legacy' REFERENCES app.seasons(season_key) ON DELETE RESTRICT;

ALTER TABLE app.economy_custodies DROP CONSTRAINT IF EXISTS economy_custodies_kind_label_unique;

ALTER TABLE app.economy_custodies
    ADD CONSTRAINT economy_custodies_book_unique UNIQUE (kind, label, season_key);

ALTER TABLE app.economy_custodies
    ADD CONSTRAINT economy_custodies_identity_unique UNIQUE (id, season_key);

COMMENT ON COLUMN app.economy_custodies.season_key IS 'Book identity of this custody: the same label in another book is another custody; pre-season rows bind the explicitly inactive compat-legacy namespace';

ALTER TABLE app.economy_entries
    ADD COLUMN IF NOT EXISTS season_key text NOT NULL DEFAULT 'compat-legacy' REFERENCES app.seasons(season_key) ON DELETE RESTRICT;

ALTER TABLE app.economy_entries
    ADD CONSTRAINT economy_entries_book_custody_fkey FOREIGN KEY (custody_id, season_key)
    REFERENCES app.economy_custodies (id, season_key) ON DELETE RESTRICT;

COMMENT ON COLUMN app.economy_entries.season_key IS 'Book of this leg: the composite key refuses legs mixing a custody of another book';

ALTER TABLE app.economy_intentions
    ADD COLUMN IF NOT EXISTS season_key text NOT NULL DEFAULT 'compat-legacy' REFERENCES app.seasons(season_key) ON DELETE RESTRICT;

ALTER TABLE app.economy_intentions DROP CONSTRAINT IF EXISTS economy_intentions_triple_unique;

ALTER TABLE app.economy_intentions DROP CONSTRAINT IF EXISTS economy_intentions_transfer_unique;

ALTER TABLE app.economy_intentions
    ADD CONSTRAINT economy_intentions_book_triple_unique UNIQUE (intention_key, actor, operation, season_key);

ALTER TABLE app.economy_intentions
    ADD CONSTRAINT economy_intentions_book_transfer_unique UNIQUE (transfer_id, season_key);

COMMENT ON COLUMN app.economy_intentions.season_key IS 'Book of this intention: replays resolve per book, never redirecting another book outcome';

ALTER TABLE app.economy_holds
    ADD COLUMN IF NOT EXISTS season_key text NOT NULL DEFAULT 'compat-legacy' REFERENCES app.seasons(season_key) ON DELETE RESTRICT;

ALTER TABLE app.economy_holds
    ADD CONSTRAINT economy_holds_book_owner_fkey FOREIGN KEY (owner_custody_id, season_key)
    REFERENCES app.economy_custodies (id, season_key) ON DELETE RESTRICT;

ALTER TABLE app.economy_holds
    ADD CONSTRAINT economy_holds_book_hold_fkey FOREIGN KEY (hold_custody_id, season_key)
    REFERENCES app.economy_custodies (id, season_key) ON DELETE RESTRICT;

COMMENT ON COLUMN app.economy_holds.season_key IS 'Book of this hold: owner and hold custodies must live in the same book';

-- +goose Down
ALTER TABLE app.economy_holds DROP CONSTRAINT IF EXISTS economy_holds_book_hold_fkey;
ALTER TABLE app.economy_holds DROP CONSTRAINT IF EXISTS economy_holds_book_owner_fkey;
ALTER TABLE app.economy_holds DROP COLUMN IF EXISTS season_key;
ALTER TABLE app.economy_intentions DROP CONSTRAINT IF EXISTS economy_intentions_book_transfer_unique;
ALTER TABLE app.economy_intentions DROP CONSTRAINT IF EXISTS economy_intentions_book_triple_unique;
ALTER TABLE app.economy_intentions ADD CONSTRAINT economy_intentions_transfer_unique UNIQUE (transfer_id);
ALTER TABLE app.economy_intentions ADD CONSTRAINT economy_intentions_triple_unique UNIQUE (intention_key, actor, operation);
ALTER TABLE app.economy_intentions DROP COLUMN IF EXISTS season_key;
ALTER TABLE app.economy_entries DROP CONSTRAINT IF EXISTS economy_entries_book_custody_fkey;
ALTER TABLE app.economy_entries DROP COLUMN IF EXISTS season_key;
ALTER TABLE app.economy_custodies DROP CONSTRAINT IF EXISTS economy_custodies_identity_unique;
ALTER TABLE app.economy_custodies DROP CONSTRAINT IF EXISTS economy_custodies_book_unique;
ALTER TABLE app.economy_custodies ADD CONSTRAINT economy_custodies_kind_label_unique UNIQUE (kind, label);
ALTER TABLE app.economy_custodies DROP COLUMN IF EXISTS season_key;
