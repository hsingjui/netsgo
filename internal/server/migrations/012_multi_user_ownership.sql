-- Name: 012_multi_user_ownership
-- Description: Replace administrator-only identities with users and backfill resource ownership.
-- CreatedAt: 2026-08-04T00:00:00Z

-- Up:
CREATE TEMP TABLE IF NOT EXISTS multi_user_migration_validation (
	name TEXT PRIMARY KEY,
	value INTEGER NOT NULL
);
DELETE FROM multi_user_migration_validation;

INSERT INTO multi_user_migration_validation (name, value)
SELECT 'admin_users_source', COUNT(*) FROM admin_users;
INSERT INTO multi_user_migration_validation (name, value)
SELECT 'admin_sessions_source', COUNT(*) FROM admin_sessions;
INSERT INTO multi_user_migration_validation (name, value)
SELECT 'admin_totp_recovery_codes_source', COUNT(*) FROM admin_totp_recovery_codes;
INSERT INTO multi_user_migration_validation (name, value)
SELECT 'admin_totp_recovery_codes_orphaned', COUNT(*)
FROM admin_totp_recovery_codes c
LEFT JOIN admin_users u ON u.id = c.user_id
WHERE u.id IS NULL;
INSERT INTO multi_user_migration_validation (name, value)
SELECT 'admin_passkeys_source', COUNT(*) FROM admin_passkeys;
INSERT INTO multi_user_migration_validation (name, value)
SELECT 'admin_passkeys_orphaned', COUNT(*)
FROM admin_passkeys p
LEFT JOIN admin_users u ON u.id = p.user_id
WHERE u.id IS NULL;
INSERT INTO multi_user_migration_validation (name, value)
SELECT 'admin_auth_challenges_source', COUNT(*) FROM admin_auth_challenges;
INSERT INTO multi_user_migration_validation (name, value)
SELECT 'admin_auth_challenges_orphaned', COUNT(*)
FROM admin_auth_challenges c
LEFT JOIN admin_users u ON u.id = c.user_id
WHERE u.id IS NULL;
INSERT INTO multi_user_migration_validation (name, value)
SELECT 'api_keys_source', COUNT(*) FROM api_keys;
INSERT INTO multi_user_migration_validation (name, value)
SELECT 'registered_clients_source', COUNT(*) FROM registered_clients;
INSERT INTO multi_user_migration_validation (name, value)
SELECT 'tunnels_source', COUNT(*) FROM tunnels;
INSERT INTO multi_user_migration_validation (name, value)
SELECT 'traffic_buckets_source', COUNT(*) FROM traffic_buckets;
INSERT INTO multi_user_migration_validation (name, value)
SELECT 'activity_events_source', COUNT(*) FROM activity_events;
INSERT INTO multi_user_migration_validation (name, value)
SELECT 'session_environment_mismatch_source', COUNT(*)
FROM activity_events
WHERE action = 'session_environment_mismatch';

CREATE TABLE users (
	id TEXT PRIMARY KEY,
	username TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	is_admin INTEGER NOT NULL DEFAULT 0 CHECK (is_admin IN (0, 1)),
	status TEXT NOT NULL DEFAULT 'active' CHECK (length(status) > 0),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	last_login TEXT,
	totp_enabled INTEGER NOT NULL DEFAULT 0 CHECK (totp_enabled IN (0, 1)),
	totp_secret TEXT NOT NULL DEFAULT ''
);

INSERT INTO users (
	id, username, password_hash, is_admin, status, created_at, updated_at,
	last_login, totp_enabled, totp_secret
)
SELECT
	id, username, password_hash, 1, 'active', created_at, created_at,
	last_login, totp_enabled, totp_secret
FROM admin_users;

INSERT INTO multi_user_migration_validation (name, value)
SELECT 'users_target', COUNT(*) FROM users;

CREATE INDEX idx_users_page ON users(created_at DESC, id DESC);
CREATE INDEX idx_users_status_page ON users(status, created_at DESC, id DESC);

CREATE TABLE user_sessions (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	ip TEXT NOT NULL DEFAULT '',
	user_agent TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_user_sessions_user ON user_sessions(user_id);
CREATE INDEX idx_user_sessions_expires ON user_sessions(expires_at);

INSERT INTO user_sessions (id, user_id, created_at, expires_at, ip, user_agent)
SELECT s.id, s.user_id, s.created_at, s.expires_at, s.ip, s.user_agent
FROM admin_sessions s
JOIN users u ON u.id = s.user_id;

INSERT INTO multi_user_migration_validation (name, value)
SELECT 'user_sessions_target', COUNT(*) FROM user_sessions;
INSERT INTO multi_user_migration_validation (name, value)
SELECT 'user_sessions_revoked', COUNT(*)
FROM admin_sessions s
LEFT JOIN users u ON u.id = s.user_id
WHERE u.id IS NULL;

CREATE TABLE admin_totp_recovery_codes_multi_user_migration (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	code_hash TEXT NOT NULL UNIQUE,
	created_at TEXT NOT NULL,
	used_at TEXT
);
INSERT INTO admin_totp_recovery_codes_multi_user_migration (id, user_id, code_hash, created_at, used_at)
SELECT c.id, c.user_id, c.code_hash, c.created_at, c.used_at
FROM admin_totp_recovery_codes c
JOIN users u ON u.id = c.user_id;
INSERT INTO multi_user_migration_validation (name, value)
SELECT 'admin_totp_recovery_codes_target', COUNT(*) FROM admin_totp_recovery_codes_multi_user_migration;

CREATE TABLE admin_passkeys_multi_user_migration (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	credential_id TEXT NOT NULL UNIQUE,
	credential_json TEXT NOT NULL,
	rp_id TEXT NOT NULL,
	origin TEXT NOT NULL,
	created_at TEXT NOT NULL,
	last_used_at TEXT
);
INSERT INTO admin_passkeys_multi_user_migration (
	id, user_id, name, credential_id, credential_json, rp_id, origin, created_at, last_used_at
)
SELECT p.id, p.user_id, p.name, p.credential_id, p.credential_json, p.rp_id, p.origin, p.created_at, p.last_used_at
FROM admin_passkeys p
JOIN users u ON u.id = p.user_id;
INSERT INTO multi_user_migration_validation (name, value)
SELECT 'admin_passkeys_target', COUNT(*) FROM admin_passkeys_multi_user_migration;

CREATE TABLE admin_auth_challenges_multi_user_migration (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	kind TEXT NOT NULL,
	session_json TEXT NOT NULL,
	metadata_json TEXT NOT NULL DEFAULT '{}',
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL
);
INSERT INTO admin_auth_challenges_multi_user_migration (
	id, user_id, kind, session_json, metadata_json, created_at, expires_at
)
SELECT c.id, c.user_id, c.kind, c.session_json, c.metadata_json, c.created_at, c.expires_at
FROM admin_auth_challenges c
JOIN users u ON u.id = c.user_id;
INSERT INTO multi_user_migration_validation (name, value)
SELECT 'admin_auth_challenges_target', COUNT(*) FROM admin_auth_challenges_multi_user_migration;

DROP TABLE admin_totp_recovery_codes;
ALTER TABLE admin_totp_recovery_codes_multi_user_migration RENAME TO admin_totp_recovery_codes;
CREATE INDEX idx_admin_totp_recovery_codes_user_unused ON admin_totp_recovery_codes(user_id, used_at);

DROP TABLE admin_passkeys;
ALTER TABLE admin_passkeys_multi_user_migration RENAME TO admin_passkeys;
CREATE INDEX idx_admin_passkeys_user ON admin_passkeys(user_id);
CREATE INDEX idx_admin_passkeys_rp ON admin_passkeys(rp_id, origin);

DROP TABLE admin_auth_challenges;
ALTER TABLE admin_auth_challenges_multi_user_migration RENAME TO admin_auth_challenges;
CREATE INDEX idx_admin_auth_challenges_user_kind ON admin_auth_challenges(user_id, kind);
CREATE INDEX idx_admin_auth_challenges_expires ON admin_auth_challenges(expires_at);

ALTER TABLE api_keys ADD COLUMN owner_user_id TEXT REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE registered_clients ADD COLUMN owner_user_id TEXT REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE traffic_buckets ADD COLUMN owner_user_id TEXT REFERENCES users(id) ON DELETE CASCADE;

UPDATE api_keys
SET owner_user_id = (
	SELECT id FROM users ORDER BY created_at ASC, id ASC LIMIT 1
)
WHERE owner_user_id IS NULL;
UPDATE registered_clients
SET owner_user_id = (
	SELECT id FROM users ORDER BY created_at ASC, id ASC LIMIT 1
)
WHERE owner_user_id IS NULL;
UPDATE traffic_buckets
SET owner_user_id = (
	SELECT id FROM users ORDER BY created_at ASC, id ASC LIMIT 1
)
WHERE owner_user_id IS NULL;

CREATE INDEX idx_api_keys_owner ON api_keys(owner_user_id, created_at);
CREATE INDEX idx_registered_clients_owner ON registered_clients(owner_user_id, created_at);
CREATE INDEX idx_traffic_user_query ON traffic_buckets(owner_user_id, resolution, bucket_start);

ALTER TABLE tunnels RENAME TO tunnels_multi_user_ownership_legacy;
CREATE TABLE tunnels (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	client_id TEXT NOT NULL,
	type TEXT NOT NULL DEFAULT '',
	local_ip TEXT NOT NULL DEFAULT '',
	local_port INTEGER NOT NULL DEFAULT 0,
	remote_port INTEGER NOT NULL DEFAULT 0,
	domain TEXT NOT NULL DEFAULT '',
	hostname TEXT NOT NULL DEFAULT '',
	binding TEXT NOT NULL DEFAULT 'client_id',
	revision INTEGER NOT NULL DEFAULT 1,
	topology TEXT NOT NULL,
	owner_client_id TEXT NOT NULL,
	ingress_location TEXT NOT NULL,
	ingress_client_id TEXT NOT NULL DEFAULT '',
	ingress_type TEXT NOT NULL,
	ingress_config TEXT NOT NULL DEFAULT '{}',
	ingress_bind_ip TEXT NOT NULL DEFAULT '',
	ingress_port INTEGER NOT NULL DEFAULT 0,
	ingress_domain TEXT NOT NULL DEFAULT '',
	ingress_path TEXT NOT NULL DEFAULT '',
	target_location TEXT NOT NULL,
	target_client_id TEXT NOT NULL DEFAULT '',
	target_type TEXT NOT NULL,
	target_config TEXT NOT NULL DEFAULT '{}',
	target_host TEXT NOT NULL DEFAULT '',
	target_port INTEGER NOT NULL DEFAULT 0,
	target_path TEXT NOT NULL DEFAULT '',
	target_resource_key TEXT NOT NULL DEFAULT '',
	transport_policy TEXT NOT NULL,
	actual_transport TEXT NOT NULL DEFAULT 'unknown',
	p2p_state TEXT NOT NULL DEFAULT 'idle',
	p2p_error TEXT NOT NULL DEFAULT '',
	p2p_session_id TEXT NOT NULL DEFAULT '',
	ingress_bps INTEGER NOT NULL DEFAULT 0,
	egress_bps INTEGER NOT NULL DEFAULT 0,
	desired_state TEXT NOT NULL,
	runtime_state TEXT NOT NULL,
	error TEXT NOT NULL DEFAULT '',
	created_by_user_id TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	total_bps INTEGER NOT NULL DEFAULT 0 CHECK (total_bps >= 0),
	owner_user_id TEXT REFERENCES users(id) ON DELETE CASCADE,
	CHECK (topology IN ('server_expose', 'client_to_client')),
	CHECK (ingress_location IN ('server', 'client')),
	CHECK (target_location IN ('client')),
	CHECK (
		(topology = 'server_expose' AND ingress_location = 'server' AND ingress_client_id = '' AND target_location = 'client' AND target_client_id <> '')
		OR
		(topology = 'client_to_client' AND ingress_location = 'client' AND ingress_client_id <> '' AND target_location = 'client' AND target_client_id <> '')
	),
	CHECK (ingress_type IN ('tcp_listen', 'udp_listen', 'http_host', 'socks5_listen')),
	CHECK (target_type IN ('tcp_service', 'udp_service', 'socks5_connect_handler')),
	CHECK (transport_policy IN ('server_relay_only', 'direct_preferred', 'direct_only')),
	CHECK (actual_transport IN ('unknown', 'server_relay', 'peer_direct', 'turn_relay')),
	CHECK (p2p_state IN ('idle', 'gathering', 'checking', 'connected', 'failed', 'fallback', 'closed')),
	CHECK (desired_state IN ('running', 'stopped')),
	CHECK (runtime_state IN ('pending', 'active', 'offline', 'idle', 'error')),
	UNIQUE(client_id, name),
	UNIQUE(owner_client_id, name)
);

INSERT INTO tunnels (
	id, name, client_id, type, local_ip, local_port, remote_port, domain, hostname, binding,
	revision, topology, owner_client_id,
	ingress_location, ingress_client_id, ingress_type, ingress_config, ingress_bind_ip, ingress_port, ingress_domain, ingress_path,
	target_location, target_client_id, target_type, target_config, target_host, target_port, target_path, target_resource_key,
	transport_policy, actual_transport, p2p_state, p2p_error, p2p_session_id,
	ingress_bps, egress_bps, desired_state, runtime_state, error, created_by_user_id, created_at, updated_at, total_bps, owner_user_id
)
SELECT
	id, name, client_id, type, local_ip, local_port, remote_port, domain, hostname, binding,
	revision, topology, owner_client_id,
	ingress_location, ingress_client_id, ingress_type, ingress_config, ingress_bind_ip, ingress_port, ingress_domain, ingress_path,
	target_location, target_client_id, target_type, target_config, target_host, target_port, target_path, target_resource_key,
	transport_policy, actual_transport, p2p_state, p2p_error, p2p_session_id,
	ingress_bps, egress_bps, desired_state, runtime_state, error, NULLIF(created_by_user_id, ''), created_at, updated_at, total_bps,
	(SELECT id FROM users ORDER BY created_at ASC, id ASC LIMIT 1)
FROM tunnels_multi_user_ownership_legacy;

DROP TABLE tunnels_multi_user_ownership_legacy;
CREATE INDEX idx_tunnels_hostname ON tunnels(hostname);
CREATE INDEX idx_tunnels_owner ON tunnels(owner_client_id, created_at);
CREATE INDEX idx_tunnels_ingress_client ON tunnels(ingress_client_id);
CREATE INDEX idx_tunnels_target_client ON tunnels(target_client_id);
CREATE INDEX idx_tunnels_topology ON tunnels(topology);
CREATE INDEX idx_tunnels_runtime_state ON tunnels(runtime_state);
CREATE INDEX idx_tunnels_ingress_port ON tunnels(ingress_location, ingress_client_id, ingress_type, ingress_bind_ip, ingress_port);
CREATE INDEX idx_tunnels_ingress_domain ON tunnels(ingress_domain);
CREATE INDEX idx_tunnels_target_resource ON tunnels(target_location, target_client_id, target_type, target_resource_key);
CREATE INDEX idx_tunnels_user_topology ON tunnels(owner_user_id, topology, created_at);

INSERT INTO multi_user_migration_validation (name, value)
SELECT 'api_keys_target', COUNT(*) FROM api_keys;
INSERT INTO multi_user_migration_validation (name, value)
SELECT 'registered_clients_target', COUNT(*) FROM registered_clients;
INSERT INTO multi_user_migration_validation (name, value)
SELECT 'tunnels_target', COUNT(*) FROM tunnels;
INSERT INTO multi_user_migration_validation (name, value)
SELECT 'traffic_buckets_target', COUNT(*) FROM traffic_buckets;

ALTER TABLE activity_events ADD COLUMN scope_user_id TEXT REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE activity_events ADD COLUMN subject_user_id TEXT REFERENCES users(id) ON DELETE CASCADE;

UPDATE activity_events
SET scope_user_id = (
	SELECT t.owner_user_id
	FROM activity_event_tunnels et
	JOIN tunnels t ON t.id = et.tunnel_id
	WHERE et.event_id = activity_events.id
		AND t.owner_user_id IS NOT NULL
	ORDER BY CASE et.relation WHEN 'subject' THEN 0 WHEN 'related' THEN 1 ELSE 2 END, et.tunnel_id
	LIMIT 1
)
WHERE EXISTS (
	SELECT 1
	FROM activity_event_tunnels et
	JOIN tunnels t ON t.id = et.tunnel_id
	WHERE et.event_id = activity_events.id
		AND t.owner_user_id IS NOT NULL
);

UPDATE activity_events
SET scope_user_id = (
	SELECT c.owner_user_id
	FROM activity_event_clients ec
	JOIN registered_clients c ON c.id = ec.client_id
	WHERE ec.event_id = activity_events.id
		AND c.owner_user_id IS NOT NULL
	ORDER BY CASE ec.relation WHEN 'owner' THEN 0 WHEN 'subject' THEN 1 WHEN 'related' THEN 2 ELSE 3 END, ec.client_id
	LIMIT 1
)
WHERE scope_user_id IS NULL
	AND EXISTS (
		SELECT 1
		FROM activity_event_clients ec
		JOIN registered_clients c ON c.id = ec.client_id
		WHERE ec.event_id = activity_events.id
			AND c.owner_user_id IS NOT NULL
	);

UPDATE activity_events
SET subject_user_id = (
	SELECT u.id FROM users u WHERE u.id = activity_events.actor_id
)
WHERE actor_id <> ''
	AND actor_type IN ('admin', 'user')
	AND EXISTS (SELECT 1 FROM users u WHERE u.id = activity_events.actor_id);

UPDATE activity_events
SET subject_user_id = (
	SELECT u.id
	FROM users u
	WHERE u.id = substr(
		activity_events.dedupe_key,
		length('security:session_environment_mismatch:environment_mismatch:') + 1,
		instr(substr(activity_events.dedupe_key, length('security:session_environment_mismatch:environment_mismatch:') + 1), ':') - 1
	)
)
WHERE action = 'session_environment_mismatch'
	AND dedupe_key IS NOT NULL
	AND substr(dedupe_key, 1, length('security:session_environment_mismatch:environment_mismatch:')) = 'security:session_environment_mismatch:environment_mismatch:'
	AND instr(substr(dedupe_key, length('security:session_environment_mismatch:environment_mismatch:') + 1), ':') > 1;

DELETE FROM activity_events
WHERE action = 'session_environment_mismatch'
	AND (
		dedupe_key IS NULL
		OR substr(dedupe_key, 1, length('security:session_environment_mismatch:environment_mismatch:')) <> 'security:session_environment_mismatch:environment_mismatch:'
		OR instr(substr(dedupe_key, length('security:session_environment_mismatch:environment_mismatch:') + 1), ':') <= 1
		OR subject_user_id IS NULL
	);

CREATE INDEX idx_activity_events_user ON activity_events(scope_user_id, occurred_at_ns DESC, id DESC);
CREATE INDEX idx_activity_events_subject_user ON activity_events(subject_user_id, occurred_at_ns DESC, id DESC);

INSERT INTO multi_user_migration_validation (name, value)
SELECT 'activity_events_target', COUNT(*) FROM activity_events;
INSERT INTO multi_user_migration_validation (name, value)
SELECT 'session_environment_mismatch_removed', value
FROM multi_user_migration_validation
WHERE name = 'session_environment_mismatch_source';
UPDATE multi_user_migration_validation
SET value = (
	SELECT value FROM multi_user_migration_validation WHERE name = 'session_environment_mismatch_source'
) - (
	SELECT COUNT(*) FROM activity_events WHERE action = 'session_environment_mismatch'
)
WHERE name = 'session_environment_mismatch_removed';

DROP TABLE admin_sessions;
DROP TABLE admin_users;

-- Down:
