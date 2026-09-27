#!/bin/sh
set -eu

BASE_URL="${NETSGO_BOOTSTRAP_BASE_URL:-http://server:8080}"
MANAGEMENT_HOST="${NETSGO_MANAGEMENT_HOST:-}"
ADMIN_USER="${NETSGO_ADMIN_USER:-admin}"
ADMIN_PASS="${NETSGO_ADMIN_PASS:?NETSGO_ADMIN_PASS is required}"
SHARED_DIR="${NETSGO_MULTI_USER_SHARED_DIR:-/shared}"
USER_A="${NETSGO_MULTI_USER_A:-playwright-user-a}"
USER_B="${NETSGO_MULTI_USER_B:-playwright-user-b}"
PASSWORD="${NETSGO_MULTI_USER_PASSWORD:-PlaywrightUser123!}"
WAIT_TIMEOUT="${NETSGO_BOOTSTRAP_WAIT_TIMEOUT:-180}"
BOOTSTRAP_READY_FILE="${NETSGO_BOOTSTRAP_READY_FILE:-}"

tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

log() {
	printf '[multi-user-fixture] %s\n' "$*"
}

if [ -f "${SHARED_DIR}/multi-user.ready" ]; then
	log "multi-user fixture is already ready"
	exit 0
fi

deadline() {
	expr "$(date +%s)" + "${WAIT_TIMEOUT}"
}

http_json() {
	method="$1"
	url="$2"
	body_file="$3"
	output_file="$4"
	shift 4
	if [ -n "${body_file}" ]; then
		if [ -n "${MANAGEMENT_HOST}" ]; then
			curl -sS -o "${output_file}" -w '%{http_code}' -X "${method}" \
				-H 'Content-Type: application/json' -H "Host: ${MANAGEMENT_HOST}" "$@" \
				--data @"${body_file}" "${url}"
		else
			curl -sS -o "${output_file}" -w '%{http_code}' -X "${method}" \
				-H 'Content-Type: application/json' "$@" \
				--data @"${body_file}" "${url}"
		fi
		return
	fi
	if [ -n "${MANAGEMENT_HOST}" ]; then
		curl -sS -o "${output_file}" -w '%{http_code}' -X "${method}" -H "Host: ${MANAGEMENT_HOST}" "$@" "${url}"
	else
		curl -sS -o "${output_file}" -w '%{http_code}' -X "${method}" "$@" "${url}"
	fi
}

login_payload="${tmpdir}/login.json"
login_resp="${tmpdir}/login.resp"
jq -n --arg username "${ADMIN_USER}" --arg password "${ADMIN_PASS}" \
	'{username:$username,password:$password}' >"${login_payload}"

log "waiting for admin API"
if [ -n "${BOOTSTRAP_READY_FILE}" ]; then
	end_ts="$(deadline)"
	while [ ! -f "${BOOTSTRAP_READY_FILE}" ] && [ "$(date +%s)" -lt "${end_ts}" ]; do
		sleep 1
	done
	if [ ! -f "${BOOTSTRAP_READY_FILE}" ]; then
		log "timed out waiting for the global client bootstrap"
		exit 1
	fi
fi
admin_token=""
end_ts="$(deadline)"
while [ "$(date +%s)" -lt "${end_ts}" ]; do
	code="$(http_json POST "${BASE_URL}/api/auth/login" "${login_payload}" "${login_resp}")" || code=""
	if [ "${code}" = "200" ]; then
		admin_token="$(jq -r '.token // empty' "${login_resp}")"
		if [ -n "${admin_token}" ]; then
			break
		fi
	fi
	sleep 1
done
if [ -z "${admin_token}" ]; then
	log "failed to obtain admin token"
	cat "${login_resp}" >&2 || true
	exit 1
fi

create_user() {
	username="$1"
	user_id_file="$2"
	payload="${tmpdir}/${username}.json"
	response="${tmpdir}/${username}.resp"
	list_response="${tmpdir}/${username}.list"
	jq -n --arg username "${username}" --arg password "${PASSWORD}" \
		'{username:$username,password:$password}' >"${payload}"
	code="$(http_json POST "${BASE_URL}/api/admin/users" "${payload}" "${response}" -H "Authorization: Bearer ${admin_token}")" || code=""
	if [ "${code}" = "201" ]; then
		jq -r '.id' "${response}" >"${user_id_file}"
		return
	fi
	if [ "${code}" != "409" ]; then
		log "failed to create ${username}: HTTP ${code}"
		cat "${response}" >&2 || true
		exit 1
	fi
	encoded_username="$(printf '%s' "${username}" | sed 's/ /%20/g')"
	code="$(http_json GET "${BASE_URL}/api/admin/users?query=${encoded_username}" '' "${list_response}" -H "Authorization: Bearer ${admin_token}")" || code=""
	if [ "${code}" != "200" ]; then
		log "failed to find existing ${username}: HTTP ${code}"
		cat "${list_response}" >&2 || true
		exit 1
	fi
	user_id="$(jq -r --arg username "${username}" '.items[] | select(.username == $username) | .id' "${list_response}" | head -n 1)"
	if [ -z "${user_id}" ]; then
		log "existing ${username} was not returned by the API"
		exit 1
	fi
	printf '%s\n' "${user_id}" >"${user_id_file}"
}

create_key() {
	user_id="$1"
	name="$2"
	key_file="$3"
	payload="${tmpdir}/${name}.json"
	response="${tmpdir}/${name}.resp"
	jq -n --arg name "${name}" '{name:$name,permissions:["connect"]}' >"${payload}"
	code="$(http_json POST "${BASE_URL}/api/admin/users/${user_id}/keys" "${payload}" "${response}" -H "Authorization: Bearer ${admin_token}")" || code=""
	if [ "${code}" != "201" ]; then
		log "failed to create ${name}: HTTP ${code}"
		cat "${response}" >&2 || true
		exit 1
	fi
	api_key="$(jq -r '.raw_key // empty' "${response}")"
	if [ -z "${api_key}" ]; then
		log "empty key returned for ${name}"
		exit 1
	fi
	printf '%s' "${api_key}" >"${key_file}"
}

mkdir -p "${SHARED_DIR}"
create_user "${USER_A}" "${SHARED_DIR}/user-a.id"
create_user "${USER_B}" "${SHARED_DIR}/user-b.id"
create_key "$(cat "${SHARED_DIR}/user-a.id")" "playwright-user-a-key" "${SHARED_DIR}/user-a.key"
create_key "$(cat "${SHARED_DIR}/user-b.id")" "playwright-user-b-key" "${SHARED_DIR}/user-b.key"
touch "${SHARED_DIR}/multi-user.ready"
log "multi-user fixture is ready"
