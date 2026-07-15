#!/bin/sh

set -eu

# BEGIN NETSGO COMMON UPDATE HELPERS
NETSGO_LATEST_CNB="https://cnb.cool/zsio/netsgo/-/git/raw/release-index/updates/index-v1/latest.json"
NETSGO_LATEST_GITHUB="https://raw.githubusercontent.com/zsio/netsgo/release-index/updates/index-v1/latest.json"

# Release public keys are derived from the private release signing key stored in
# NETSGO_RELEASE_SIGNING_KEY_PEM. Commit public keys here so install/upgrade
# scripts can verify release checksums without trusting HTTPS alone.
# BEGIN NETSGO RELEASE PUBLIC KEYS
NETSGO_RELEASE_PUBLIC_KEY_PEM='-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAH4VWaTpLBw8/WXELyluQChFm5Fi1qI2E8DSOwYKpRCc=
-----END PUBLIC KEY-----'
NETSGO_RELEASE_ALLOWED_SIGNERS='netsgo-release ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB+FVmk6SwcPP1lxC8pbkAoRZuRYtaiNhPA0jsGCqUQn'
# END NETSGO RELEASE PUBLIC KEYS

die() {
  printf '%s\n' "$*" >&2
  exit 1
}

log() {
  printf '==> %s\n' "$*" >&2
}

warn() {
  printf 'WARN: %s\n' "$*" >&2
}

is_tty_path() {
  exec 8<"$1"
  if tty -s <&8 2>/dev/null; then
    exec 8<&-
    return 0
  fi
  exec 8<&-
  return 1
}

require_linux_systemd() {
  log "检查 Linux + systemd 环境"
  [ "$(uname -s)" = "Linux" ] || die "此脚本只支持 Linux + systemd。请前往 GitHub Releases 手动下载。"
  command -v systemctl >/dev/null 2>&1 || die "未找到 systemctl。请前往 GitHub Releases 手动下载。"
  systemctl --version >/dev/null 2>&1 || die "systemd 不可用。请前往 GitHub Releases 手动下载。"
}

require_tools() {
  log "检查依赖工具"
  for tool in curl tar sha256sum jq awk sed sort grep head dirname rm mv mkdir mktemp chmod id stat cp tee tty; do
    command -v "$tool" >/dev/null 2>&1 || die "缺少依赖: $tool"
  done
}

source_order() {
  case "$1" in
    cnb) printf '%s\n' cnb github ;;
    github) printf '%s\n' github cnb ;;
    auto) printf '%s\n' cnb github ;;
    *) die "--source 仅支持 auto|cnb|github" ;;
  esac
}

latest_url_for_provider() {
  case "$1" in
    cnb) printf '%s\n' "$NETSGO_LATEST_CNB" ;;
    github) printf '%s\n' "$NETSGO_LATEST_GITHUB" ;;
    *) return 1 ;;
  esac
}

fetch_latest_index() {
  source="$1"
  out="$2"
  for provider in $(source_order "$source"); do
    url="$(latest_url_for_provider "$provider")"
    if curl -fsSL "$url" -o "$out"; then
      printf '%s\n' "$provider"
      return 0
    fi
  done
  return 1
}

release_detail_url() {
  provider="$1"
  tag="$2"
  case "$provider" in
    cnb) printf 'https://cnb.cool/zsio/netsgo/-/git/raw/release-index/updates/index-v1/releases/%s.json\n' "$tag" ;;
    github) printf 'https://raw.githubusercontent.com/zsio/netsgo/release-index/updates/index-v1/releases/%s.json\n' "$tag" ;;
    *) return 1 ;;
  esac
}

fetch_release_detail() {
  source="$1"
  tag="$2"
  out="$3"
  for provider in $(source_order "$source"); do
    url="$(release_detail_url "$provider" "$tag")"
    if download_official "$url" "$out"; then
      printf '%s\n' "$provider"
      return 0
    fi
  done
  return 1
}

json_get_channel_latest() {
  file="$1"
  channel="$2"
  jq -r --arg channel "$channel" '.channels[$channel].latest // empty' "$file"
}

valid_release_tag() {
  printf '%s\n' "$1" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-beta\.[1-9][0-9]*)?$'
}

extract_comparable_version() {
  text="$1"
  for word in $text; do
    word="${word%,}"
    word="${word#(}"
    word="${word%)}"
    case "$word" in
      v*-*-g*)
        base="${word%%-[0-9]*-g*}"
        if valid_release_tag "$base"; then
          printf '%s\n' "$base"
          return 0
        fi
        ;;
      v*)
        if valid_release_tag "$word"; then
          printf '%s\n' "$word"
          return 0
        fi
        ;;
    esac
  done
  return 1
}

extract_exact_release_version() {
  text="$1"
  for word in $text; do
    word="${word%,}"
    word="${word#(}"
    word="${word%)}"
    if valid_release_tag "$word"; then
      printf '%s\n' "$word"
      return 0
    fi
  done
  return 1
}

canonical_platform() {
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64) arch="amd64" ;;
    aarch64|arm64) arch="arm64" ;;
    armv7l|armv7*) arch="armv7" ;;
    *) die "不支持的架构: $arch" ;;
  esac
  [ "$os" = "linux" ] || die "脚本只支持 Linux"
  printf '%s_%s\n' "$os" "$arch"
}

asset_name_for() {
  tag="$1"
  platform="$2"
  printf 'netsgo_%s_%s.tar.gz\n' "${tag#v}" "$platform"
}

official_url_allowed() {
  case "$1" in
    https://github.com/zsio/netsgo/releases/download/*) return 0 ;;
    https://raw.githubusercontent.com/zsio/netsgo/release-index/*) return 0 ;;
    https://cnb.cool/zsio/netsgo/-/releases/download/*) return 0 ;;
    https://cnb.cool/zsio/netsgo/-/git/raw/release-index/*) return 0 ;;
    *) return 1 ;;
  esac
}

download_official() {
  url="$1"
  out="$2"
  official_url_allowed "$url" || die "拒绝非官方下载 URL: $url"
  reject_symlink_path "$out"
  tmp_out="${out}.part.$$"
  rm -f "$tmp_out"
  log "下载 $url"
  if curl -fL --silent --show-error "$url" -o "$tmp_out"; then
    reject_symlink_path "$out"
    mv "$tmp_out" "$out"
    chmod 600 "$out" 2>/dev/null || true
    return 0
  fi
  rm -f "$tmp_out"
  return 1
}

json_url_for_name_provider() {
  file="$1"
  name="$2"
  provider="$3"
  jq -r --arg name "$name" --arg provider "$provider" '
    [
      .checksum_asset?,
      .signature_assets.ed25519?,
      .signature_assets.sshsig?,
      (.assets[]?)
    ]
    | map(select(.name == $name) | .urls[]? | select(.provider == $provider) | .url)
    | .[0] // empty
  ' "$file"
}

download_release_detail_file() {
  detail="$1"
  source="$2"
  name="$3"
  out="$4"
  for provider in $(source_order "$source"); do
    url="$(json_url_for_name_provider "$detail" "$name" "$provider")"
    [ -n "$url" ] || continue
    if download_official "$url" "$out"; then
      printf '%s\n' "$provider"
      return 0
    fi
  done
  return 1
}

validate_release_detail() {
	detail="$1"
	tag="$2"
	asset="$3"
	jq -e --arg tag "$tag" --arg asset "$asset" '
	  .schema == 1 and
	  .project == "netsgo" and
	  .version == $tag and
	  .checksum_asset.name == "checksums.txt" and
	  (.checksum_asset.urls | type == "array" and length > 0) and
	  .signature_assets.ed25519.name == "checksums.txt.sig" and
	  (.signature_assets.ed25519.urls | type == "array" and length > 0) and
	  .signature_assets.sshsig.name == "checksums.txt.sshsig" and
	  (.signature_assets.sshsig.urls | type == "array" and length > 0) and
	  any(.assets[]?; .name == $asset and .os == "linux" and (.urls | type == "array" and length > 0))
	' "$detail" >/dev/null || die "release detail 无效或缺少当前平台资产: $asset"
}

verify_checksum() {
  checksums="$1"
  archive="$2"
  name="$3"
  checksum_matches "$checksums" "$archive" "$name" || die "checksum mismatch: $name"
}

checksum_matches() {
  checksums="$1"
  archive="$2"
  name="$3"
  [ -s "$checksums" ] || return 1
  [ -s "$archive" ] || return 1
  expected="$(awk -v n="$name" '$2 == n {print $1}' "$checksums" | head -1)"
  [ -n "$expected" ] || return 1
  actual="$(sha256sum "$archive" | awk '{print $1}')"
  [ "$actual" = "$expected" ]
}

verify_signature_openssl() {
  checksums="$1"
  sig="$2"
  [ -n "$NETSGO_RELEASE_PUBLIC_KEY_PEM" ] || return 1
  command -v openssl >/dev/null 2>&1 || return 1
  pub="$(private_temp_file netsgo-release-public-key)"
  printf '%s\n' "$NETSGO_RELEASE_PUBLIC_KEY_PEM" > "$pub"
  if openssl pkeyutl -verify -pubin -inkey "$pub" -rawin -in "$checksums" -sigfile "$sig" >/dev/null 2>&1; then
    rm -f "$pub"
    return 0
  fi
  rm -f "$pub"
  return 1
}

verify_signature_sshsig() {
  checksums="$1"
  sshsig="$2"
  [ -n "$NETSGO_RELEASE_ALLOWED_SIGNERS" ] || return 1
  command -v ssh-keygen >/dev/null 2>&1 || return 1
  allowed="$(private_temp_file netsgo-release-allowed-signers)"
  printf '%s\n' "$NETSGO_RELEASE_ALLOWED_SIGNERS" > "$allowed"
  if ssh-keygen -Y verify -f "$allowed" -I netsgo-release -n file -s "$sshsig" < "$checksums" >/dev/null 2>&1; then
    rm -f "$allowed"
    return 0
  fi
  rm -f "$allowed"
  return 1
}

verify_signature() {
  checksums="$1"
  sig="$2"
  sshsig="$3"
  signature_valid "$checksums" "$sig" "$sshsig" || die "无法验证 checksums.txt 签名，已终止。"
}

signature_valid() {
  checksums="$1"
  sig="$2"
  sshsig="$3"
  if verify_signature_openssl "$checksums" "$sig"; then
    return 0
  fi
  if verify_signature_sshsig "$checksums" "$sshsig"; then
    return 0
  fi
  return 1
}

download_available_signatures() {
  detail="$1"
  source="$2"
  sig="$3"
  sshsig="$4"
  downloaded=0
  if command -v openssl >/dev/null 2>&1; then
    if download_release_detail_file "$detail" "$source" checksums.txt.sig "$sig" >/dev/null; then
      downloaded=1
    fi
  fi
  if command -v ssh-keygen >/dev/null 2>&1; then
    if download_release_detail_file "$detail" "$source" checksums.txt.sshsig "$sshsig" >/dev/null; then
      downloaded=1
    fi
  fi
  [ "$downloaded" -eq 1 ] || die "无法下载可用的 checksums.txt 签名，已终止。"
}

extract_netsgo() {
  archive="$1"
  dest="$2"
  log "解压 NetsGo 二进制"
  mkdir -p "$(dirname "$dest")"
  tar -xzf "$archive" -C "$(dirname "$dest")" --strip-components=1 --wildcards '*/netsgo' 2>/dev/null ||
    tar -xzf "$archive" -C "$(dirname "$dest")" netsgo
  [ -f "$dest" ] && [ ! -L "$dest" ] || die "release archive 中的 netsgo 不是普通文件"
  chmod +x "$dest"
}

stat_owner_uid() {
  stat -c '%u' "$1" 2>/dev/null || stat -f '%u' "$1"
}

stat_mode_text() {
  stat -c '%A' "$1" 2>/dev/null || stat -f '%Sp' "$1"
}

reject_symlink_path() {
  [ ! -L "$1" ] || die "拒绝使用符号链接更新缓存路径: $1"
}

system_tmp_root="${TMPDIR:-/tmp}"

mode_group_or_world_writable() {
  case "$1" in
    ?????w*|????????w*) return 0 ;;
    *) return 1 ;;
  esac
}

mode_has_sticky_bit() {
  case "$1" in
    d????????t|d????????T) return 0 ;;
    *) return 1 ;;
  esac
}

validate_system_tmp_root() {
  netsgo_tmp_root="$system_tmp_root"
  case "$netsgo_tmp_root" in
    /*) ;;
    *) die "系统临时目录必须是绝对路径: $netsgo_tmp_root" ;;
  esac
  [ -d "$netsgo_tmp_root" ] || die "系统临时目录不存在: $netsgo_tmp_root"
  case "$netsgo_tmp_root" in
    /) ;;
    */) netsgo_tmp_root="${netsgo_tmp_root%/}" ;;
  esac
  netsgo_tmp_root="$(physical_directory "$netsgo_tmp_root")" || die "无法解析系统临时目录物理路径: $netsgo_tmp_root"
  [ ! -L "$netsgo_tmp_root" ] || die "系统临时目录不得是符号链接: $netsgo_tmp_root"

  netsgo_tmp_owner="$(stat_owner_uid "$netsgo_tmp_root")" || die "无法读取系统临时目录属主: $netsgo_tmp_root"
  netsgo_tmp_mode="$(stat_mode_text "$netsgo_tmp_root")" || die "无法读取系统临时目录权限: $netsgo_tmp_root"
  netsgo_tmp_uid="$(id -u)"
  if mode_group_or_world_writable "$netsgo_tmp_mode"; then
    if [ "$netsgo_tmp_owner" != "0" ] || ! mode_has_sticky_bit "$netsgo_tmp_mode"; then
      die "系统临时目录可被其他用户替换文件且未受 root sticky bit 保护: $netsgo_tmp_root"
    fi
  elif [ "$netsgo_tmp_owner" != "$netsgo_tmp_uid" ] && [ "$netsgo_tmp_owner" != "0" ]; then
    die "系统临时目录属主不可信: $netsgo_tmp_root"
  fi
  validate_private_path_ancestors "$netsgo_tmp_root"
  system_tmp_root="$netsgo_tmp_root"
}

private_temp_dir() {
  netsgo_tmp_prefix="$1"
  validate_system_tmp_root
  old_umask="$(umask)"
  umask 077
  netsgo_tmp_path="$(mktemp -d "$system_tmp_root/$netsgo_tmp_prefix.XXXXXXXXXX")" || {
    umask "$old_umask"
    die "无法创建私有临时目录"
  }
  umask "$old_umask"
  chmod 700 "$netsgo_tmp_path" || die "无法保护私有临时目录: $netsgo_tmp_path"
  printf '%s\n' "$netsgo_tmp_path"
}

can_execute_file() {
  "$1" >/dev/null 2>&1
}

private_executable_temp_dir() {
  netsgo_tmp_prefix="$1"
  original_system_tmp_root="$system_tmp_root"
  for candidate in "${NETSGO_EXEC_TMPDIR:-}" "$original_system_tmp_root" /var/tmp /tmp; do
    [ -n "$candidate" ] || continue
    system_tmp_root="$candidate"
    if ! netsgo_exec_tmp_path="$(private_temp_dir "$netsgo_tmp_prefix")" 2>/dev/null; then
      continue
    fi
    netsgo_exec_probe="$netsgo_exec_tmp_path/.netsgo-exec-probe"
    if printf '%s\n' '#!/bin/sh' 'exit 0' >"$netsgo_exec_probe" && chmod 700 "$netsgo_exec_probe" && can_execute_file "$netsgo_exec_probe"; then
      rm -f "$netsgo_exec_probe"
      system_tmp_root="$original_system_tmp_root"
      printf '%s\n' "$netsgo_exec_tmp_path"
      return 0
    fi
    rm -rf "$netsgo_exec_tmp_path"
  done
  system_tmp_root="$original_system_tmp_root"
  die "未找到可执行且安全的临时目录；请设置 NETSGO_EXEC_TMPDIR 指向可执行目录"
}

private_temp_file() {
  netsgo_tmp_prefix="$1"
  validate_system_tmp_root
  old_umask="$(umask)"
  umask 077
  netsgo_tmp_path="$(mktemp "$system_tmp_root/$netsgo_tmp_prefix.XXXXXXXXXX")" || {
    umask "$old_umask"
    die "无法创建私有临时文件"
  }
  umask "$old_umask"
  chmod 600 "$netsgo_tmp_path" || die "无法保护私有临时文件: $netsgo_tmp_path"
  printf '%s\n' "$netsgo_tmp_path"
}

validate_private_path_ancestors() {
  netsgo_ancestor_path="$1"
  while [ "$netsgo_ancestor_path" != "/" ]; do
    netsgo_ancestor_path="$(dirname "$netsgo_ancestor_path")"
    [ -d "$netsgo_ancestor_path" ] || die "更新缓存父路径不是目录: $netsgo_ancestor_path"
    netsgo_ancestor_owner="$(stat_owner_uid "$netsgo_ancestor_path")" || die "无法读取更新缓存父路径属主: $netsgo_ancestor_path"
    netsgo_ancestor_uid="$(id -u)"
    if [ "$netsgo_ancestor_owner" != "$netsgo_ancestor_uid" ] && [ "$netsgo_ancestor_owner" != "0" ]; then
      die "更新缓存父路径属主不可信: $netsgo_ancestor_path"
    fi
    netsgo_ancestor_mode="$(stat_mode_text "$netsgo_ancestor_path")" || die "无法读取更新缓存父路径权限: $netsgo_ancestor_path"
    if mode_group_or_world_writable "$netsgo_ancestor_mode"; then
      if [ "$netsgo_ancestor_owner" != "0" ] || ! mode_has_sticky_bit "$netsgo_ancestor_mode"; then
        die "更新缓存父路径可被其他用户替换子路径: $netsgo_ancestor_path"
      fi
    fi
  done
}

physical_directory() {
  CDPATH='' cd -- "$1" 2>/dev/null && pwd -P
}

default_cache_root() {
  private_temp_dir netsgo-update-cache
}

validate_cache_root() {
  root="$1"
  case "$root" in
    /*) ;;
    *) die "NETSGO_UPDATE_CACHE_DIR 必须是绝对路径: $root" ;;
  esac
  reject_symlink_path "$root"
  if [ -e "$root" ] && [ ! -d "$root" ]; then
    die "更新缓存路径不是目录: $root"
  fi
  if [ ! -e "$root" ]; then
    old_umask="$(umask)"
    umask 077
    mkdir -p "$root" || { umask "$old_umask"; die "无法创建更新缓存目录: $root"; }
    umask "$old_umask"
  fi
  physical_root="$(physical_directory "$root")" || die "无法解析更新缓存物理路径: $root"
  root="$physical_root"
  reject_symlink_path "$root"
  [ -d "$root" ] || die "更新缓存路径不是目录: $root"

  owner_uid="$(stat_owner_uid "$root")" || die "无法读取更新缓存目录属主: $root"
  current_uid="$(id -u)"
  if [ "$owner_uid" != "$current_uid" ]; then
    die "更新缓存目录必须归当前用户所有: $root"
  fi

  mode_text="$(stat_mode_text "$root")" || die "无法读取更新缓存目录权限: $root"
  case "$mode_text" in
    ?????w*|????????w*) die "更新缓存目录不得 group/world 可写: $root" ;;
  esac
  validate_private_path_ancestors "$root"
  validated_cache_root="$root"
}

cache_root() {
  if [ -n "${NETSGO_UPDATE_CACHE_DIR:-}" ]; then
    validate_cache_root "$NETSGO_UPDATE_CACHE_DIR"
    printf '%s\n' "$validated_cache_root"
    return 0
  fi
  default_cache_root
}

cache_dir_for() {
  if [ -n "${NETSGO_UPDATE_CACHE_DIR:-}" ]; then
    validate_cache_root "$NETSGO_UPDATE_CACHE_DIR"
    root="$validated_cache_root"
  else
    root="$(default_cache_root)"
  fi
  tag="$1"
  platform="$2"
  printf '%s/%s/%s\n' "$root" "$tag" "$platform"
}

ensure_cache_dir() {
  cache_dir="$1"
  parent="$(dirname "$cache_dir")"
  reject_symlink_path "$parent"
  reject_symlink_path "$cache_dir"
  mkdir -p "$cache_dir" || die "无法创建更新缓存目录: $cache_dir"
  reject_symlink_path "$parent"
  reject_symlink_path "$cache_dir"
  [ -d "$cache_dir" ] || die "更新缓存路径不是目录: $cache_dir"
  chmod 700 "$parent" "$cache_dir" || die "无法保护更新缓存目录: $cache_dir"
}

cleanup_empty_cache_parents() {
  cache_dir="$1"
  if [ -n "${NETSGO_UPDATE_CACHE_DIR:-}" ]; then
    root="$NETSGO_UPDATE_CACHE_DIR"
  else
    root="$(dirname "$(dirname "$cache_dir")")"
  fi
  parent="$(dirname "$cache_dir")"
  if [ "$parent" != "$root" ] && [ -d "$parent" ]; then
    rmdir "$parent" 2>/dev/null || true
  fi
  if [ -d "$root" ]; then
    rmdir "$root" 2>/dev/null || true
  fi
}

ensure_release_detail_cached() {
  source="$1"
  tag="$2"
  asset="$3"
  cache_dir="$4"
  out="$cache_dir/release.json"
  ensure_cache_dir "$cache_dir"
  reject_symlink_path "$out"
  if [ -s "$out" ] && validate_release_detail "$out" "$tag" "$asset" >/dev/null 2>&1; then
    log "复用已下载的 release detail: $out"
    printf '%s\n' "$out"
    return 0
  fi
  [ ! -e "$out" ] || warn "已下载的 release detail 无效，将重新下载: $out"
  rm -f "$out"
  fetch_release_detail "$source" "$tag" "$out" >/dev/null || die "无法获取 release detail: $tag"
  validate_release_detail "$out" "$tag" "$asset"
  printf '%s\n' "$out"
}

ensure_checksums_cached() {
  detail="$1"
  source="$2"
  cache_dir="$3"
  checksums="$cache_dir/checksums.txt"
  sig="$cache_dir/checksums.txt.sig"
  sshsig="$cache_dir/checksums.txt.sshsig"
  ensure_cache_dir "$cache_dir"
  reject_symlink_path "$checksums"
  reject_symlink_path "$sig"
  reject_symlink_path "$sshsig"
  if [ -s "$checksums" ] && signature_valid "$checksums" "$sig" "$sshsig"; then
    log "复用已下载并验签的 checksums.txt"
    printf '%s\n' "$checksums"
    return 0
  fi
  if [ -e "$checksums" ] || [ -e "$sig" ] || [ -e "$sshsig" ]; then
    warn "已下载的 checksum 或签名无效，将重新下载"
  fi
  rm -f "$checksums" "$sig" "$sshsig"
  download_release_detail_file "$detail" "$source" checksums.txt "$checksums" >/dev/null || die "无法下载 checksums.txt"
  download_available_signatures "$detail" "$source" "$sig" "$sshsig"
  verify_signature "$checksums" "$sig" "$sshsig"
  log "checksums.txt 签名验证通过"
  printf '%s\n' "$checksums"
}

ensure_archive_cached() {
  detail="$1"
  source="$2"
  asset="$3"
  cache_dir="$4"
  checksums="$5"
  archive="$cache_dir/$asset"
  ensure_cache_dir "$cache_dir"
  reject_symlink_path "$archive"
  if [ -s "$archive" ] && checksum_matches "$checksums" "$archive" "$asset"; then
    log "复用已下载并校验的 release archive: $archive"
    printf '%s\n' "$archive"
    return 0
  fi
  [ ! -e "$archive" ] || warn "已下载的 release archive 校验失败，将重新下载: $archive"
  rm -f "$archive"
  download_release_detail_file "$detail" "$source" "$asset" "$archive" >/dev/null || die "无法下载 release archive: $asset"
  if ! checksum_matches "$checksums" "$archive" "$asset"; then
    rm -f "$archive"
    die "checksum mismatch: $asset"
  fi
  log "release archive SHA256 校验通过"
  printf '%s\n' "$archive"
}

version_sort_key() {
  v="${1#v}"
  core="${v%%-*}"
  pre=""
  [ "$core" = "$v" ] || pre="${v#*-}"
  major="$(printf '%s' "$core" | awk -F. '{print $1}')"
  minor="$(printf '%s' "$core" | awk -F. '{print $2}')"
  patch="$(printf '%s' "$core" | awk -F. '{print $3}')"
  if [ -z "$pre" ]; then
    pre_rank=1
    beta_num=999999999
  else
    pre_rank=0
    beta_num="$(printf '%s' "$pre" | sed -n 's/^beta\.\([1-9][0-9]*\)$/\1/p')"
    [ -n "$beta_num" ] || beta_num=0
  fi
  printf '%09d.%09d.%09d.%d.%09d\n' "$major" "$minor" "$patch" "$pre_rank" "$beta_num"
}

semver_gt() {
  a="$1"
  b="$2"
  [ "$(printf '%s %s\n%s %s\n' "$(version_sort_key "$a")" "$a" "$(version_sort_key "$b")" "$b" | sort | tail -1 | awk '{print $2}')" = "$a" ] && [ "$a" != "$b" ]
}

semver_eq() {
  [ "$1" = "$2" ]
}

select_highest_version() {
  best=""
  for candidate in "$@"; do
    [ -n "$candidate" ] || continue
    valid_release_tag "$candidate" || continue
    if [ -z "$best" ] || semver_gt "$candidate" "$best"; then
      best="$candidate"
    fi
  done
  [ -n "$best" ] || return 1
  printf '%s\n' "$best"
}

channel_for_target() {
  case "$1" in
    *-beta.*) printf '%s\n' beta ;;
    *) printf '%s\n' stable ;;
  esac
}
# END NETSGO COMMON UPDATE HELPERS

cleanup_paths=""
cache_dir=""
completed=0
cancelled=0
cleanup() {
  for path in $cleanup_paths; do
    [ -n "$path" ] && rm -rf "$path"
  done
  if [ "$completed" -eq 1 ]; then
    if [ -n "$cache_dir" ]; then
      if rm -rf "$cache_dir"; then
        cleanup_empty_cache_parents "$cache_dir"
        log "已清理下载缓存: $cache_dir"
      else
        warn "升级已完成，但清理下载缓存失败: $cache_dir"
      fi
    fi
  elif [ "$cancelled" -eq 1 ]; then
    [ -z "$cache_dir" ] || log "升级已取消，保留已验证下载缓存: $cache_dir"
  elif [ -n "$cache_dir" ]; then
    warn "升级未完成，已保留下载缓存以便下次重试: $cache_dir"
  fi
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

sudo_bin=""
env_bin=""
sudo_candidates="/usr/bin/sudo /bin/sudo"
env_candidates="/usr/bin/env /bin/env"
trusted_root_path="/usr/sbin:/usr/bin:/sbin:/bin"

ensure_root_access() {
  if [ "$(id -u)" -eq 0 ]; then
    return 0
  fi
  for candidate in $sudo_candidates; do
    if [ -x "$candidate" ]; then
      sudo_bin="$candidate"
      break
    fi
  done
  for candidate in $env_candidates; do
    if [ -x "$candidate" ]; then
      env_bin="$candidate"
      break
    fi
  done
  [ -n "$env_bin" ] || die "升级需要 env"
  [ -n "$sudo_bin" ] || die "升级需要 sudo"
  "$sudo_bin" -v || die "无法获取 sudo 权限，升级未启动"
  "$sudo_bin" -n -v || die "无法无交互获取 sudo 权限，升级未启动"
}

run_as_root() {
  if [ "$(id -u)" -eq 0 ]; then
    PATH="$trusted_root_path" "$@"
  else
    "$sudo_bin" -n "$env_bin" PATH="$trusted_root_path" "$@"
  fi
}

trusted_upgrade_root="/var/lib/netsgo-upgrade"
trusted_root_owner="root:root"
prepared_upgrade_dir=""

prepare_trusted_upgrade_dir() {
  source_bin="$1"
  command -v cp >/dev/null 2>&1 || die "缺少依赖: cp"
  ensure_root_access

  trusted_root="$trusted_upgrade_root"
  run_as_root mkdir -p "$trusted_root" || die "无法创建可信升级目录: $trusted_root"
  run_as_root chown "$trusted_root_owner" "$trusted_root" || die "无法保护可信升级目录: $trusted_root"
  run_as_root chmod 700 "$trusted_root" || die "无法保护可信升级目录: $trusted_root"
  trusted_dir="$(run_as_root mktemp -d "$trusted_root/run.XXXXXXXXXX")" || die "无法创建可信升级工作目录"
  run_as_root chown "$trusted_root_owner" "$trusted_dir" || die "无法保护可信升级工作目录: $trusted_dir"
  run_as_root chmod 700 "$trusted_dir" || die "无法保护可信升级工作目录: $trusted_dir"
  run_as_root cp "$source_bin" "$trusted_dir/netsgo" || {
    run_as_root rm -rf "$trusted_dir" || true
    die "无法准备可信升级二进制"
  }
  run_as_root chown "$trusted_root_owner" "$trusted_dir/netsgo" || die "无法保护可信升级二进制"
  run_as_root chmod 700 "$trusted_dir/netsgo" || die "无法保护可信升级二进制"
  command -v tee >/dev/null 2>&1 || die "缺少依赖: tee"
  run_as_root tee "$trusted_dir/run.sh" >/dev/null <<'EOF'
#!/bin/sh
set -eu
trusted_dir="$1"
force_arg="$2"
trap 'rm -rf "$trusted_dir"' EXIT
status=0
if [ "$force_arg" -eq 1 ]; then
  TERM=dumb "$trusted_dir/netsgo" upgrade -f -y || status=$?
else
  TERM=dumb "$trusted_dir/netsgo" upgrade -y || status=$?
fi
exit "$status"
EOF
  run_as_root chown "$trusted_root_owner" "$trusted_dir/run.sh" || die "无法保护可信升级 runner"
  run_as_root chmod 700 "$trusted_dir/run.sh" || die "无法保护可信升级 runner"
  prepared_upgrade_dir="$trusted_dir"
}

run_upgrade_binary() {
  source_bin="$1"
  force_arg="$2"
  command -v systemd-run >/dev/null 2>&1 || die "缺少依赖: systemd-run"

  prepare_trusted_upgrade_dir "$source_bin"
  trusted_dir="$prepared_upgrade_dir"
  unit="netsgo-upgrade-${trusted_dir##*.}.service"
  log "执行升级并等待结果（unit=${unit}）"
  status=0
  run_as_root systemd-run --quiet --wait --service-type=exec --unit="$unit" \
    "$trusted_dir/run.sh" "$trusted_dir" "$force_arg" || status=$?
  if [ "$status" -eq 0 ]; then
    log "升级完成"
    return 0
  fi
  die "升级失败（退出码 ${status}）；请运行 journalctl -u ${unit} 查看日志。"
}

apply_upgrade_confirmation() {
  answer="$1"
  case "$answer" in
    y|Y|yes|YES|Yes) yes=1 ;;
    n|N|no|NO|No)
      cancelled=1
      printf '升级已取消，未进行任何修改。\n'
      return 1
      ;;
    *) die "请输入 yes 或 no" ;;
  esac
}

confirm_upgrade() {
  [ "$yes" -eq 1 ] && return 0
  tty_path="${NETSGO_UPGRADE_TTY:-/dev/tty}"
  is_tty_path "$tty_path" || die "upgrade without -y/--yes must be run from an interactive TTY"
  printf '用本次下载的 NetsGo 文件替换已安装版本？输入 yes 继续，或输入 no 取消。\n' >"$tty_path"
  answer=""
  IFS= read -r answer <"$tty_path" || die "无法读取升级确认"
  apply_upgrade_confirmation "$answer"
}

source="auto"
channel="auto"
force=0
yes=0

while [ "$#" -gt 0 ]; do
  case "$1" in
    --source)
      [ "$#" -ge 2 ] || die "--source requires a value"
      source="$2"
      shift 2
      ;;
    --channel)
      [ "$#" -ge 2 ] || die "--channel requires a value"
      channel="$2"
      shift 2
      ;;
    -f|--force)
      force=1
      shift
      ;;
    -y|--yes)
      yes=1
      shift
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
done

case "$channel" in auto|stable|beta) ;; *) die "--channel 仅支持 auto|stable|beta" ;; esac

require_linux_systemd
require_tools

log "检查是否存在 NetsGo 托管服务"
if ! systemctl list-unit-files 'netsgo-*.service' 2>/dev/null | grep -q '^netsgo-'; then
  die "未检测到 NetsGo 托管服务，拒绝下载。"
fi

installed_bin="${NETSGO_INSTALLED_BIN:-/usr/local/bin/netsgo}"
[ -x "$installed_bin" ] || die "未找到已安装二进制 $installed_bin"
log "读取已安装版本: $installed_bin"
installed_version="$(TERM=dumb "$installed_bin" --version || true)"
installed_base="$(extract_comparable_version "$installed_version" || true)"

tmp="$(private_temp_dir netsgo-upgrade-download)"
cleanup_paths="$cleanup_paths $tmp"

log "获取 release index（source=${source}, channel=${channel}）"
provider="$(fetch_latest_index "$source" "$tmp/latest.json")" || die "无法获取 release index"
target_channel="$channel"
if [ "$channel" = "auto" ]; then
  case "$installed_base" in
    *-beta.*) target_channel="auto-beta" ;;
    *) target_channel="stable" ;;
  esac
fi
if [ "$target_channel" = "auto-beta" ]; then
  stable_target="$(json_get_channel_latest "$tmp/latest.json" stable || true)"
  beta_target="$(json_get_channel_latest "$tmp/latest.json" beta || true)"
  target="$(select_highest_version "$stable_target" "$beta_target")" || die "release index 中缺少有效 stable/beta 版本"
  target_channel="$(channel_for_target "$target")"
else
  target="$(json_get_channel_latest "$tmp/latest.json" "$target_channel")"
fi
if [ -z "$target" ] || ! valid_release_tag "$target"; then
  die "release index 中缺少有效 $target_channel 版本"
fi
log "目标版本: $target"

if [ "$force" -ne 1 ]; then
  [ -n "$installed_base" ] || die "当前版本不可比较；如需强制替换，请使用 -f。"
  if semver_eq "$installed_base" "$target"; then
    printf '当前已是目标版本 %s，不下载、不替换、不重启。\n' "$target"
    exit 0
  fi
  if semver_gt "$installed_base" "$target"; then
    die "目标版本 ${target} 低于当前版本 ${installed_base}；如需强制降级，请使用 -f。"
  fi
fi

platform="$(canonical_platform)"
asset="$(asset_name_for "$target" "$platform")"
log "当前平台: $platform"
cache_dir="$(cache_dir_for "$target" "$platform")"
log "下载缓存目录: $cache_dir"

release_detail="$(ensure_release_detail_cached "$source" "$target" "$asset" "$cache_dir")"
checksums="$(ensure_checksums_cached "$release_detail" "$source" "$cache_dir")"
archive="$(ensure_archive_cached "$release_detail" "$source" "$asset" "$cache_dir" "$checksums")"
extract_netsgo "$archive" "$tmp/netsgo"

log "验证临时 NetsGo 版本"
version_output="$(TERM=dumb "$tmp/netsgo" --version)"
version="$(extract_exact_release_version "$version_output" || true)"
[ "$version" = "$target" ] || die "临时 netsgo 版本不匹配: want $target, got $version_output"

if confirm_upgrade; then
  run_upgrade_binary "$tmp/netsgo" "$force"
  completed=1
fi
