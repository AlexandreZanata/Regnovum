-- +goose Up
-- 00040 admits the conversion debit into the legacy vocabulary (P33-T04):
-- the wallet_operations type CHECK gains debit_conversion alongside the
-- nine existing types; every other value stays rejected.
--
-- A conversion extinguishes opted-in legacy credit through a compensating
-- debit written in the same transaction as the Genesis credit it funds.
-- The old CHECK is replaced (DROP+ADD, the expand/contract swap the
-- compatibility matrix allows) because CHECKs are metadata, not data:
-- no row is touched and no history is rewritten.

ALTER TABLE app.wallet_operations
    DROP CONSTRAINT IF EXISTS wallet_operations_type_check;
ALTER TABLE app.wallet_operations
    ADD CONSTRAINT wallet_operations_type_check CHECK (operation_type IN (
        'credit_free',
        'credit_member',
        'credit_purchase',
        'credit_refund',
        'credit_admin',
        'debit_argument',
        'debit_admin',
        'debit_refund',
        'debit_conversion',
        'expire_free'
    ));

-- +goose Down
ALTER TABLE app.wallet_operations
    DROP CONSTRAINT IF EXISTS wallet_operations_type_check;
ALTER TABLE app.wallet_operations
    ADD CONSTRAINT wallet_operations_type_check CHECK (operation_type IN (
        'credit_free',
        'credit_member',
        'credit_purchase',
        'credit_refund',
        'credit_admin',
        'debit_argument',
        'debit_admin',
        'debit_refund',
        'expire_free'
    ));
