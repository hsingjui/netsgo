-- Name: 007_api_key_lookup_digest
-- Description: Add API key lookup digest for candidate selection before bcrypt verification.
-- CreatedAt: 2026-06-15T00:00:00Z

-- Up:
ALTER TABLE api_keys ADD COLUMN lookup_digest TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_api_keys_lookup_digest ON api_keys(lookup_digest);

-- Down:
