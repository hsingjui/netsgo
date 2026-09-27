#!/usr/bin/env bash
# test-realenv-upgrade.sh — cross-version upgrade tests on real Linux distros.
#
# Runs the production database upgrade path inside Ubuntu / Debian containers
# using real released binaries (git tags) and the current branch binary:
#
#   A. normal-upgrade   v0.1.14 (latest stable, 010 applied)  -> current
#                       verifies full data integrity: admins, TOTP/passkeys,
#                       sessions, API keys, clients, tunnels, traffic buckets,
#                       ownership backfill, ledgers, login, API key auth, web UI.
#   B. mid-state        v0.1.15-beta.1 (011 applied)          -> current
#                       verifies activity_events backfill (scope/subject user)
#                       and session_environment_mismatch cleanup.
#   C. failure-inject   orphaned passkey/TOTP/challenge rows  -> current
#                       verifies migration 012 rejects, rolls back atomically,
#                       leaves the database untouched, and the old binary can
#                       restart (safe rollback path).
#   D. post-upgrade     current migrated DB                   -> v0.1.14
#                       verifies the old binary fails closed (strict ledger
#                       preflight) without modifying the database file.
#   E. fresh-install    current binary on an empty database
#                       verifies full schema creation and first-time init.
#
# Every scenario runs on both Ubuntu 24.04 and Debian 12 unless DISTROS is set.
#
# Usage:
#   bash test/e2e/scripts/test-realenv-upgrade.sh
#   DISTROS=ubuntu bash test/e2e/scripts/test-realenv-upgrade.sh
#   DISTROS=debian UPGRADE_TAGS="v0.1.14" bash test/e2e/scripts/test-realenv-upgrade.sh
#
# Required: docker, go, bun (for building released-tag frontends), sqlite3 on host
# is NOT required (installed inside containers).
set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
ARTIFACTS_DIR="${REPO_ROOT}/test/e2e/.artifacts"
DISTROS="${DISTROS:-ubuntu debian}"
UPGRADE_TAGS="${UPGRADE_TAGS:-v0.1.14 v0.1.15-beta.1}"
# comma-separated scenario names to run (default: all). e.g. RUN_SCENARIOS=A,F
RUN_SCENARIOS="${RUN_SCENARIOS:-}"

run_scenario() {
	# run_scenario <name> <function> <container> [args...]
	local name="$1" fn="$2" c="$3"
	if [ -n "${RUN_SCENARIOS}" ]; then
		case ",${RUN_SCENARIOS}," in
			*,"${name}",*) ;;
			*) return 0 ;;
		esac
	fi
	if "${fn}" "${c}" "${@:4}"; then PASS=$((PASS+1)); else FAIL=$((FAIL+1)); fi
	server_stop "${c}" || true
	docker exec "${c}" bash -c "rm -rf '${DATA_DIR}' && mkdir -p '${DATA_DIR}'"
}
SERVER_PORT="${SERVER_PORT:-19527}"
DATA_DIR="/var/lib/netsgo-upgrade"
ADMIN_USER="admin"
ADMIN_PASS="${NETSGO_REALENV_ADMIN_PASS:-NetsGo1-RealEnvUpgrade!2026}"
SERVER_ADDR="http://127.0.0.1:${SERVER_PORT}"
HOST_ARCH="$(docker info --format '{{.Architecture}}' 2>/dev/null || uname -m)"
case "${HOST_ARCH}" in
	x86_64|amd64) GOARCH=amd64 ;;
	aarch64|arm64) GOARCH=arm64 ;;
	*) echo "unsupported docker architecture: ${HOST_ARCH}" >&2; exit 1 ;;
esac

PASS=0
FAIL=0

log()  { echo "[realenv] $*"; }
ok()   { echo "  ✔ $*"; }
fail() { echo "  ✘ $*"; }

# ---------- binary builds ----------

build_binary() {
	# build_binary <tag|current> <out-name> [dev]
	local ref="$1" out="$2" mode="${3:-}"
	local src tmp go_tags=""
	if [ "${ref}" = "current" ]; then
		src="${REPO_ROOT}"
		if [ -z "${mode}" ] && [ ! -d "${REPO_ROOT}/web/dist" ]; then
			log "building frontend for current..."
			(cd "${REPO_ROOT}/web" && bun install --frozen-lockfile && bun run build) >/dev/null
		fi
		if [ "${mode}" = "dev" ]; then go_tags="-tags dev"; fi
		CGO_ENABLED=0 GOOS=linux GOARCH="${GOARCH}" go build ${go_tags} -trimpath -o "${ARTIFACTS_DIR}/${out}" ./cmd/netsgo
	else
		tmp="$(mktemp -d)"
		trap 'rm -rf "${tmp}"' RETURN
		log "extracting source tree at ${ref}..."
		git archive --format=tar "${ref}" | tar -x -C "${tmp}"
		if [ "${mode}" != "dev" ]; then
			log "building frontend at ${ref}..."
			(cd "${tmp}/web" && bun install --frozen-lockfile && bun run build) >/dev/null
		else
			go_tags="-tags dev"
		fi
		(cd "${tmp}" && CGO_ENABLED=0 GOOS=linux GOARCH="${GOARCH}" go build ${go_tags} -trimpath -o "${ARTIFACTS_DIR}/${out}" ./cmd/netsgo)
	fi
	log "binary ready: ${ARTIFACTS_DIR}/${out} (${ref}${mode:+ dev})"
}

# ---------- container helpers ----------

container_start() {
	# container_start <distro> <name> -> echoes container id
	local distro="$1" name="$2" image
	case "${distro}" in
		ubuntu) image="ubuntu:24.04" ;;
		debian) image="debian:12" ;;
		*) echo "unknown distro ${distro}" >&2; exit 1 ;;
	esac
	docker rm -f "${name}" >/dev/null 2>&1 || true
	docker run -d --name "${name}" "${image}" sleep infinity >/dev/null
	docker exec "${name}" sh -c 'command -v apt-get >/dev/null' || { echo "container ${name} has no apt-get" >&2; exit 1; }
	docker exec "${name}" bash -c 'export DEBIAN_FRONTEND=noninteractive; apt-get update -qq && apt-get install -y -qq curl jq sqlite3 ca-certificates procps >/dev/null 2>&1'
	docker exec "${name}" mkdir -p "${DATA_DIR}"
}

container_rm() { docker rm -f "$1" >/dev/null 2>&1 || true; }

bin_push() {
	# bin_push <container> <host-binary-path>
	docker cp "$2" "$1:/usr/local/bin/netsgo"
	docker exec "$1" chmod +x /usr/local/bin/netsgo
}

server_start() {
	# server_start <container> <log-file>
	docker exec -d "$1" bash -c "cd / && nohup /usr/local/bin/netsgo server --data-dir '${DATA_DIR}' --port ${SERVER_PORT} --init-admin-username '${ADMIN_USER}' --init-admin-password '${ADMIN_PASS}' --init-server-addr '${SERVER_ADDR}' > '${DATA_DIR}/$2' 2>&1 &"
	server_wait_ready "$1" "$2"
}

server_start_noinit() {
	# server_start_noinit <container> <log-file>  (database already initialized)
	docker exec -d "$1" bash -c "cd / && nohup /usr/local/bin/netsgo server --data-dir '${DATA_DIR}' --port ${SERVER_PORT} > '${DATA_DIR}/$2' 2>&1 &"
	server_wait_ready "$1" "$2"
}

server_wait_ready() {
	local c="$1" logf="$2" deadline=$(( $(date +%s) + 60 ))
	while [ "$(date +%s)" -lt "${deadline}" ]; do
		if docker exec "$c" bash -c "curl -s --max-time 2 -o /dev/null -w '%{http_code}' http://127.0.0.1:${SERVER_PORT}/api/status 2>/dev/null" | grep -qE '^(200|401|403)$'; then
			return 0
		fi
		if ! docker exec "$c" bash -c "pgrep -f '/usr/local/bin/netsgo server' >/dev/null 2>&1"; then
			echo "server process exited; log tail:" >&2
			docker exec "$c" tail -30 "${DATA_DIR}/${logf}" >&2 || true
			return 1
		fi
		sleep 1
	done
	echo "server did not become ready in 60s; log tail:" >&2
	docker exec "$c" tail -50 "${DATA_DIR}/${logf}" >&2 || true
	return 1
}

server_stop() {
	local c="$1"
	docker exec "$c" bash -c 'pkill -f "/usr/local/bin/netsgo server" 2>/dev/null || true; for i in $(seq 1 20); do pgrep -f "/usr/local/bin/netsgo server" >/dev/null || break; sleep 0.5; done'
}

db() {
	# db <container> <sql> — run sqlite3 query, echo rows
	docker exec "$c" sqlite3 "${DATA_DIR}/server/netsgo.db" "$2"
}

db_check() {
	# db_check <container> <sql> <expected-exact-output>
	local got
	got="$(db "$1" "$2")"
	if [ "${got}" != "$3" ]; then
		fail "sql mismatch: $2"
		echo "    want: $3"
		echo "    got:  ${got}"
		SCENARIO_FAIL=1
		return 1
	fi
	ok "$2 -> $3"
}

db_check_contains() {
	# db_check_contains <container> <sql> <substring>
	local got
	got="$(db "$1" "$2")"
	if ! echo "${got}" | grep -q "$3"; then
		fail "sql missing expected value: $2"
		echo "    want substring: $3"
		echo "    got: ${got}"
		SCENARIO_FAIL=1
		return 1
	fi
	ok "$2 -> contains $3"
}

login_token() {
	# login_token <container> -> token
	docker exec "$c" bash -c "curl -fsS -X POST http://127.0.0.1:${SERVER_PORT}/api/auth/login -H 'Content-Type: application/json' -d '{\"username\":\"${ADMIN_USER}\",\"password\":\"${ADMIN_PASS}\"}' | jq -r '.token'"
}

api_status_code() {
	# api_status_code <container> <auth-header> <path>
	docker exec "$c" bash -c "curl -s -o /dev/null -w '%{http_code}' -H '$2' http://127.0.0.1:${SERVER_PORT}$3"
}

# ---------- seed SQL (legacy-format production data) ----------

seed_legacy_data() {
	# seed_legacy_data <container> — v0.1.14 schema data incl. security rows
	local c="$1"
	docker exec -i "$c" sqlite3 "${DATA_DIR}/server/netsgo.db" <<'SQL'
UPDATE admin_users SET totp_secret = 'totp-secret-legacy-value' WHERE username = 'admin';
INSERT INTO admin_sessions (id, user_id, username, role, created_at, expires_at, ip, user_agent)
VALUES ('legacy-session-1', (SELECT id FROM admin_users ORDER BY created_at LIMIT 1), 'admin', 'admin',
        '2026-07-01T00:00:00Z', '2030-01-01T00:00:00Z', '127.0.0.1', 'realenv-test');
INSERT INTO admin_totp_recovery_codes (id, user_id, code_hash, created_at, used_at) VALUES
('rc-1', (SELECT id FROM admin_users ORDER BY created_at LIMIT 1), 'rc-hash-1', '2026-07-01T00:00:00Z', NULL),
('rc-2', (SELECT id FROM admin_users ORDER BY created_at LIMIT 1), 'rc-hash-2', '2026-07-02T00:00:00Z', '2026-07-03T00:00:00Z');
INSERT INTO admin_passkeys (id, user_id, name, credential_id, credential_json, rp_id, origin, created_at, last_used_at)
VALUES ('pk-1', (SELECT id FROM admin_users ORDER BY created_at LIMIT 1), 'yubikey',
        'cred-abc-123', '{"id":"cred-abc-123"}', 'panel.example.com', 'https://panel.example.com',
        '2026-07-01T00:00:00Z', '2026-07-10T00:00:00Z');
INSERT INTO admin_auth_challenges (id, user_id, kind, session_json, metadata_json, created_at, expires_at)
VALUES ('ch-1', (SELECT id FROM admin_users ORDER BY created_at LIMIT 1), 'mfa',
        '{"pending":true}', '{}', '2026-07-01T00:00:00Z', '2026-07-02T00:00:00Z');
INSERT INTO registered_clients (id, install_id, display_name, hostname, os, arch, ip, version, created_at, last_seen)
VALUES ('client-target', 'install-target', 'target host', 'target-host', 'linux', 'arm64', '10.0.0.2', 'v0.1.14',
        '2026-06-01T00:00:00Z', '2026-07-20T00:00:00Z'),
       ('client-ingress', 'install-ingress', 'ingress host', 'ingress-host', 'linux', 'amd64', '10.0.0.3', 'v0.1.14',
        '2026-06-02T00:00:00Z', '2026-07-20T00:00:00Z');
INSERT INTO client_stats (client_id, cpu_usage, mem_usage, uptime, updated_at)
VALUES ('client-target', 12.5, 33.3, 86400, '2026-07-20T00:00:00Z'),
       ('client-ingress', 3.2, 41.0, 3600, '2026-07-20T00:00:00Z');
INSERT INTO tunnels (
	id, name, client_id, type, local_ip, local_port, remote_port, domain, hostname, binding,
	revision, topology, owner_client_id, ingress_location, ingress_client_id, ingress_type, ingress_config,
	ingress_bind_ip, ingress_port, ingress_domain, ingress_path, target_location, target_client_id,
	target_type, target_config, target_host, target_port, target_path, target_resource_key,
	transport_policy, actual_transport, p2p_state, p2p_error, p2p_session_id,
	ingress_bps, egress_bps, desired_state, runtime_state, error, created_by_user_id, created_at, updated_at, total_bps
) VALUES (
	'tunnel-http-1', 'web', 'client-target', 'http', '', 8080, 0, 'app.example.com', 'app.example.com', 'client_id',
	1, 'server_expose', 'client-target', 'server', '', 'http_host', '{"domain":"app.example.com","allowed_source_cidrs":["0.0.0.0/0","::/0"],"auth":{"type":"none"}}',
	'', 0, 'app.example.com', '', 'client', 'client-target',
	'tcp_service', '{"host":"127.0.0.1","port":8080}', '127.0.0.1', 8080, '', '',
	'server_relay_only', 'server_relay', 'idle', '', '',
	1024, 2048, 'running', 'active', '', '', '2026-06-01T00:00:00Z', '2026-07-20T00:00:00Z', 0
), (
	'tunnel-tcp-1', 'c2c-db', 'client-ingress', 'tcp', '', 5432, 0, '', '', 'client_id',
	1, 'client_to_client', 'client-target', 'client', 'client-ingress', 'tcp_listen', '{"bind_ip":"0.0.0.0","port":15432,"allowed_source_cidrs":["0.0.0.0/0","::/0"]}',
	'0.0.0.0', 15432, '', '', 'client', 'client-target',
	'tcp_service', '{"host":"127.0.0.1","port":5432}', '127.0.0.1', 5432, '', '',
	'direct_preferred', 'peer_direct', 'connected', '', 'p2p-sess-1',
	512, 512, 'running', 'active', '', '', '2026-06-05T00:00:00Z', '2026-07-20T00:00:00Z', 0
);
INSERT INTO traffic_buckets (tunnel_id, owner_client_id, ingress_client_id, target_client_id, topology, transport, client_id, tunnel_name, tunnel_type, resolution, bucket_start, ingress_bytes, egress_bytes) VALUES
('tunnel-http-1', 'client-target', '', 'client-target', 'server_expose', 'server_relay', 'client-target', 'web', 'http', '1h', 1789000000, 1000, 2000),
('tunnel-http-1', 'client-target', '', 'client-target', 'server_expose', 'server_relay', 'client-target', 'web', 'http', '1h', 1789003600, 1500, 2500),
('tunnel-tcp-1', 'client-ingress', 'client-ingress', 'client-target', 'client_to_client', 'peer_direct', 'client-ingress', 'c2c-db', 'tcp', '1h', 1789000000, 500, 500);
INSERT INTO client_tokens (id, token_hash, install_id, key_id, client_id, created_at, last_active_at, last_ip, is_revoked) VALUES
('ct-1', 'token-hash-1', 'install-target', 'key-1', 'client-target', '2026-06-01T00:00:00Z', '2026-07-20T00:00:00Z', '10.0.0.2', 0),
('ct-2', 'token-hash-2', 'install-ingress', 'key-1', 'client-ingress', '2026-06-02T00:00:00Z', '2026-07-20T00:00:00Z', '10.0.0.3', 0);
INSERT INTO allowed_ports (start_port, end_port) VALUES (10000, 20000);
SQL
}

seed_activity_data() {
	# seed_activity_data <container> — 011 activity events incl. environment mismatches
	local c="$1"
	docker exec -i "$c" sqlite3 "${DATA_DIR}/server/netsgo.db" <<'SQL'
INSERT INTO activity_events (occurred_at_ns, recorded_at_ns, severity, category, action, source, actor_type, actor_id, actor_name, dedupe_key, payload_json) VALUES
(1789000000000000000, 1789000001000000000, 'info', 'tunnel', 'tunnel_created', 'server', 'admin', (SELECT id FROM admin_users ORDER BY created_at LIMIT 1), 'admin', 'tunnel:create:tunnel-http-1', '{}'),
(1789000002000000000, 1789000003000000000, 'error', 'client', 'client_offline', 'server', 'system', '', '', 'client:offline:client-target:1789000002', '{}'),
(1789000004000000000, 1789000005000000000, 'warning', 'security', 'session_environment_mismatch', 'server', 'admin', (SELECT id FROM admin_users ORDER BY created_at LIMIT 1), 'admin',
 'security:session_environment_mismatch:environment_mismatch:' || (SELECT id FROM admin_users ORDER BY created_at LIMIT 1) || ':192.168.1.10', '{}'),
(1789000006000000000, 1789000007000000000, 'warning', 'security', 'session_environment_mismatch', 'server', 'admin', '', 'ghost-user',
 'security:session_environment_mismatch:environment_mismatch:ghost-user:10.0.0.99', '{}'),
(1789000008000000000, 1789000009000000000, 'warning', 'security', 'session_environment_mismatch', 'server', 'admin', '', 'ghost-user', NULL, '{}');
INSERT INTO activity_event_tunnels (event_id, tunnel_id, relation, name, tunnel_type, topology) VALUES
((SELECT id FROM activity_events WHERE dedupe_key = 'tunnel:create:tunnel-http-1'), 'tunnel-http-1', 'subject', 'web', 'http', 'server_expose');
INSERT INTO activity_event_clients (event_id, client_id, relation, display_name, hostname) VALUES
((SELECT id FROM activity_events WHERE dedupe_key = 'client:offline:client-target:1789000002'), 'client-target', 'subject', 'target host', 'target-host');
SQL
}

# ---------- scenario A: v0.1.14 -> current normal upgrade ----------

scenario_a() {
	local c="$1" tag="$2" bin="${tag//./_}"
	SCENARIO_FAIL=0
	log "SCENARIO A: normal upgrade ${tag} -> current (${c})"
	bin_push "${c}" "${ARTIFACTS_DIR}/netsgo-${bin}"
	server_start "${c}" "a-old.log"
	ok "old server (${tag}) ready"

	# real API usage on old version
	local token legacy_token
	token="$(login_token "${c}")"
	[ -n "${token}" ] || { fail "A: old login returned no token"; return 1; }
	legacy_token="${token}"
	ok "old login works"
	local key_resp
	key_resp="$(docker exec "$c" bash -c "curl -fsS -X POST http://127.0.0.1:${SERVER_PORT}/api/admin/keys -H 'Authorization: Bearer ${token}' -H 'Content-Type: application/json' -d '{\"name\":\"ci-client\",\"permissions\":[\"connect\"]}'")"
	local raw_key
	raw_key="$(echo "${key_resp}" | jq -r '.raw_key')"
	[ -n "${raw_key}" ] || { fail "A: old API key creation failed: ${key_resp}"; return 1; }
	ok "old API key created (connect permission)"

	seed_legacy_data "${c}"
	# verify seed actually landed (docker exec stdin pitfalls)
	db_check "${c}" "SELECT count(*) FROM registered_clients" "2"
	db_check "${c}" "SELECT count(*) FROM tunnels" "2"
	db_check "${c}" "SELECT count(*) FROM traffic_buckets" "3"
	db_check "${c}" "SELECT count(*) FROM admin_sessions" "2"  # 1 login session + 1 seeded
	ok "seeded legacy production data"
	legacy_admin_hash="$(docker exec "$c" sqlite3 "${DATA_DIR}/server/netsgo.db" "SELECT password_hash FROM admin_users WHERE username='admin'")"
	[ -n "${legacy_admin_hash}" ] || { fail "A: could not read legacy admin password hash"; return 1; }

	server_stop "${c}"
	bin_push "${c}" "${ARTIFACTS_DIR}/netsgo-current"
	server_start_noinit "${c}" "a-current.log"
	ok "current server started after upgrade"

	# ledger checks
	db_check "${c}" "SELECT count(*) FROM schema_migrations WHERE name='012_multi_user_ownership'" "1"
	db_check "${c}" "SELECT group_concat(name, ',') FROM schema_compatible_migrations ORDER BY name" "010_client_auth_control,011_activity_events"

	# identity migration (totp_enabled stays 0 here so login does not require MFA;
	# the enabled-flag migration itself is verified in scenario B)
	db_check "${c}" "SELECT count(*) FROM users WHERE is_admin=1 AND status='active'" "1"
	db_check "${c}" "SELECT count(*) FROM users WHERE totp_secret='totp-secret-legacy-value'" "1"
	# password hash must be preserved exactly (compare with hash captured before migration)
	local migrated_hash
	migrated_hash="$(docker exec "$c" sqlite3 "${DATA_DIR}/server/netsgo.db" "SELECT password_hash FROM users WHERE username='admin'")"
	if [ "${migrated_hash}" != "${legacy_admin_hash}" ]; then
		fail "A: password hash changed across migration"
		echo "    before: ${legacy_admin_hash}"
		echo "    after:  ${migrated_hash}"
		return 1
	fi
	ok "password hash preserved exactly"

	# sessions / security rows (checked before any new login, which would
	# replace the migrated sessions for the same user)
	db_check "${c}" "SELECT count(*) FROM user_sessions" "2"
	db_check "${c}" "SELECT count(*) FROM admin_totp_recovery_codes" "2"
	db_check "${c}" "SELECT count(*) FROM admin_passkeys" "1"
	db_check "${c}" "SELECT count(*) FROM admin_auth_challenges" "1"
	db_check "${c}" "SELECT count(*) FROM admin_totp_recovery_codes WHERE user_id IN (SELECT id FROM users)" "2"
	db_check "${c}" "SELECT count(*) FROM admin_passkeys WHERE user_id IN (SELECT id FROM users)" "1"
	db_check "${c}" "SELECT count(*) FROM admin_auth_challenges WHERE user_id IN (SELECT id FROM users)" "1"

	# ownership backfill
	db_check "${c}" "SELECT count(*) FROM api_keys WHERE owner_user_id IN (SELECT id FROM users)" "1"
	db_check "${c}" "SELECT count(*) FROM registered_clients WHERE owner_user_id IN (SELECT id FROM users)" "2"
	db_check "${c}" "SELECT count(*) FROM traffic_buckets WHERE owner_user_id IN (SELECT id FROM users)" "3"

	# tables untouched by 012 must keep their data
	db_check "${c}" "SELECT count(*) FROM client_tokens" "2"
	db_check "${c}" "SELECT count(*) FROM allowed_ports" "1"

	# tunnels rebuilt with data preserved
	db_check "${c}" "SELECT count(*) FROM tunnels" "2"
	db_check "${c}" "SELECT count(*) FROM tunnels WHERE id='tunnel-http-1' AND topology='server_expose' AND desired_state='running' AND runtime_state='active' AND ingress_domain='app.example.com' AND owner_user_id IN (SELECT id FROM users)" "1"
	db_check "${c}" "SELECT count(*) FROM tunnels WHERE id='tunnel-tcp-1' AND topology='client_to_client' AND ingress_client_id='client-ingress' AND target_client_id='client-target' AND transport_policy='direct_preferred' AND owner_user_id IN (SELECT id FROM users)" "1"

	# old tables removed, FK integrity
	db_check "${c}" "SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('admin_users','admin_sessions','tunnels_multi_user_ownership_legacy')" "0"
	db_check "${c}" "SELECT count(*) FROM pragma_foreign_key_check" "0"

	# functional checks on current (login replaces the user's migrated sessions)
	# a token issued BEFORE the upgrade must still authenticate afterwards
	# (jwt_secret is preserved and the session row was migrated to user_sessions)
	local old_token
	old_token="${legacy_token}"
	st="$(api_status_code "${c}" "Authorization: Bearer ${old_token}" /api/status)"
	[ "${st}" = "200" ] || { fail "A: pre-upgrade session token invalid after upgrade (got ${st})"; return 1; }
	ok "pre-upgrade session token still valid after upgrade"
	local token2
	token2="$(login_token "${c}")"
	[ -n "${token2}" ] || { fail "A: login with original password failed after upgrade"; return 1; }
	ok "login with original password works after upgrade"
	# API key row survived migration with its lookup digest and owner backfill
	db_check "${c}" "SELECT count(*) FROM api_keys WHERE name='ci-client' AND lookup_digest <> '' AND owner_user_id IN (SELECT id FROM users)" "1"
	st="$(api_status_code "${c}" "Authorization: Bearer ${token2}" /api/activity)"
	[ "${st}" = "200" ] || { fail "A: /api/activity after upgrade returned ${st}"; return 1; }
	ok "activity API works after upgrade"
	st="$(api_status_code "${c}" "Authorization: Bearer ${token2}" /api/clients)"
	[ "${st}" = "200" ] || { fail "A: /api/clients after upgrade returned ${st}"; return 1; }
	ok "clients API works after upgrade"
	local html
	html="$(docker exec "$c" bash -c "curl -s --max-time 5 http://127.0.0.1:${SERVER_PORT}/ | head -c 200")"
	echo "${html}" | grep -q '<!doctype html>' || { fail "A: web panel did not return HTML"; return 1; }
	ok "web panel serves HTML"

	[ "${SCENARIO_FAIL}" = "0" ] || { fail "A: scenario checks failed"; return 1; }
	log "PASS: scenario A (${tag} -> current) on ${c}"
	return 0
}

# ---------- scenario B: v0.1.15-beta.1 -> current (011 applied) ----------

scenario_b() {
	local c="$1" tag="v0.1.15-beta.1" bin="${tag//./_}"
	SCENARIO_FAIL=0
	log "SCENARIO B: mid-state upgrade ${tag} -> current (${c})"
	bin_push "${c}" "${ARTIFACTS_DIR}/netsgo-${bin}"
	server_start "${c}" "b-old.log"
	ok "old server (${tag}) ready"

	seed_legacy_data "${c}"
	# enable TOTP for the admin to verify the flag migrates (no login happens here)
	docker exec "$c" sqlite3 "${DATA_DIR}/server/netsgo.db" "UPDATE admin_users SET totp_enabled=1 WHERE username='admin'"
	seed_activity_data "${c}"
	# verify seed actually landed
	db_check "${c}" "SELECT count(*) FROM activity_events" "5"
	db_check "${c}" "SELECT count(*) FROM activity_event_tunnels" "1"
	db_check "${c}" "SELECT count(*) FROM activity_event_clients" "1"
	ok "seeded activity events incl. environment mismatches"

	server_stop "${c}"
	bin_push "${c}" "${ARTIFACTS_DIR}/netsgo-current"
	server_start_noinit "${c}" "b-current.log"
	ok "current server started after upgrade"

	db_check "${c}" "SELECT count(*) FROM schema_migrations WHERE name='012_multi_user_ownership'" "1"
	# totp_enabled flag migrated into the users table
	db_check "${c}" "SELECT count(*) FROM users WHERE username='admin' AND totp_enabled=1 AND totp_secret='totp-secret-legacy-value'" "1"
	# 5 events seeded: 2 kept normal + 1 valid mismatch kept + 2 invalid mismatches removed
	db_check "${c}" "SELECT count(*) FROM activity_events" "3"
	# valid mismatch kept and backfilled with subject_user_id
	db_check "${c}" "SELECT count(*) FROM activity_events WHERE action='session_environment_mismatch' AND subject_user_id IN (SELECT id FROM users)" "1"
	# normal events scoped to owner
	db_check "${c}" "SELECT count(*) FROM activity_events WHERE action='tunnel_created' AND scope_user_id IN (SELECT id FROM users)" "1"
	db_check "${c}" "SELECT count(*) FROM activity_events WHERE action='client_offline' AND scope_user_id IN (SELECT id FROM users)" "1"
	# joined tables intact
	db_check "${c}" "SELECT count(*) FROM activity_event_tunnels" "1"
	db_check "${c}" "SELECT count(*) FROM activity_event_clients" "1"

	# ownership backfill still correct
	db_check "${c}" "SELECT count(*) FROM tunnels WHERE owner_user_id IN (SELECT id FROM users)" "2"
	db_check "${c}" "SELECT count(*) FROM registered_clients WHERE owner_user_id IN (SELECT id FROM users)" "2"

	[ "${SCENARIO_FAIL}" = "0" ] || { fail "B: scenario checks failed"; return 1; }
	log "PASS: scenario B (${tag} -> current) on ${c}"
	return 0
}

# ---------- scenario C: failure injection (orphaned security rows) ----------

scenario_c() {
	local c="$1" tag="v0.1.14" bin="${tag//./_}"
	SCENARIO_FAIL=0
	log "SCENARIO C: failure injection (orphaned rows) on ${c}"

	local kind orphan_table orphan_id
	for kind in passkey recovery_code auth_challenge; do
		case "${kind}" in
			passkey) orphan_table=admin_passkeys; orphan_id=pk-orphan
				orphan_sql="INSERT INTO admin_passkeys (id, user_id, name, credential_id, credential_json, rp_id, origin, created_at, last_used_at) VALUES ('pk-orphan', 'ghost-user', 'orphan', 'cred-orphan', '{}', 'r', 'o', '2026-07-01T00:00:00Z', NULL)" ;;
			recovery_code) orphan_table=admin_totp_recovery_codes; orphan_id=rc-orphan
				orphan_sql="INSERT INTO admin_totp_recovery_codes (id, user_id, code_hash, created_at, used_at) VALUES ('rc-orphan', 'ghost-user', 'rc-orphan-hash', '2026-07-01T00:00:00Z', NULL)" ;;
			auth_challenge) orphan_table=admin_auth_challenges; orphan_id=ch-orphan
				orphan_sql="INSERT INTO admin_auth_challenges (id, user_id, kind, session_json, metadata_json, created_at, expires_at) VALUES ('ch-orphan', 'ghost-user', 'mfa', '{}', '{}', '2026-07-01T00:00:00Z', '2026-07-02T00:00:00Z')" ;;
		esac

		# fresh environment per kind: init -> seed -> inject orphan -> upgrade attempt
		docker exec "${c}" bash -c "rm -rf '${DATA_DIR}' && mkdir -p '${DATA_DIR}'"
		bin_push "${c}" "${ARTIFACTS_DIR}/netsgo-${bin}"
		server_start "${c}" "c-old.log"
		ok "old server ready (${kind})"

		seed_legacy_data "${c}"
		docker exec "$c" sqlite3 "${DATA_DIR}/server/netsgo.db" "${orphan_sql}"
		server_stop "${c}"
		bin_push "${c}" "${ARTIFACTS_DIR}/netsgo-current"

		# current must fail to start and must say why
		if docker exec "${c}" bash -c "/usr/local/bin/netsgo server --data-dir '${DATA_DIR}' --port ${SERVER_PORT} > '${DATA_DIR}/c-fail-${kind}.log' 2>&1"; then
			fail "C(${kind}): current server unexpectedly started"
			return 1
		fi
		docker exec "${c}" bash -c "grep -q 'was not upgraded' '${DATA_DIR}/c-fail-${kind}.log'" \
			|| { fail "C(${kind}): error message missing 'was not upgraded'"; docker exec "${c}" tail -20 "${DATA_DIR}/c-fail-${kind}.log" >&2; return 1; }
		docker exec "${c}" bash -c "grep -q 'rolled back' '${DATA_DIR}/c-fail-${kind}.log'" \
			|| { fail "C(${kind}): error message missing 'rolled back'"; docker exec "${c}" tail -20 "${DATA_DIR}/c-fail-${kind}.log" >&2; return 1; }
		ok "C(${kind}): migration rejected with rollback guidance"

		# database must be untouched: no users table, admin_users still present
		db_check "${c}" "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='users'" "0"
		db_check "${c}" "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='admin_users'" "1"
		db_check "${c}" "SELECT count(*) FROM schema_migrations WHERE name='012_multi_user_ownership'" "0"
		# the injected orphan row must still be present in its original table
		db_check "${c}" "SELECT count(*) FROM ${orphan_table} WHERE id='${orphan_id}'" "1"
		ok "C(${kind}): database untouched after rejected migration"

		# old binary must be able to restart (safe rollback)
		bin_push "${c}" "${ARTIFACTS_DIR}/netsgo-${bin}"
		server_start_noinit "${c}" "c-rollback-${kind}.log"
		ok "C(${kind}): old binary restarted successfully"
		server_stop "${c}"
	done

	[ "${SCENARIO_FAIL}" = "0" ] || { fail "C: scenario checks failed"; return 1; }
	log "PASS: scenario C (failure injection) on ${c}"
	return 0
}

# ---------- scenario D: post-upgrade rollback must fail closed ----------

scenario_d() {
	local c="$1" tag="v0.1.14" bin="${tag//./_}"
	SCENARIO_FAIL=0
	log "SCENARIO D: post-upgrade rollback fails closed (${c})"
	bin_push "${c}" "${ARTIFACTS_DIR}/netsgo-${bin}"
	server_start "${c}" "d-old.log"
	ok "old server ready"
	server_stop "${c}"

	bin_push "${c}" "${ARTIFACTS_DIR}/netsgo-current"
	server_start_noinit "${c}" "d-current.log"
	ok "current server upgraded database"
	server_stop "${c}"

	local before after
	before="$(docker exec "${c}" sha256sum "${DATA_DIR}/server/netsgo.db" | cut -d' ' -f1)"

	# attempt to roll back: put the old binary back in place
	bin_push "${c}" "${ARTIFACTS_DIR}/netsgo-${bin}"
	if docker exec "${c}" bash -c "/usr/local/bin/netsgo server --data-dir '${DATA_DIR}' --port ${SERVER_PORT} > '${DATA_DIR}/d-rollback.log' 2>&1"; then
		fail "D: old binary unexpectedly started on upgraded database"
		docker exec "${c}" tail -20 "${DATA_DIR}/d-rollback.log" >&2 || true
		return 1
	fi
	docker exec "${c}" bash -c "grep -q 'unknown applied migration' '${DATA_DIR}/d-rollback.log'" \
		|| { fail "D: old binary error missing 'unknown applied migration'"; docker exec "${c}" tail -20 "${DATA_DIR}/d-rollback.log" >&2; return 1; }
	ok "D: old binary rejected upgraded database (fail closed)"

	after="$(docker exec "${c}" sha256sum "${DATA_DIR}/server/netsgo.db" | cut -d' ' -f1)"
	if [ "${before}" != "${after}" ]; then
		fail "D: database file was modified by rejected rollback attempt"
		return 1
	fi
	ok "D: database file unchanged after rejected rollback"

	# current binary must still start (idempotent)
	bin_push "${c}" "${ARTIFACTS_DIR}/netsgo-current"
	server_start_noinit "${c}" "d-reup.log"
	ok "D: current server restarts fine"
	server_stop "${c}"

	[ "${SCENARIO_FAIL}" = "0" ] || { fail "D: scenario checks failed"; return 1; }
	log "PASS: scenario D (post-upgrade rollback fail-closed) on ${c}"
	return 0
}

# ---------- scenario E: fresh install on current ----------

scenario_e() {
	local c="$1"
	SCENARIO_FAIL=0
	log "SCENARIO E: fresh install (current) on ${c}"
	bin_push "${c}" "${ARTIFACTS_DIR}/netsgo-current"
	server_start "${c}" "e-fresh.log"
	ok "current server initialized fresh database"

	db_check "${c}" "SELECT count(*) FROM schema_migrations WHERE name='012_multi_user_ownership'" "1"
	db_check "${c}" "SELECT count(*) FROM users WHERE is_admin=1 AND status='active'" "1"
	db_check "${c}" "SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('admin_users','admin_sessions')" "0"
	local token
	token="$(login_token "${c}")"
	[ -n "${token}" ] || { fail "E: fresh login failed"; return 1; }
	ok "fresh login works"
	local st
	st="$(api_status_code "${c}" "Authorization: Bearer ${token}" /api/status)"
	[ "${st}" = "200" ] || { fail "E: /api/status returned ${st}"; return 1; }
	ok "fresh /api/status works"
	server_stop "${c}"

	[ "${SCENARIO_FAIL}" = "0" ] || { fail "E: scenario checks failed"; return 1; }
	log "PASS: scenario E (fresh install) on ${c}"
	return 0
}

# ---------- scenario F: v0.1.8 (001-008 era) full-path upgrade ----------
#
# Oldest still-released schema generation: no total_bps column, tunnels carry
# '{}' ingress_config (created by migration 005), two admins, client tokens,
# allowed ports, and a large traffic_buckets table to measure migration cost.

seed_legacy_data_v18() {
	# seed_legacy_data_v18 <container> — v0.1.8 schema data (no total_bps)
	local c="$1"
	docker exec -i "$c" sqlite3 "${DATA_DIR}/server/netsgo.db" <<'SQL'
INSERT INTO admin_users (id, username, password_hash, role, created_at, last_login, totp_enabled, totp_secret) VALUES
('admin-earliest', 'boss', 'hash-legacy-boss', 'admin', '2026-01-01T00:00:00Z', NULL, 1, 'totp-boss'),
('admin-second', 'operator', 'hash-legacy-operator', 'admin', '2026-02-01T00:00:00Z', NULL, 0, '');
INSERT INTO admin_sessions (id, user_id, username, role, created_at, expires_at, ip, user_agent)
VALUES ('legacy-session-v18', 'admin-earliest', 'boss', 'admin', '2026-07-01T00:00:00Z', '2030-01-01T00:00:00Z', '127.0.0.1', 'realenv-test');
INSERT INTO admin_totp_recovery_codes (id, user_id, code_hash, created_at, used_at)
VALUES ('rc-v18', 'admin-earliest', 'rc-hash-v18', '2026-07-01T00:00:00Z', NULL);
INSERT INTO admin_passkeys (id, user_id, name, credential_id, credential_json, rp_id, origin, created_at, last_used_at)
VALUES ('pk-v18', 'admin-earliest', 'key', 'cred-v18', '{}', 'panel.example.com', 'https://panel.example.com', '2026-07-01T00:00:00Z', NULL);
INSERT INTO admin_auth_challenges (id, user_id, kind, session_json, metadata_json, created_at, expires_at)
VALUES ('ch-v18', 'admin-earliest', 'mfa', '{}', '{}', '2026-07-01T00:00:00Z', '2026-07-02T00:00:00Z');
INSERT INTO api_keys (id, name, key_hash, created_at, expires_at, is_active, max_uses, use_count, lookup_digest) VALUES
('key-v18-1', 'client-a', 'hash-a', '2026-06-01T00:00:00Z', NULL, 1, 0, 0, 'digest-a'),
('key-v18-2', 'client-b', 'hash-b', '2026-06-02T00:00:00Z', NULL, 1, 0, 0, 'digest-b');
INSERT INTO registered_clients (id, install_id, display_name, hostname, os, arch, ip, version, created_at, last_seen)
VALUES ('client-target', 'install-target', 'target host', 'target-host', 'linux', 'arm64', '10.0.0.2', 'v0.1.8', '2026-06-01T00:00:00Z', '2026-07-20T00:00:00Z'),
       ('client-ingress', 'install-ingress', 'ingress host', 'ingress-host', 'linux', 'amd64', '10.0.0.3', 'v0.1.8', '2026-06-02T00:00:00Z', '2026-07-20T00:00:00Z');
INSERT INTO client_stats (client_id, cpu_usage, mem_usage, uptime, updated_at)
VALUES ('client-target', 12.5, 33.3, 86400, '2026-07-20T00:00:00Z'),
       ('client-ingress', 3.2, 41.0, 3600, '2026-07-20T00:00:00Z');
INSERT INTO client_tokens (id, token_hash, install_id, key_id, client_id, created_at, last_active_at, last_ip, is_revoked) VALUES
('ct-v18-1', 'token-hash-v18-1', 'install-target', 'key-v18-1', 'client-target', '2026-06-01T00:00:00Z', '2026-07-20T00:00:00Z', '10.0.0.2', 0),
('ct-v18-2', 'token-hash-v18-2', 'install-ingress', 'key-v18-2', 'client-ingress', '2026-06-02T00:00:00Z', '2026-07-20T00:00:00Z', '10.0.0.3', 0);
INSERT INTO allowed_ports (start_port, end_port) VALUES (10000, 20000);
-- 005-era legacy tunnels: ingress_config is '{}', server_expose only, no total_bps column
INSERT INTO tunnels (
	id, name, client_id, type, local_ip, local_port, remote_port, domain, hostname, binding,
	revision, topology, owner_client_id, ingress_location, ingress_client_id, ingress_type, ingress_config,
	ingress_bind_ip, ingress_port, ingress_domain, ingress_path, target_location, target_client_id,
	target_type, target_config, target_host, target_port, target_path, target_resource_key,
	transport_policy, actual_transport, p2p_state, p2p_error, p2p_session_id,
	ingress_bps, egress_bps, desired_state, runtime_state, error, created_by_user_id, created_at, updated_at
) VALUES (
	'tunnel-http-old', 'legacy-web', 'client-target', 'http', '', 8080, 0, 'old.example.com', 'old.example.com', 'client_id',
	1, 'server_expose', 'client-target', 'server', '', 'http_host', '{}',
	'', 0, 'old.example.com', '', 'client', 'client-target',
	'tcp_service', '{}', '127.0.0.1', 8080, '', '',
	'server_relay_only', 'server_relay', 'idle', '', '',
	512, 512, 'running', 'active', '', '', '2026-05-20T00:00:00Z', '2026-07-20T00:00:00Z'
), (
	'tunnel-tcp-old', 'legacy-tcp', 'client-target', 'tcp', '', 5432, 0, '', '', 'client_id',
	1, 'server_expose', 'client-target', 'server', '', 'tcp_listen', '{}',
	'0.0.0.0', 15432, '', '', 'client', 'client-target',
	'tcp_service', '{}', '127.0.0.1', 5432, '', '',
	'server_relay_only', 'server_relay', 'idle', '', '',
	256, 256, 'running', 'active', '', '', '2026-05-21T00:00:00Z', '2026-07-20T00:00:00Z'
);
-- 50k traffic buckets (005-era schema) to exercise migration cost
WITH RECURSIVE cnt(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM cnt WHERE x < 50000)
INSERT INTO traffic_buckets (tunnel_id, owner_client_id, ingress_client_id, target_client_id, topology, transport, client_id, tunnel_name, tunnel_type, resolution, bucket_start, ingress_bytes, egress_bytes)
SELECT 'tunnel-http-old', 'client-target', '', 'client-target', 'server_expose', 'server_relay', 'client-target', 'legacy-web', 'http', '1h', 1789000000 + x, x, x*2 FROM cnt;
SQL
}

scenario_f() {
	local c="$1" tag="v0.1.8" bin="${tag//./_}"
	SCENARIO_FAIL=0
	log "SCENARIO F: full-path upgrade ${tag} -> current (${c})"
	bin_push "${c}" "${ARTIFACTS_DIR}/netsgo-${bin}"
	server_start "${c}" "f-old.log"
	ok "old server (${tag}) ready"

	seed_legacy_data_v18 "${c}"
	# verify seed landed (1 init admin + 2 seeded = 3)
	db_check "${c}" "SELECT count(*) FROM admin_users" "3"
	db_check "${c}" "SELECT count(*) FROM tunnels" "2"
	db_check "${c}" "SELECT count(*) FROM traffic_buckets" "50000"
	db_check "${c}" "SELECT count(*) FROM client_tokens" "2"
	ok "seeded v0.1.8-era production data (50k traffic buckets, 2 admins)"

	server_stop "${c}"
	bin_push "${c}" "${ARTIFACTS_DIR}/netsgo-current"
	local t0 t1
	t0="$(docker exec "${c}" date +%s.%N)"
	server_start_noinit "${c}" "f-current.log"
	t1="$(docker exec "${c}" date +%s.%N)"
	local elapsed
	elapsed="$(awk "BEGIN{print ${t1} - ${t0}}")"
	log "F: startup + migration wall time: ${elapsed}s"
	ok "current server started after full-path upgrade"

	# two seeded admins + the init admin migrated, all with preserved identities
	db_check "${c}" "SELECT count(*) FROM users WHERE is_admin=1 AND status='active'" "3"
	db_check "${c}" "SELECT count(*) FROM users WHERE username='boss' AND totp_enabled=1 AND totp_secret='totp-boss'" "1"
	db_check "${c}" "SELECT count(*) FROM users WHERE username='operator' AND totp_enabled=0" "1"
	# security rows follow their owner
	db_check "${c}" "SELECT count(*) FROM admin_passkeys WHERE user_id=(SELECT id FROM users WHERE username='boss')" "1"
	db_check "${c}" "SELECT count(*) FROM user_sessions" "1"

	# all resources are owned by the earliest admin (ORDER BY created_at, id)
	db_check "${c}" "SELECT count(*) FROM api_keys WHERE owner_user_id=(SELECT id FROM users WHERE username='boss')" "2"
	db_check "${c}" "SELECT count(*) FROM registered_clients WHERE owner_user_id=(SELECT id FROM users WHERE username='boss')" "2"
	db_check "${c}" "SELECT count(*) FROM traffic_buckets WHERE owner_user_id=(SELECT id FROM users WHERE username='boss')" "50000"
	db_check "${c}" "SELECT count(*) FROM tunnels WHERE owner_user_id=(SELECT id FROM users WHERE username='boss')" "2"
	ok "multi-admin ownership backfilled to earliest admin"

	# untouched tables keep data
	db_check "${c}" "SELECT count(*) FROM client_tokens" "2"
	db_check "${c}" "SELECT count(*) FROM allowed_ports" "1"
	db_check "${c}" "SELECT count(*) FROM client_stats" "2"

	# '{}' config legacy tunnels load fine and keep their semantics
	db_check "${c}" "SELECT count(*) FROM tunnels WHERE id='tunnel-http-old' AND topology='server_expose' AND ingress_type='http_host' AND ingress_domain='old.example.com' AND desired_state='running'" "1"
	db_check "${c}" "SELECT count(*) FROM tunnels WHERE id='tunnel-tcp-old' AND ingress_type='tcp_listen' AND ingress_bind_ip='0.0.0.0' AND ingress_port=15432" "1"
	ok "legacy '{}' config tunnels load with correct endpoint semantics"

	# no leftover legacy tables, FK integrity, login works for both admins
	db_check "${c}" "SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('admin_users','admin_sessions','tunnels_multi_user_ownership_legacy')" "0"
	db_check "${c}" "SELECT count(*) FROM pragma_foreign_key_check" "0"

	[ "${SCENARIO_FAIL}" = "0" ] || { fail "F: scenario checks failed"; return 1; }
	log "PASS: scenario F (${tag} -> current full-path) on ${c}"
	return 0
}

# ---------- scenario G: interrupted migration recovery ----------
#
# Kill the current binary while migration 012 is mid-flight (large tables make
# it take long enough to hit), then restart: the transaction must roll back and
# the upgrade must complete cleanly on the next attempt.

scenario_g() {
	local c="$1" tag="v0.1.14" bin="${tag//./_}"
	SCENARIO_FAIL=0
	log "SCENARIO G: interrupted migration recovery (${c})"
	bin_push "${c}" "${ARTIFACTS_DIR}/netsgo-${bin}"
	server_start "${c}" "g-old.log"
	ok "old server ready"

	# large traffic_buckets so migration 012 takes a while
	docker exec -i "$c" sqlite3 "${DATA_DIR}/server/netsgo.db" <<'SQL'
INSERT INTO registered_clients (id, install_id, display_name, hostname, os, arch, ip, version, created_at, last_seen)
VALUES ('client-target', 'install-target', 'target host', 'target-host', 'linux', 'arm64', '10.0.0.2', 'v0.1.14', '2026-06-01T00:00:00Z', '2026-07-20T00:00:00Z');
INSERT INTO tunnels (
	id, name, client_id, type, local_ip, local_port, remote_port, domain, hostname, binding,
	revision, topology, owner_client_id, ingress_location, ingress_client_id, ingress_type, ingress_config,
	ingress_bind_ip, ingress_port, ingress_domain, ingress_path, target_location, target_client_id,
	target_type, target_config, target_host, target_port, target_path, target_resource_key,
	transport_policy, actual_transport, p2p_state, p2p_error, p2p_session_id,
	ingress_bps, egress_bps, desired_state, runtime_state, error, created_by_user_id, created_at, updated_at, total_bps
) VALUES (
	'tunnel-http-1', 'web', 'client-target', 'http', '', 8080, 0, 'app.example.com', 'app.example.com', 'client_id',
	1, 'server_expose', 'client-target', 'server', '', 'http_host', '{"domain":"app.example.com","allowed_source_cidrs":["0.0.0.0/0","::/0"],"auth":{"type":"none"}}',
	'', 0, 'app.example.com', '', 'client', 'client-target',
	'tcp_service', '{"host":"127.0.0.1","port":8080}', '127.0.0.1', 8080, '', '',
	'server_relay_only', 'server_relay', 'idle', '', '',
	1024, 2048, 'running', 'active', '', '', '2026-06-01T00:00:00Z', '2026-07-20T00:00:00Z', 0
);
WITH RECURSIVE cnt(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM cnt WHERE x < 300000)
INSERT INTO traffic_buckets (tunnel_id, owner_client_id, ingress_client_id, target_client_id, topology, transport, client_id, tunnel_name, tunnel_type, resolution, bucket_start, ingress_bytes, egress_bytes)
SELECT 'tunnel-http-1', 'client-target', '', 'client-target', 'server_expose', 'server_relay', 'client-target', 'web', 'http', '1h', 1789000000 + x, x, x*2 FROM cnt;
SQL
	ok "seeded 300k traffic buckets"

	server_stop "${c}"
	bin_push "${c}" "${ARTIFACTS_DIR}/netsgo-current"
	# start the upgrade and kill it shortly after (migration takes seconds on 300k rows)
	docker exec -d "${c}" bash -c "/usr/local/bin/netsgo-current server --data-dir '${DATA_DIR}' --port ${SERVER_PORT} > '${DATA_DIR}/g-current.log' 2>&1"
	sleep 1
	# kill the server mid-migration (SIGKILL simulates power loss / crash)
	docker exec "${c}" bash -c 'for p in $(ls /proc/ | grep -E "^[0-9]+$"); do cmd=$(tr "\0" " " < /proc/$p/cmdline 2>/dev/null); case "$cmd" in /usr/local/bin/netsgo-current*) kill -9 $p 2>/dev/null ;; esac; done; sleep 1'
	docker exec "${c}" bash -c 'for p in $(ls /proc/ | grep -E "^[0-9]+$"); do cmd=$(tr "\0" " " < /proc/$p/cmdline 2>/dev/null); case "$cmd" in /usr/local/bin/netsgo-current*) kill -9 $p 2>/dev/null ;; esac; done; sleep 1'
	ok "killed current server mid-migration"

	# restart: must complete the upgrade successfully
	server_start_noinit "${c}" "g-current2.log"
	ok "current server recovered and completed upgrade"
	db_check "${c}" "SELECT count(*) FROM schema_migrations WHERE name='012_multi_user_ownership'" "1"
	db_check "${c}" "SELECT count(*) FROM users WHERE is_admin=1 AND status='active'" "1"
	db_check "${c}" "SELECT count(*) FROM traffic_buckets WHERE owner_user_id IN (SELECT id FROM users)" "300000"
	db_check "${c}" "SELECT count(*) FROM tunnels WHERE owner_user_id IN (SELECT id FROM users)" "1"
	db_check "${c}" "SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('admin_users','admin_sessions')" "0"
	db_check "${c}" "SELECT count(*) FROM pragma_foreign_key_check" "0"
	ok "G: data complete and consistent after interrupted-then-retried upgrade"
	server_stop "${c}"

	[ "${SCENARIO_FAIL}" = "0" ] || { fail "G: scenario checks failed"; return 1; }
	log "PASS: scenario G (interrupted migration recovery) on ${c}"
	return 0
}

# ---------- main ----------

mkdir -p "${ARTIFACTS_DIR}"

# build binaries
for tag in ${UPGRADE_TAGS}; do
	bin="${tag//./_}"
	if [ ! -x "${ARTIFACTS_DIR}/netsgo-${bin}" ]; then
		build_binary "${tag}" "netsgo-${bin}"
	fi
done
if [ ! -x "${ARTIFACTS_DIR}/netsgo-v0_1_8" ]; then
	# oldest released schema generation; dev tag skips the (incompatible) frontend build
	build_binary v0.1.8 netsgo-v0_1_8 dev
fi
if [ ! -x "${ARTIFACTS_DIR}/netsgo-current" ]; then
	build_binary current netsgo-current
fi

log "binaries ready in ${ARTIFACTS_DIR}"
log "running scenarios on distros: ${DISTROS}"

for distro in ${DISTROS}; do
	log "==================== ${distro} ===================="
	c="netsgo-realenv-${distro}"

	container_start "${distro}" "${c}"

	for tag in ${UPGRADE_TAGS}; do
		run_scenario A scenario_a "${c}" "${tag}"
	done

	run_scenario B scenario_b "${c}"
	run_scenario C scenario_c "${c}"
	run_scenario D scenario_d "${c}"
	run_scenario E scenario_e "${c}"
	run_scenario F scenario_f "${c}"
	run_scenario G scenario_g "${c}"

	container_rm "${c}"
done

log ""
log "============================================="
log "REALENV UPGRADE SUMMARY"
log "============================================="
log "passed: ${PASS}"
log "failed: ${FAIL}"
log "============================================="

[ "${FAIL}" -eq 0 ]
