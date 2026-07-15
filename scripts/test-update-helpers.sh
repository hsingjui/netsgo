#!/bin/sh

set -eu

ROOT="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

assert_contains() {
  haystack="$1"
  needle="$2"
  case "$haystack" in
    *"$needle"*) ;;
    *) fail "expected output to contain: $needle; actual: $haystack" ;;
  esac
}

assert_not_contains() {
  haystack="$1"
  needle="$2"
  case "$haystack" in
    *"$needle"*) fail "expected output not to contain: $needle; actual: $haystack" ;;
    *) ;;
  esac
}

stat_mode_text_local() {
  stat -c '%A' "$1" 2>/dev/null || stat -f '%Sp' "$1"
}

helpers_without_shebang() {
  # Keep the test process in control: load helper functions but not set -eu.
  sed '1,3d' "$ROOT/scripts/common-update.sh"
}

run_helper() {
  code="$1"
  sh -s <<EOF
$(helpers_without_shebang)
$code
EOF
}

safe_root="$(mktemp -d)"
trap 'rm -rf "$safe_root"' EXIT HUP INT TERM

safe_cache="$safe_root/cache"
safe_cache_physical="$(CDPATH='' cd -- "$safe_root" && pwd -P)/cache"
# shellcheck disable=SC2016 # run_helper evaluates this literal in its child shell.
safe_cache_physical="$safe_cache_physical" NETSGO_UPDATE_CACHE_DIR="$safe_cache" run_helper '
  dir="$(cache_dir_for v1.2.3 linux_amd64)"
  [ "$dir" = "$safe_cache_physical/v1.2.3/linux_amd64" ] || die "unexpected cache dir: $dir"
  [ -d "$safe_cache_physical" ] || die "override cache root was not created"
  mode="$(stat_mode_text "$NETSGO_UPDATE_CACHE_DIR")"
  case "$mode" in ?????w*|????????w*) die "override cache root stayed writable: $mode" ;; esac
' || fail "safe NETSGO_UPDATE_CACHE_DIR should pass"

unsafe_cache="$safe_root/world-writable-cache"
mkdir -p "$unsafe_cache"
chmod 0777 "$unsafe_cache"
if output="$(NETSGO_UPDATE_CACHE_DIR="$unsafe_cache" run_helper 'cache_dir_for v1.2.3 linux_amd64' 2>&1)"; then
  fail "world-writable NETSGO_UPDATE_CACHE_DIR should be rejected"
fi
assert_contains "$output" "不得 group/world 可写"

symlink_target="$safe_root/symlink-target"
mkdir -p "$symlink_target"
symlink_cache="$safe_root/symlink-cache"
ln -s "$symlink_target" "$symlink_cache"
if output="$(NETSGO_UPDATE_CACHE_DIR="$symlink_cache" run_helper 'cache_dir_for v1.2.3 linux_amd64' 2>&1)"; then
  fail "symlink NETSGO_UPDATE_CACHE_DIR should be rejected"
fi
assert_contains "$output" "符号链接更新缓存路径"

unsafe_parent="$safe_root/world-writable-parent"
mkdir -p "$unsafe_parent/cache"
chmod 0777 "$unsafe_parent"
if output="$(NETSGO_UPDATE_CACHE_DIR="$unsafe_parent/cache" run_helper 'cache_dir_for v1.2.3 linux_amd64' 2>&1)"; then
  fail "cache below a replaceable parent should be rejected"
fi
assert_contains "$output" "父路径可被其他用户替换子路径"

unsafe_tmp="$safe_root/world-writable-tmp"
mkdir -p "$unsafe_tmp"
chmod 0777 "$unsafe_tmp"
if output="$(TMPDIR="$unsafe_tmp" run_helper 'private_temp_dir netsgo-test' 2>&1)"; then
  fail "world-writable non-sticky TMPDIR should be rejected"
fi
assert_contains "$output" "未受 root sticky bit 保护"


# Simulate a noexec preferred temp root: installation must continue from a safe fallback.
noexec_exec_tmp="$safe_root/noexec-exec-tmp"
fallback_exec_tmp="$safe_root/fallback-exec-tmp"
mkdir -m 700 "$noexec_exec_tmp" "$fallback_exec_tmp"
# shellcheck disable=SC2016 # run_helper evaluates this literal in its child shell.
exec_tmp_output="$(NETSGO_EXEC_TMPDIR="$noexec_exec_tmp" TMPDIR="$fallback_exec_tmp" NOEXEC_EXEC_TMP="$noexec_exec_tmp" run_helper '
  can_execute_file() {
    case "$1" in */noexec-exec-tmp/*) return 1 ;; *) return 0 ;; esac
  }
  private_executable_temp_dir netsgo-install
')"
fallback_exec_tmp_physical="$(CDPATH='' cd -- "$fallback_exec_tmp" && pwd -P)"
case "$exec_tmp_output" in
  "$fallback_exec_tmp_physical"/netsgo-install.*) ;;
  *) fail "executable temp directory did not fall back: $exec_tmp_output" ;;
esac
[ -d "$exec_tmp_output" ] || fail "fallback executable temp directory missing"
rm -rf "$exec_tmp_output"

if output="$(NETSGO_EXEC_TMPDIR="$noexec_exec_tmp" TMPDIR="$fallback_exec_tmp" run_helper '
  can_execute_file() { return 1; }
  private_executable_temp_dir netsgo-install
' 2>&1)"; then
  fail "all non-executable temporary roots should be rejected"
fi
assert_contains "$output" "未找到可执行且安全的临时目录"
grep -q 'private_executable_temp_dir netsgo-install' "$ROOT/scripts/install.sh" || fail "install.sh must use an executable temporary directory"
unset NETSGO_UPDATE_CACHE_DIR

# Default cache roots must be private mktemp directories, not a predictable /tmp/netsgo-update-cache tree.
safe_root_physical="$(CDPATH='' cd -- "$safe_root" && pwd -P)"
default_output="$(TMPDIR="$safe_root" run_helper 'cache_dir_for v1.2.3 linux_amd64')"
case "$default_output" in
  "$safe_root_physical"/netsgo-update-cache.*'/v1.2.3/linux_amd64') ;;
  *) fail "default cache dir should use private mktemp root, got: $default_output" ;;
esac
default_root="${default_output%/v1.2.3/linux_amd64}"
[ -d "$default_root" ] || fail "default private cache root missing"
mode="$(stat_mode_text_local "$default_root")"
case "$mode" in ?????w*|????????w*) fail "default private cache root is writable by group/world: $mode" ;; esac
case "$default_root" in "$safe_root/netsgo-update-cache") fail "default cache root is still predictable" ;; esac

case "$default_root" in "${TMPDIR:-/tmp}"/*|"$safe_root"/*|"$safe_root_physical"/*) ;; *) fail "unexpected default cache root: $default_root" ;; esac

# Standalone update scripts must stay POSIX-shell parseable after helper generation.
sh -n "$ROOT/scripts/install.sh" || fail "install.sh syntax check failed"
sh -n "$ROOT/scripts/upgrade.sh" || fail "upgrade.sh syntax check failed"

if grep -q -- '--progress-bar' "$ROOT/scripts/common-update.sh" "$ROOT/scripts/install.sh" "$ROOT/scripts/upgrade.sh"; then
  fail "update scripts must not render curl progress control sequences"
fi
grep -q 'TERM=dumb' "$ROOT/scripts/install.sh" || fail "install.sh must disable terminal probing for scripted runs"
grep -q 'TERM=dumb' "$ROOT/scripts/upgrade.sh" || fail "upgrade.sh must disable terminal probing for scripted runs"
grep -q 'systemd-run --quiet --wait --service-type=exec' "$ROOT/scripts/upgrade.sh" || fail "upgrade.sh must wait for the real upgrade exit status"
grep -q 'trusted_upgrade_root="/var/lib/netsgo-upgrade"' "$ROOT/scripts/upgrade.sh" || fail "upgrade.sh must stage runners outside /run"
if grep -qE 'detach_upgrade_binary|nohup|systemd-run .*--no-block' "$ROOT/scripts/upgrade.sh"; then
  fail "upgrade.sh must not use unobservable detached upgrade execution"
fi

# Exercise confirmation behavior, not just marker strings.
confirmation_functions="$(sed -n '/^apply_upgrade_confirmation() {/,/^}/p' "$ROOT/scripts/upgrade.sh")"
cancel_output="$(sh -c '
  set -eu
  yes=0
  cancelled=0
  die() { printf "DIE:%s\n" "$*" >&2; exit 1; }
  eval "$1"
  if apply_upgrade_confirmation no; then exit 9; fi
  [ "$cancelled" -eq 1 ]
' sh "$confirmation_functions" 2>&1)" || fail "no confirmation must cancel successfully"
assert_contains "$cancel_output" "升级已取消，未进行任何修改"
assert_not_contains "$cancel_output" "DIE:"

sh -c '
  set -eu
  yes=0
  cancelled=0
  die() { printf "DIE:%s\n" "$*" >&2; exit 1; }
  eval "$1"
  apply_upgrade_confirmation yes
  [ "$yes" -eq 1 ]
' sh "$confirmation_functions" || fail "yes confirmation should continue"

confirm_function="$(sed -n '/^confirm_upgrade() {/,/^}/p' "$ROOT/scripts/upgrade.sh")"
if NETSGO_UPGRADE_TTY="$safe_root/not-a-tty" sh -c '
  set -eu
  yes=0
  die() { exit 1; }
  is_tty_path() { return 1; }
  eval "$1"
  confirm_upgrade
' sh "$confirm_function"; then
  fail "confirmation must reject non-TTY paths"
fi


# Exercise the systemd-run wrapper with a root-owned test staging root.
upgrade_runtime_functions="$(sed -n '/^ensure_root_access() {/,/^confirm_upgrade() {/p' "$ROOT/scripts/upgrade.sh" | sed '$d')"
runtime_root="$safe_root/trusted-runtime"
trusted_test_owner="$(id -u):$(id -g)"
mkdir -p "$runtime_root"
chmod 700 "$runtime_root"
fake_bin_dir="$safe_root/fake-bin"
mkdir -p "$fake_bin_dir"
cat >"$fake_bin_dir/id" <<'EOF'
#!/bin/sh
if [ "${1:-}" = "-u" ]; then
  printf '0\n'
  exit 0
fi
exec /usr/bin/id "$@"
EOF
chmod 700 "$fake_bin_dir/id"
cat >"$fake_bin_dir/systemd-run" <<'EOF'
#!/bin/sh
while [ "$#" -gt 0 ]; do
  case "$1" in
    --unit=*) shift ;;
    --*) shift ;;
    *) break ;;
  esac
done
"$@"
EOF
chmod 700 "$fake_bin_dir/systemd-run"
test_root_path="$fake_bin_dir:$PATH"

cat >"$safe_root/upgrade-success" <<'EOF'
#!/bin/sh
[ "$1" = upgrade ] || exit 91
[ "$2" = -y ] || exit 92
exit 0
EOF
chmod 700 "$safe_root/upgrade-success"
PATH="$test_root_path" sh -c '
  set -eu
  die() { printf "DIE:%s\n" "$*" >&2; exit 1; }
  log() { :; }
  warn() { :; }
  eval "$1"
  trusted_root_owner="$4"
  trusted_upgrade_root="$2"
  trusted_root_path="$5"
  run_upgrade_binary "$3" 0
' sh "$upgrade_runtime_functions" "$runtime_root" "$safe_root/upgrade-success" "$trusted_test_owner" "$test_root_path" || fail "successful upgrade status should propagate"

cat >"$safe_root/upgrade-failure" <<'EOF'
#!/bin/sh
exit 42
EOF
chmod 700 "$safe_root/upgrade-failure"
if output="$(PATH="$test_root_path" sh -c '
  set -eu
  die() { printf "DIE:%s\n" "$*" >&2; exit 1; }
  log() { :; }
  warn() { :; }
  eval "$1"
  trusted_upgrade_root="$2"
  trusted_root_owner="$4"
  trusted_root_path="$5"
  run_upgrade_binary "$3" 0
' sh "$upgrade_runtime_functions" "$runtime_root" "$safe_root/upgrade-failure" "$trusted_test_owner" "$test_root_path" 2>&1)"; then
  fail "failed upgrade status must propagate"
fi
assert_contains "$output" "退出码 42"

# Non-root staging must retain sudo state after preparation and use a fixed env path.
cat >"$fake_bin_dir/id-nonroot" <<'EOF'
#!/bin/sh
if [ "${1:-}" = "-u" ]; then
  printf '1000\n'
  exit 0
fi
exec /usr/bin/id "$@"
EOF
chmod 700 "$fake_bin_dir/id-nonroot"
cat >"$fake_bin_dir/sudo" <<'EOF'
#!/bin/sh
if [ "${1:-}" = "-v" ] || { [ "${1:-}" = "-n" ] && [ "${2:-}" = "-v" ]; }; then
  exit 0
fi
[ "${1:-}" = "-n" ] || exit 96
shift
"$@"
EOF
chmod 700 "$fake_bin_dir/sudo"
if output="$(PATH="$test_root_path" sh -c '
  set -eu
  die() { printf "DIE:%s\n" "$*" >&2; exit 1; }
  log() { :; }
  warn() { :; }
  eval "$1"
  fake_id="$2"
  id() { "$fake_id" "$@"; }
  sudo_candidates="$3"
  env_candidates="$4"
  trusted_upgrade_root="$5"
  trusted_root_owner="$6"
  trusted_root_path="$7"
  run_upgrade_binary "$8" 0
' sh "$upgrade_runtime_functions" "$fake_bin_dir/id-nonroot" "$fake_bin_dir/sudo" /usr/bin/env "$runtime_root" "$trusted_test_owner" "$test_root_path" "$safe_root/upgrade-failure" 2>&1)"; then
  fail "non-root failed upgrade status must propagate"
fi
assert_contains "$output" "退出码 42"
printf 'PASS: update helper cache hardening\n'
