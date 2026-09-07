#!/usr/bin/env bash
# panoxy build — top-level build entry (LIF-006): compilation is delegated to
# make (this script writes no low-level compile commands itself); it also owns
# packaging/distribution (offline bundles) and the smart-install flow.
# Environment/deps preparation belongs to setup.sh, never here.
# Usage: build.sh [command]
#   build (default)  build.sh [--arch amd64|arm64|all] [--ver V0.0.1] [--prog NAME] [--debug] [--clean]
#                    Builds the current CPU arch by default; amd64 auto-detects AVX2 (v3/v1).
#                    --debug = debug build (symbols, no optimization); --clean wipes bin/ first.
#   install          build.sh install [--bindir /usr/local/bin] [--ver V0.0.1] [--prog NAME]
#                    Installed machine: runs the fresh binary's redeploy — stop service → swap CLI
#                    in place (config & subscriptions kept) → restart → re-mount firewall → health check.
#                    Fresh machine: installs the CLI only (make install), deploy later with init.
#   package          build.sh package [all|amd64|arm64] [--arch ...] [--ver ...] [--prog ...] [--sub-url URL]
#                    Packages the current CPU arch by default; `all` (or --arch all) packages every target.
#   clean            build.sh clean   (bin/ + staging; release packages in dist/ survive — make distclean wipes them)
#   help             build.sh -h|-?|--help
# Options:
#   --arch <amd64|arm64|all>   target arch; defaults to the current platform
#   --ver  <V0.0.1>            version (default git describe; fallback V0.0.1-dev)
#   --prog <panoxy>            program name (build-time injection; decides binary/package names and runtime paths)
#   --debug                    debug build: keep symbols, disable optimizations
#   --clean                    run `make clean` before building
#   --sub-url <URL>            package: when a direct download fails, build a local bootstrap proxy from the subscription
# Environment:
#   PROG             program name (same as --prog, default panoxy)
#   GOAMD64          amd64 build level (auto-detects AVX2 by default; force v1/v3/v4)
#   ASSETS_SRC       local assets dir (default /opt/$PROG; copied when present — offline packaging)
#   PANOXY_BOOT_BIN  bootstrap-proxy CLI (default bin/$PROG-linux-<host_arch>; the kernel is embedded, no external mihomo)
#   PROXY_PORT       bootstrap proxy port (default 33999)
# Layout: raw binaries land in bin/; dist/ holds release packages only
#   (tar.gz offline bundles + checksums; subscription URLs never enter a package).
# Packaging flow: build CLI via make (kernel embedded) → assets (local-first / direct /
#   subscription proxy fallback) → subscription leak scan → assemble → dist/ → prune old outputs.
set -euo pipefail
SELF="$(cd "$(dirname "$0")" && pwd)/$(basename "$0")"
ROOT="$(cd "$(dirname "$SELF")" && pwd)"
cd "$ROOT"

usage() { sed -n '2,/^set -euo/p' "$SELF" | sed '$d; s/^# \{0,1\}//'; exit 0; }

# ---- global state (used by package and the EXIT trap) ----
SUB_URL=""; PROXYX=""; BOOT_DIRF=""; TMP=""
PROG="${PROG:-panoxy}"
PROXY_PORT="${PROXY_PORT:-33999}"
trap 'boot_proxy_stop; rm -rf "$TMP"' EXIT

host_arch() { case "$(uname -m)" in x86_64|amd64) echo amd64 ;; aarch64|arm64) echo arm64 ;; *) echo "" ;; esac; }
envpfx()    { printf '%s' "$PROG" | tr 'a-z-' 'A-Z_'; }
default_ver() { git describe --tags 2>/dev/null || echo "V0.0.1-dev"; }

# ---- build: delegate to make (single compile source of truth) ----
build_cmd() {
  local ARCH="" VER="" DEBUG=0 DOCLEAN=0
  while [ $# -gt 0 ]; do
    case "$1" in
      --arch)  ARCH="$2"; shift 2 ;;
      --ver)   VER="$2"; shift 2 ;;
      --prog)  PROG="$2"; shift 2 ;;
      --debug) DEBUG=1; shift ;;
      --clean) DOCLEAN=1; shift ;;
      -h|-\?|--help) usage ;;
      *) echo "unknown option: $1 (see $0 -h)"; exit 1 ;;
    esac
  done
  [ -n "$ARCH" ] || ARCH="$(host_arch)"
  [ -n "$ARCH" ] || { echo "cannot detect the current arch; pass --arch amd64|arm64|all"; exit 1; }
  [ -n "$VER" ] || VER="$(default_ver)"
  if [ "$DOCLEAN" = 1 ]; then
    make clean >/dev/null
  fi
  local commit; commit="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
  local flavor=""
  [ "$DEBUG" = 1 ] && flavor=", debug"
  echo "== build $PROG $VER (commit $commit, arch: $ARCH$flavor) via make =="
  local mkargs="ARCH=$ARCH PANOXY_VERSION=$VER PROG=$PROG"
  if [ "$DEBUG" = 1 ]; then
    mkargs="$mkargs DEBUG=1"
  fi
  # shellcheck disable=SC2086 # mkargs is intentionally word-split make arguments
  make build $mkargs
  # Light smoke test: run --version on binaries that match the host arch
  # (cross-compiled ones cannot execute here).
  local b HA; HA="$(host_arch)"
  for b in bin/$PROG-linux-*; do
    [ -x "$b" ] || continue
    case "$b" in
      *-linux-"$HA") "$b" --version 2>/dev/null || true ;;
    esac
  done
  echo "== done: bin/$PROG-linux-{amd64,arm64} (as requested) =="
}

clean() {
  make clean
  echo "== cleaned bin/ and staging dirs (release packages in dist/ survive; make distclean wipes those too) =="
}

# ---- install: build the current arch, then pick the right install path ----
#   Installed machine (/etc/<prog>.yaml + installed CLI both present): run the fresh
#   binary's redeploy — stop service + clear firewall → swap CLI/units/man in place →
#   validate + restart → re-mount firewall → health check.
#   Fresh machine: install the CLI only (make install --bindir, default /usr/local/bin),
#   then finish deployment with sudo <prog> init 'SUB_URL'.
# Compilation always runs as the current user (root would pollute the go build
# cache); elevation happens only at the install step.
install_cmd() {
  local BINDIR="/usr/local/bin" VER=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --bindir) BINDIR="$2"; shift 2 ;;
      --ver)    VER="$2"; shift 2 ;;
      --prog)   PROG="$2"; shift 2 ;;
      -h|-\?|--help) usage ;;
      *) echo "unknown option: $1 (see $0 -h)"; exit 1 ;;
    esac
  done
  local HA; HA="$(host_arch)"
  [ -n "$HA" ] || { echo "cannot detect the current arch; install supports this machine only (use the plain build command for cross-compiling)"; exit 1; }
  build_cmd --arch "$HA" --ver "$VER" --prog "$PROG"
  local BIN="$ROOT/bin/$PROG-linux-$HA"
  [ -x "$BIN" ] || { echo "build artifact missing: $BIN"; exit 1; }

  local SUDO=""
  [ "$(id -u)" = 0 ] || SUDO="sudo"
  srun() { if [ -n "$SUDO" ]; then "$SUDO" "$@"; else "$@"; fi; }

  # installed-machine detection: the same <PROG>_CONF / <PROG>_CLI env overrides the CLI uses
  local pfx conf cli
  pfx="$(envpfx)"
  conf="$(printenv "${pfx}_CONF" 2>/dev/null || true)"; conf="${conf:-/etc/$PROG.yaml}"
  cli="$(printenv "${pfx}_CLI" 2>/dev/null || true)";  cli="${cli:-/usr/local/bin/$PROG}"

  if [ -f "$conf" ] && [ -x "$cli" ]; then
    echo "== installed machine: in-place redeploy via the fresh binary (stop service → swap CLI → restart → re-mount firewall → health check) =="
    srun "$BIN" redeploy
    echo "== done: $cli refreshed (config and subscription data untouched) =="
    return 0
  fi
  echo "== not installed ($conf or $cli missing): installing the CLI only; afterwards run sudo $PROG init 'SUB_URL' =="
  srun make install BINDIR="$BINDIR" ARCH="$HA" PROG="$PROG"
  srun "$BINDIR/$PROG" --version 2>/dev/null || true
  echo "== done: $BINDIR/$PROG =="
}

# ---- subscription bootstrap proxy: when GitHub is unreachable directly,
# build a local proxy from a subscription node and download through it.
# The kernel is embedded in the CLI, so the bootstrap proxy is plain `$PROG run`
# (no external mihomo binary involved).
boot_proxy() {
  [ -n "$SUB_URL" ] || return 1
  local HA; HA="$(host_arch)"
  local BOOT_BIN="${PANOXY_BOOT_BIN:-$ROOT/bin/$PROG-linux-$HA}"
  [ -x "$BOOT_BIN" ] || BOOT_BIN="$(command -v "$PROG" 2>/dev/null || true)"
  [ -x "$BOOT_BIN" ] || { echo "      WARN no bootstrap CLI ($BOOT_BIN), cannot download via subscription"; return 1; }
  local d; d="$(mktemp -d)"
  cat > "$d/boot.yaml" <<YEOF
mixed-port: $PROXY_PORT
mode: rule
log-level: warning
proxy-providers:
  boot:
    type: http
    url: "$SUB_URL"
    path: ./boot.sub.yaml
    interval: 86400
    health-check: {enable: false}
proxy-groups:
  - {name: P, type: select, use: [boot]}
rules:
  - MATCH,P
YEOF
  (cd "$d" && "$(envpfx)"_ROOT="$d" "$(envpfx)"_CONF="$d/boot.yaml" nohup "$BOOT_BIN" run > boot.log 2>&1 & echo $! > "$d/pid")
  local i ok=0
  for i in $(seq 1 25); do
    if curl -s -m 3 -x "http://127.0.0.1:$PROXY_PORT" -o /dev/null https://www.gstatic.com/generate_204; then ok=1; break; fi
    sleep 1
  done
  if [ "$ok" = 1 ]; then
    echo "$d" > "$BOOT_DIRF"
    echo "      bootstrap proxy up via subscription (127.0.0.1:$PROXY_PORT)"
    PROXYX="http://127.0.0.1:$PROXY_PORT"
    return 0
  fi
  echo "      WARN bootstrap proxy not ready (subscription unreachable?)"; kill "$(cat "$d/pid")" 2>/dev/null; rm -rf "$d"; return 1
}

boot_proxy_stop() {
  [ -s "$BOOT_DIRF" ] || return 0
  local d; d="$(cat "$BOOT_DIRF")"
  [ -f "$d/pid" ] && kill "$(cat "$d/pid")" 2>/dev/null
  rm -rf "$d"; : > "$BOOT_DIRF"
}

# dl: direct first (short timeout); on failure with SUB_URL set, go through the bootstrap proxy.
dl() {
  curl -fsSL --connect-timeout 6 --retry 1 -o "$1" "$2" && return 0
  [ -n "$SUB_URL" ] || return 1
  [ -z "$PROXYX" ] && { boot_proxy || return 1; }
  curl -fsSL --connect-timeout 10 --retry 2 -x "$PROXYX" -o "$1" "$2"
}

# ---- subscription leak scan: no real-subscription marker may enter a public package ----
leak_scan() {
  local dir="$1"
  if grep -rInE 'token=[A-Za-z0-9]{8,}|SUB_URL_PLACEHOLDER.*http|/subscribe\?|client/subscribe' "$dir" \
     --include='*.yaml' --include='*.tpl' --include='*.yml' 2>/dev/null | grep -v 'SUB_URL_PLACEHOLDER'; then
    echo "!! suspected real subscription link detected, aborting packaging (private subscriptions must never enter a public Release)" >&2
    exit 1
  fi
}

build_one() {
  local arch="$1"
  local pkg="${PROG}-${VER}-${arch}"
  rm -rf "$pkg"; mkdir -p "$pkg/assets/geo" "$pkg/assets/ui/official" "$pkg/assets/rule"
  cp "bin/$PROG-linux-$arch" "$pkg/$PROG"; chmod +x "$pkg/$PROG"
  cp "$TMP"/GeoIP.dat "$TMP"/GeoSite.dat "$TMP"/Country.mmdb "$pkg/assets/geo/"
  tar xzf "$TMP/ui.tgz" -C "$pkg/assets/ui/official"
  test -f "$pkg/assets/ui/official/index.html" || { echo "UI package is abnormal"; exit 1; }
  cp "$TMP/HyperADRules-Ads.yaml" "$pkg/assets/rule/"
  cp README.md "$pkg/"
  cp LICENSE THIRD_PARTY_NOTICES "$pkg/" 2>/dev/null || true
  cp -r LICENSES "$pkg/LICENSES" 2>/dev/null || true
  leak_scan "$pkg"
  mkdir -p dist && tar -czf "dist/$pkg.tar.gz" "$pkg"
  (cd dist && sha256sum "$pkg.tar.gz" > "$pkg.tar.gz.sha256")
  rm -rf "$pkg"   # remove the staging dir, keep only the dist/ artifact
  echo "      produced: $pkg.tar.gz"
}

# cleanup_old removes artifacts of older versions: dist/ keeps only this
# version's packages, and leftover staging dirs are pruned. Older versions can
# always be rebuilt from git, so dist/ must not accumulate.
cleanup_old() {
  local f d
  for f in dist/${PROG}-*.tar.gz dist/${PROG}-*.tar.gz.sha256; do
    [ -e "$f" ] || continue
    [[ "$f" == dist/${PROG}-"$VER"-*.tar.gz* ]] && continue
    rm -f "$f"
  done
  for d in ${PROG}-*; do
    [ -d "$d" ] || continue
    [[ "$d" == ${PROG}-"$VER"-* ]] && continue
    rm -rf "$d"
  done
}

package_cmd() {
  local ARCH=""
  VER=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --arch) ARCH="$2"; shift 2 ;;
      --ver)  VER="$2"; shift 2 ;;
      --prog) PROG="$2"; shift 2 ;;
      --sub-url) SUB_URL="$2"; shift 2 ;;
      all|amd64|arm64) [ -n "$ARCH" ] && { echo "conflicting positional arg and --arch: $1"; exit 1; }; ARCH="$1"; shift ;;
      -h|-\?|--help) usage ;;
      *) echo "unknown option: $1 (see $0 -h)"; exit 1 ;;
    esac
  done
  [ -n "$ARCH" ] || ARCH="$(host_arch)"          # package the current CPU arch by default
  [ -n "$ARCH" ] || { echo "cannot detect the current arch; pass --arch amd64|arm64|all (or the positional equivalent)"; exit 1; }
  [ -n "$VER" ] || VER="$(default_ver)"
  # local assets source (offline packaging): copied first when present, downloaded only when missing
  local SRC="${ASSETS_SRC:-/opt/$PROG}"

  BOOT_DIRF="$(mktemp -d)/${PROG}-boot-proxy.dir"; : > "$BOOT_DIRF" 2>/dev/null || BOOT_DIRF="/tmp/${PROG}-boot-proxy.$$.dir"

  echo "== [1/5] build (CLI, --arch $ARCH) via make =="
  build_cmd --arch "$ARCH" --ver "$VER" --prog "$PROG"

  echo "== [2/5] fetch assets (local first: $SRC; download only when missing) =="
  TMP="$(mktemp -d)"
  # geo trio
  local geo="https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest"
  local f
  for f in GeoIP.dat GeoSite.dat Country.mmdb; do
    if [ -f "$SRC/$f" ]; then cp "$SRC/$f" "$TMP/$f"; echo "      local: $f"
    else dl "$TMP/$f" "$geo/$(echo $f | tr 'A-Z' 'a-z' | sed 's/\.dat$/.dat/;s/country\.mmdb/country.mmdb/')" || true; fi
  done
  # ad-block rules
  if [ -f "$SRC/rule_provider/HyperADRules-Ads.yaml" ]; then cp "$SRC/rule_provider/HyperADRules-Ads.yaml" "$TMP/"; echo "      local: HyperADRules-Ads.yaml"
  else dl "$TMP/HyperADRules-Ads.yaml" "https://github.com/Lynricsy/HyperADRules/releases/latest/download/hyper_adrules_ads_clash.yaml" || true; fi
  # web panel
  if [ -d "$SRC/ui/official" ] && [ -f "$SRC/ui/official/index.html" ]; then
    (cd "$SRC/ui/official" && tar czf "$TMP/ui.tgz" .); echo "      local: metacubexd UI"
  else dl "$TMP/ui.tgz" "https://github.com/MetaCubeX/metacubexd/releases/latest/download/compressed-dist.tgz" || true; fi
  [ -s "$TMP/Country.mmdb" ] && [ -s "$TMP/HyperADRules-Ads.yaml" ] && [ -s "$TMP/ui.tgz" ] || { echo "geo/rules/UI assets incomplete (neither local nor network available)"; exit 1; }

  echo "== [3/5] subscription leak scan =="
  leak_scan .

  echo "== [4/5] assemble =="
  case "$ARCH" in
    amd64|arm64) build_one "$ARCH" ;;
    all) build_one amd64; build_one arm64 ;;
    *) echo "--arch must be amd64|arm64|all"; exit 1 ;;
  esac

  echo "== [5/5] done =="
  cleanup_old
  ls -la dist/${PROG}-*.tar.gz* 2>/dev/null | tail -4
}

case "${1:-}" in
  -h|-\?|--help) usage ;;
  clean)  clean ;;
  install) shift; install_cmd "$@" ;;
  package) shift; package_cmd "$@" ;;
  *)      build_cmd "$@" ;;
esac
