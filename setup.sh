#!/usr/bin/env bash
# panoxy setup — environment & dependency preparation (LIF-006).
# Idempotent: repeated runs never error, already-satisfied dependencies are
# skipped. It only fetches toolchains/dependencies and runs prechecks — it
# never compiles anything (that belongs to make / build.sh).
# ./setup.sh --check runs in CI-preflight mode: verify only, install nothing.
set -euo pipefail
cd "$(dirname "$0")"

# Minimum Go toolchain required by src/go.mod.
GO_NEED_MAJOR=1
GO_NEED_MINOR=23
CHECK=0
[ "${1:-}" = "--check" ] && CHECK=1

die() { echo "setup: $*" >&2; exit 1; }
info() { echo "  ok  $*"; }
miss() { echo "  MISSING  $*" >&2; }

# ---- 1. Go toolchain (>= 1.23) ----
# Not auto-installed: distro packages are routinely older than required, so we
# print the canonical install path instead of installing something unusable.
go_bin="$(command -v go 2>/dev/null || true)"
if [ -z "$go_bin" ]; then
	miss "go >= ${GO_NEED_MAJOR}.${GO_NEED_MINOR} not found"
	echo "  install it from https://go.dev/dl/ (or: rm -rf /usr/local/go && tar -C /usr/local -xzf go<ver>.linux-*.tar.gz), then re-run ./setup.sh" >&2
	exit 1
fi
goversion_ok() {
	# "go version go1.24.1 linux/amd64" -> major/minor comparison
	local v maj min
	v="$(go version | awk '{print $3}')"          # go1.24.1
	v="${v#go}"
	maj="${v%%.*}"
	min="${v#*.}"; min="${min%%.*}"
	[ "$maj" -gt "$GO_NEED_MAJOR" ] && return 0
	[ "$maj" -eq "$GO_NEED_MAJOR" ] && [ "$min" -ge "$GO_NEED_MINOR" ] && return 0
	return 1
}
if goversion_ok; then
	info "go toolchain: $(go version | awk '{print $3}') at $go_bin"
else
	miss "go $(go version | awk '{print $3}') is older than ${GO_NEED_MAJOR}.${GO_NEED_MINOR}"
	echo "  upgrade from https://go.dev/dl/, then re-run ./setup.sh" >&2
	exit 1
fi

# ---- 2. Go module dependencies ----
# Pre-fetches the module graph so builds never touch the network (except the
# vendored third_party/mihomo subtree, which is a separate module kept in-tree).
if [ "$CHECK" = 1 ]; then
	if (cd src && go mod verify >/dev/null 2>&1); then
		info "module cache: verified"
	else
		info "module cache: not fully downloaded yet (run ./setup.sh without --check to fetch)"
	fi
else
	echo "== fetching Go module dependencies =="
	(cd src && go mod download)
	info "module dependencies: downloaded"
fi

# ---- 3. Optional build-time tools (informational only) ----
if command -v git >/dev/null 2>&1; then
	info "git: present (version stamping via git describe)"
else
	info "git: absent (builds fall back to the V0.0.1-dev version stamp)"
fi

if [ "$CHECK" = 1 ]; then
	echo "== setup --check: environment ready =="
else
	echo "== setup complete; build with ./build.sh (or make) =="
fi
