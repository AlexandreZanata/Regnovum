-- +goose Up
-- 00063 establishes account key credentials (P57-T01):
-- accounts can authenticate with a 32-character CSPRNG account key without email.
-- key_lookup is the unseeded SHA-256 for O(1) indexed lookup.
-- key_salt is a 16-byte cryptographically random hex string.
-- key_hash is SHA-256(key_salt + '\x00' + canonical_key).

CREATE TABLE IF NOT EXISTS app.account_key_credentials (
    account_id uuid PRIMARY KEY REFERENCES app.accounts(id) ON DELETE CASCADE,
    username_hash bytea NOT NULL,
    key_lookup bytea NOT NULL,
    key_salt text NOT NULL,
    key_hash bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT account_key_credentials_key_lookup_unique UNIQUE (key_lookup)
);

CREATE INDEX IF NOT EXISTS account_key_credentials_key_lookup_idx ON app.account_key_credentials (key_lookup);
CREATE INDEX IF NOT EXISTS account_key_credentials_username_hash_idx ON app.account_key_credentials (username_hash);

COMMENT ON TABLE app.account_key_credentials IS 'Hashed account key credentials for email-free authentication';
COMMENT ON COLUMN app.account_key_credentials.key_lookup IS 'SHA-256 of canonical key for blind constant-time index lookup';
COMMENT ON COLUMN app.account_key_credentials.key_salt IS '16-byte random salt encoded as hexadecimal string';
COMMENT ON COLUMN app.account_key_credentials.key_hash IS 'SHA-256 of salt, zero byte separator and canonical key';

ALTER TABLE app.account_key_credentials OWNER TO arena_owner;

GRANT SELECT, INSERT, UPDATE, DELETE ON app.account_key_credentials TO arena_app;

-- +goose Down
DROP TABLE IF EXISTS app.account_key_credentials;
