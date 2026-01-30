CREATE TABLE IF NOT EXISTS domain_keys (
    trust_domain VARCHAR(255) NOT NULL,
    key_id VARCHAR(255) NOT NULL,
    algorithm VARCHAR(50) NOT NULL,
    encrypted_key TEXT NOT NULL,
    master_key_id VARCHAR(255) NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    PRIMARY KEY (trust_domain, key_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_domain_keys_one_active_per_domain ON domain_keys(trust_domain) WHERE is_active = TRUE;
