# panoxy Makefile — low-level build abstraction (LIF-006 standard target set).
# Responsibility split: setup.sh owns the environment; build.sh is the top-level
# entry (both delegate here); this Makefile owns compilation. Developers may
# call make directly. The mihomo kernel is embedded in the single CLI binary,
# so tests need no external mihomo binary.

PANOXY_VERSION ?= $(shell git describe --tags 2>/dev/null || echo "V0.0.1-dev")
PROG           ?= panoxy
PREFIX         ?= /usr/local
BINDIR         ?= $(PREFIX)/bin
DESTDIR        ?=
OUTPUT_DIR     ?= bin
DIST_DIR       ?= dist
HOST_ARCH     := $(shell uname -m | sed -e 's/^x86_64$$/amd64/' -e 's/^aarch64$$/arm64/')
ARCH          ?= $(HOST_ARCH)
GOAMD64       ?= $(shell grep -qw avx2 /proc/cpuinfo 2>/dev/null && echo v3 || echo v1)
DEBUG         ?=

# Release build: strip symbols and build-machine paths (smaller, reproducible).
# DEBUG=1 (wrapped by build.sh --debug): keep symbols, disable optimizations.
ifeq ($(DEBUG),1)
  LDFLAGS  := -X main.version=$(PANOXY_VERSION) -X github.com/deadship2003/panoxy/internal/constants.ProgName=$(PROG)
  TRIMPATH :=
  BUILDEXTRA := -gcflags "all=-N -l"
else
  LDFLAGS  := -s -w -X main.version=$(PANOXY_VERSION) -X github.com/deadship2003/panoxy/internal/constants.ProgName=$(PROG) -buildid=
  TRIMPATH := -trimpath
  BUILDEXTRA :=
endif

# Command echo: silent by default (@ prefix), status lines only.
# To inspect a build without running it, use `make -n` (prints the commands).
Q := @

# Expand the target arches; fail fast on an invalid ARCH instead of a shell error.
ifeq ($(ARCH),all)
  BUILD_ARCHS := amd64 arm64
else ifeq ($(ARCH),amd64)
  BUILD_ARCHS := amd64
else ifeq ($(ARCH),arm64)
  BUILD_ARCHS := arm64
else
  $(error ARCH must be amd64|arm64|all (got: $(ARCH)))
endif

# `make install` installs the host-matching artifact when ARCH=all was used to build.
ifeq ($(ARCH),all)
  INSTALL_ARCH := $(HOST_ARCH)
else
  INSTALL_ARCH := $(ARCH)
endif

.PHONY: all build clean distclean fmt lint test e2e test-all install uninstall help _build-amd64 _build-arm64 _checksums

all: build ## build binaries for the current platform (default)

build: $(addprefix _build-,$(BUILD_ARCHS)) _checksums ## build target binaries into bin/ (ARCH=amd64|arm64|all; DEBUG=1 keeps symbols)

_build-amd64:
	@mkdir -p $(OUTPUT_DIR)
	@echo "  -> building amd64 (GOAMD64=$(GOAMD64), debug=$(DEBUG))"
	$(Q)cd src && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=$(GOAMD64) go build $(TRIMPATH) $(BUILDEXTRA) -ldflags "$(LDFLAGS)" -o ../$(OUTPUT_DIR)/$(PROG)-linux-amd64 ./cmd/panoxy

_build-arm64:
	@mkdir -p $(OUTPUT_DIR)
	@echo "  -> building arm64 (debug=$(DEBUG))"
	$(Q)cd src && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(TRIMPATH) $(BUILDEXTRA) -ldflags "$(LDFLAGS)" -o ../$(OUTPUT_DIR)/$(PROG)-linux-arm64 ./cmd/panoxy

_checksums:
	$(Q)cd $(OUTPUT_DIR) && sha256sum $(PROG)-linux-* > sha256sums.txt 2>/dev/null || true
	@echo "done -> binaries in $(OUTPUT_DIR)/, checksums in $(OUTPUT_DIR)/sha256sums.txt"

install: ## install the built CLI to $(DESTDIR)$(BINDIR)/$(PROG) (run `make build` first; PREFIX/BINDIR/DESTDIR overridable)
	$(Q)install -Dm755 $(OUTPUT_DIR)/$(PROG)-linux-$(INSTALL_ARCH) $(DESTDIR)$(BINDIR)/$(PROG)
	@echo "-> installed $(DESTDIR)$(BINDIR)/$(PROG)"

uninstall: ## remove the installed CLI
	$(Q)rm -f $(DESTDIR)$(BINDIR)/$(PROG)
	@echo "-> removed $(DESTDIR)$(BINDIR)/$(PROG)"

fmt: ## format Go sources (cmd/internal/tests only; the third_party mihomo subtree stays untouched)
	$(Q)cd src && gofmt -l -w cmd internal tests

lint: ## static analysis (go vet)
	$(Q)cd src && go vet ./...

test: ## unit tests (in-process kernel, no external mihomo needed)
	$(Q)cd src && go test ./internal/... -count=1 -timeout 120s

e2e: ## end-to-end tests (~60s; compiles the panoxy single binary itself)
	$(Q)cd src && go test ./tests/ -count=1 -timeout 300s -v

test-all: test e2e ## run every test layer

clean: ## remove build artifacts (bin/) and staging dirs; release packages in $(DIST_DIR) survive
	$(Q)rm -rf $(OUTPUT_DIR)/ $(PROG)-V*/
	@echo "-> cleaned $(OUTPUT_DIR)/ and staging dirs"

distclean: clean ## deep clean: clean + release packages + local env/vendor (never touches global Go caches)
	$(Q)rm -rf $(DIST_DIR)/ src/.env src/vendor
	@echo "-> deep-cleaned (including $(DIST_DIR)/)"

help: ## list all targets
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'
