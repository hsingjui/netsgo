-- Name: 013_global_passkey_challenges
-- Description: Decouple discoverable Passkey login challenges from candidate user lifecycles.
-- CreatedAt: 2026-08-18T00:00:00Z

-- Up:
CREATE TABLE admin_auth_challenges_global_login (
	id TEXT PRIMARY KEY,
	user_id TEXT REFERENCES users(id) ON DELETE CASCADE,
	kind TEXT NOT NULL,
	session_json TEXT NOT NULL,
	metadata_json TEXT NOT NULL DEFAULT '{}',
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	CHECK (user_id IS NOT NULL OR kind = 'passkey_login')
);

INSERT INTO admin_auth_challenges_global_login (
	id, user_id, kind, session_json, metadata_json, created_at, expires_at
)
SELECT id, user_id, kind, session_json, metadata_json, created_at, expires_at
FROM admin_auth_challenges;

DROP TABLE admin_auth_challenges;
ALTER TABLE admin_auth_challenges_global_login RENAME TO admin_auth_challenges;
CREATE INDEX idx_admin_auth_challenges_user_kind ON admin_auth_challenges(user_id, kind);
CREATE INDEX idx_admin_auth_challenges_expires ON admin_auth_challenges(expires_at);

-- Down:
