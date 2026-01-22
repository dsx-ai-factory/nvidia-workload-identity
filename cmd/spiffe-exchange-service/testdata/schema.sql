CREATE TABLE IF NOT EXISTS keysets (
    trust_domain VARCHAR(255) NOT NULL,
    key_id VARCHAR(255) NOT NULL,
    algorithm VARCHAR(50) NOT NULL,
    encrypted_keyset BYTEA NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    PRIMARY KEY (trust_domain, key_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_keysets_one_active_per_domain ON keysets(trust_domain) WHERE is_active = TRUE;
