<div align="center">

# panoxy

**A Linux transparent-proxy gateway deploy/management tool built on the [mihomo](https://github.com/MetaCubeX/mihomo) core**

[![Go](https://img.shields.io/badge/Go-1.23+-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License](https://img.shields.io/badge/License-GPL%203.0-blue)](LICENSE)
[![Platform](https://img.shields.io/badge/Platform-Linux%20amd64%7Carm64-lightgrey)]()
[![Release](https://img.shields.io/badge/Release-V0.0.1-orange)](../../releases)

Single binary · zero dependencies · transactional deployment · automatic rollback

</div>

**What it does.** panoxy turns one Linux box into a transparent proxy gateway for your entire network. Install it on a single machine and every device behind it — phones, laptops, consoles, IoT — is proxied automatically at the network layer (TUN/TPROXY), with no client software and no per-app configuration. It embeds the mihomo kernel (fused into the single binary), bundles a web panel and geo/rules data, and adds a transactional deploy/upgrade flow with automatic rollback, so standing up and maintaining a gateway is a single command.

> **The name.** *panoxy* = Greek *πᾶν* (*pan*, "all") + *proxy*: one proxy for *all* of your traffic.

> **Program name.** The default program name is `panoxy`, and it can be customized to any name at build time (e.g. `myproxy`). Once renamed, the binary name, install path, config path, systemd unit, nft table, environment-variable prefix and man page **all follow** that name. See [Build · Custom program name](#custom-program-name).

---

## ✨ Features

- 🔧 **TUN / TPROXY dual mode** — TUN is stable out of the box (default); TPROXY preserves the client's real source IP with the best kernel forwarding performance
- 🛡️ **DNS hijack = nftables** — a dedicated `inet panoxy` table; port 53 redirect → the kernel's dual-stack DNS listener `[::]:1053` (no protocol blocked, DoT/DoQ/DoH route normally)
- 🔄 **Self-healing** — `kill -9`/OOM residue is cleaned automatically on `systemctl restart panoxy`; no manual intervention
- 📡 **Verifiable subscriptions** — prefetch → validate → incremental write → restart → **node count > 0 counts as success**; never a false success
- 🧩 **Config merge** — `merge-conf` does field-level merge of same-named groups (union of proxies/use); base groups are preserved, never deleted
- ⬆️ **Parameterized upgrade** — `--check`/`--ui-version` (web UI); dry-run validation, automatic rollback on failure
- 📖 **Complete documentation** — every command's `-h/-?/--help` includes examples; `man panoxy` is generated from the same source as `--help`
- 🔍 **Debug-friendly** — `--verbose` for step-by-step detail; `--debug` for zero-obfuscation of external command/API I/O

## 🚀 Quick start

### Option 1: Single-binary direct install (personal use, recommended)

```bash
# Copy the panoxy binary to the target machine, then:
sudo panoxy init '<your-subscription-url>'
```

Eight steps run automatically: precheck → fetch subscription → network probe → download geo/rules → download panel → place assets → deploy service → import subscription. Each step has a progress bar; when a direct connection fails, downloads are proxied through subscription nodes (a temporary in-process kernel is booted from the panoxy CLI itself); with no panoxy CLI present it suggests the offline package `deploy`.

### Option 2: Offline package (for friends)

Download the offline package from [Releases](../../releases) (~50 MB, geo + UI + rules; the kernel is embedded in the CLI):

```bash
tar xzf panoxy-V0.0.1-amd64.tar.gz && cd panoxy-V0.0.1-amd64
sudo ./panoxy deploy                 # fully automatic install
sudo panoxy sub import              # paste the subscription link (no quotes needed)
panoxy status                        # verify health
```

### Option 3: Pre-install (rootless trial)

```bash
panoxy try '<subscription-url>'       # sandboxed full-install test, never touches the real system
panoxy init --dry-run                # read-only rehearsal (environment / download strategy / config render)
```

### Already have your own config?

```bash
sudo panoxy merge-conf ~/my.yaml    # overlay-merge: same-named groups merge, base groups preserved
sudo panoxy merge-conf --dry-run ~/my.yaml   # preview the merge decision first
```

## 📐 Architecture

- **DNS (53/853)** — nft redirect → `:1053` → kernel
- **Data traffic (non-53)** — routing table → TUN device → kernel; TPROXY mode adds `nft mark 1` + policy routing + `tproxy → :7893` (preserves source IP)
- TUN and TPROXY share the same kernel — only the traffic path differs

- Data plane (node/group selection) lives in the **web panel**; transport plane (tun/tproxy) lives in the **CLI**
- the kernel's own outbound traffic is allowed via `routing-mark: 6666` → prevents DNS loop deadlock
- systemd unit has zero `resolvectl`; `fw apply` self-cleans → restart self-heals

### Traffic policy (blocks nothing)

The first goal of transparent proxying is **normal access**; routing is only an optimization of which path traffic takes:

| Protocol | Handling | Notes |
|---|---|---|
| Plain DNS (53) | **hijack** → kernel | provides domain-level routing (fake-ip) for most devices |
| QUIC/HTTP3 (UDP 443) | **normal routing** | native HTTP/3 experience; SNI is encrypted, so domain rules don't apply (IP routing) |
| DoT (TCP 853) | **normal routing** | encrypted DNS goes through the proxy (preserved for custom devices) |
| DoQ (UDP 853) | **normal routing** | same as above |
| DoH (TCP 443) | **normal routing** | same port as HTTPS, cannot and should not be blocked |

### Direct-connect base services (31 rules, no proxy)

| Category | Ports | Services |
|---|---|---|
| **Remote management** | 23 | Telnet |
| **Remote desktop** | 3389, 5900 | RDP, VNC |
| **VPN/overlay** | 41641, 3478, 51820, 1194, 500, 4500, 1701, 1723 | Tailscale, STUN/TURN, WireGuard, OpenVPN, IPSec (IKE/NAT-T), L2TP, PPTP |
| **VoIP** | 5060, 5061 | SIP, SIPS |
| **Domain auth** | 88, 389, 636, 1812, 1813 | Kerberos, LDAP, LDAPS, RADIUS |
| **Discovery/time** | 5353, 123, 161, 1900 | mDNS, NTP, SNMP, SSDP/UPnP |
| **IoT** | 1883, 8883, 5683 | MQTT, MQTT/TLS, CoAP |
| **Storage/database** | 3260, 3306, 5432, 6379, 27017, 873 | iSCSI, MySQL, PostgreSQL, Redis, MongoDB, Rsync |
| **Tailscale-specific** | 100.100.100.100, 100.64.0.0/10 | MagicDNS, CGNAT subnet |

> **Note:** SSH (port 22) is **not** in the direct-connect list — it now follows split-tunneling. Foreign SSH goes through the proxy, and GitHub SSH is routed via proxy to avoid pollution (see the commented `DST-PORT,22,DIRECT` rule in `src/internal/asset/config.tpl`).

## 📂 Repository layout

```
panoxy/
├── src/               Go source (cmd/internal/tests)
├── dist/              release artifacts (binary + offline package, gitignored)
├── build.sh           packaging/distribution script (offline package / subscription bootstrap / leak scan)
├── docs/              extended docs
│   ├── TPROXY.md      complete TPROXY-mode guide
│   ├── MIGRATION.md   bash-version migration steps
│   ├── KNOWN-LIMITATIONS.md
│   └── TROUBLESHOOTING.md
├── Makefile           local build/install entry (make)
└── README.md
```

## 🛠️ Build

### Prerequisites

- Go 1.23+ ([install](https://go.dev/dl/))
- No CGO dependency (fully static build)

### Using Makefile (recommended)

```bash
make                                    # build current arch → dist/ (amd64 auto-detects AVX2)
make build                              # same as above (explicit)
make build ARCH=arm64                   # cross-compile one arch (amd64|arm64|all; no ARM machine needed)
make install                            # install CLI → /usr/local/bin/panoxy (PREFIX/BINDIR customizable)
make build PANOXY_VERSION=V0.0.1        # set a version number
make build PROG=myproxy                 # customize program name (default panoxy, see "Custom program name")
make help                               # list all targets with descriptions
```

### Using the script

```bash
./build.sh                              # build current arch (default)
./build.sh --arch arm64                 # target arch
./build.sh --arch all                   # both arches
./build.sh --ver V0.0.1                 # set version
./build.sh install                      # build + smart install (see below)
```

#### `build.sh install` — build, then install the right way

Compiles the current arch first, then picks the install path automatically:

- **Installed machine** (`/etc/panoxy.yaml` + installed CLI present): runs the fresh binary's
  `redeploy` — stop service → swap the CLI in place (config & subscriptions kept) → restart →
  re-mount firewall → health check. This is the one-command dev iteration loop on a live gateway.
- **Fresh machine**: installs the CLI only (`--bindir`, default `/usr/local/bin`); deploy
  afterwards with `sudo panoxy init 'SUB_URL'`.

Unlike `make install`, which just copies the binary to `BINDIR` with no service orchestration,
`build.sh install` handles the full stop/replace/restart transaction on a running gateway.

### Manual build

```bash
cd src

# native arch (amd64)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v3 \
  go build -trimpath -ldflags "-s -w -X main.version=V0.0.1" \
  -o ../dist/panoxy-linux-amd64 \
  ./cmd/panoxy

# cross-compile ARM64 (no ARM machine needed)
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -ldflags "-s -w -X main.version=V0.0.1" \
  -o ../dist/panoxy-linux-arm64 \
  ./cmd/panoxy

# generate checksums
cd ../dist && sha256sum panoxy-linux-* > sha256sums.txt
```

<details>
<summary>📖 Build flags reference</summary>

| Flag | Effect |
|---|---|
| `CGO_ENABLED=0` | fully static build, no libc dependency, runs on any Linux |
| `-trimpath` | strip build-machine path info (security + size) |
| `-ldflags "-s -w"` | strip symbol table and debug info (size -30%) |
| `-X main.version=X` | inject the version number (shown by `panoxy --version`) |
| `-X github.com/deadship2003/panoxy/internal/constants.ProgName=X` | inject the program name (default `panoxy`; see [Custom program name](#custom-program-name)) |
| `GOAMD64` | amd64 build level: `build.sh` auto-detects AVX2 by default (present → v3, absent → v1); manually override with `GOAMD64=v3`/`v1` |

</details>

### Custom program name

The default program name `panoxy` is defined in `internal/constants.ProgName`; inject `-X` at build time to rename it — the binary and all runtime artifacts **inherit** the name:

```bash
# rename to myproxy: binary name, install path, config path, systemd unit,
# nft table, env prefix, man page all follow
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath \
  -ldflags "-s -w -X main.version=V0.0.1 -X github.com/deadship2003/panoxy/internal/constants.ProgName=myproxy" \
  -o ../dist/myproxy-linux-amd64 \
  ./cmd/panoxy
```

The easy way is to hand it to the build entry:

```bash
make PROG=myproxy                     # Makefile: PROG variable
./build.sh --prog myproxy             # build.sh: --prog flag
PROG=myproxy ./build.sh package       # or env var PROG (packaging works the same)
```

Runtime artifacts follow the renamed program (using `myproxy` as the example):

| Dimension | Default `panoxy` | Renamed `myproxy` |
|---|---|---|
| Binary / install path | `panoxy` → `/usr/local/bin/panoxy` | `myproxy` → `/usr/local/bin/myproxy` |
| Config / root dir | `/etc/panoxy.yaml` · `/opt/panoxy` | `/etc/myproxy.yaml` · `/opt/myproxy` |
| systemd unit | `panoxy.service` etc. | `myproxy.service` etc. |
| nft table | `inet panoxy` | `inet myproxy` |
| Env prefix | `PANOXY_` | `MYPROXY_` (program name uppercased, `-`→`_`) |
| man page | `panoxy.1.gz` · `man panoxy` | `myproxy.1.gz` · `man myproxy` |

> Note: the program name (a build-time variable) and the GitHub repo name (`deadship2003/panoxy`) are two different things — renaming only affects the binary and runtime artifacts, not the repo or the upgrade source.

> **CPU selection**: the panoxy CLI auto-detects `GOAMD64` against the current CPU at build time (amd64 with AVX2 → `v3`, without → `v1` full compatibility; force with `GOAMD64=v1 ./build.sh`). Since the mihomo kernel is fused into the CLI, this single build-time choice also covers the kernel — there is no separate core to probe or download at runtime.

### Verify the build

```bash
file dist/panoxy-linux-amd64
# ELF 64-bit LSB executable, x86-64, statically linked ✓

dist/panoxy-linux-amd64 --version
# panoxy version V0.0.1
```

## 📦 Packaging

### Using build.sh

```bash
./build.sh package                       # current arch (default)
./build.sh package all                    # all target platforms (amd64+arm64)
./build.sh package --arch arm64 --ver V0.0.1
./build.sh package -h                    # help
```

### Script flags / env vars

| Flag / var | Default | Meaning |
|---|---|---|
| `--arch amd64\|arm64\|all` | current platform | target arch |
| `--ver V0.0.1` | git describe | version number |
| `--prog panoxy` (or `PROG` env) | `panoxy` | program name (build-time injection; determines binary/package name and runtime paths) |
| `--sub-url URL` | (empty) | download assets through subscription proxy when offline |
| `ASSETS_SRC` | `/opt/panoxy` | local assets dir (copied if present, not downloaded) |
| `PROXY_PORT` | `33999` | subscription bootstrap proxy port |

### Packaging flow (internal steps)

```
[1/5] build ─── inline go build → dist/panoxy-linux-<arch> (both arches when `all`)
[2/5] assets ── local first (ASSETS_SRC) > direct (15s check) > subscription proxy > gh mirror
                 download: geo×3 + Country.mmdb + HyperADRules + metacubexd UI (kernel is embedded in the CLI)
[3/5] scan ──── subscription-leak detection (token= etc. → abort; URL never enters the package)
[4/5] assemble ─ panoxy-V<ver>-<arch>/{panoxy, README.md, assets/}
[5/5] package ── tar.gz + sha256 → dist/
```

### Manual packaging

<details>
<summary>📖 Expand the full manual packaging steps</summary>

```bash
cd ~/panoxy
mkdir -p dist

# ===== Step 1: build =====
cd src
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags "-s -w -X main.version=V0.0.1" \
  -o ../dist/panoxy-linux-amd64 ./cmd/panoxy
cd ..

# ===== Step 2: download assets =====
TMP=$(mktemp -d)

# geo trio (28MB)
geo="https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest"
curl -fsSL -o $TMP/GeoIP.dat    "$geo/geoip.dat"
curl -fsSL -o $TMP/GeoSite.dat  "$geo/geosite.dat"
curl -fsSL -o $TMP/Country.mmdb "$geo/country.mmdb"

# ad rules
curl -fsSL -o $TMP/HyperADRules-Ads.yaml \
  "https://github.com/Lynricsy/HyperADRules/releases/latest/download/hyper_adrules_ads_clash.yaml"

# metacubexd panel
curl -fsSL -o $TMP/ui.tgz \
  "https://github.com/MetaCubeX/metacubexd/releases/latest/download/compressed-dist.tgz"

# ===== Step 3: assemble the offline package =====
PKG="panoxy-V0.0.1-amd64"
rm -rf "$PKG"
mkdir -p "$PKG/assets/geo" "$PKG/assets/ui/official" "$PKG/assets/rule"

cp dist/panoxy-linux-amd64 "$PKG/panoxy"
chmod +x "$PKG/panoxy"
cp $TMP/Geo*.dat $TMP/Country.mmdb "$PKG/assets/geo/"
cp $TMP/HyperADRules-Ads.yaml "$PKG/assets/rule/"
tar xzf $TMP/ui.tgz -C "$PKG/assets/ui/official"
cp README.md "$PKG/"

# ===== Step 4: tar it up =====
tar -czf "dist/$PKG.tar.gz" "$PKG"
(cd dist && sha256sum "$PKG.tar.gz" > "$PKG.tar.gz.sha256")

# ===== Step 5: clean up =====
rm -rf "$PKG" $TMP
echo "artifact: dist/$PKG.tar.gz ($(du -h dist/$PKG.tar.gz | cut -f1))"
```

**Skip downloads when assets already exist locally:**

```bash
# copy the local assets directly (offline packaging; same local fallback as build.sh package)
cp /opt/panoxy/Geo*.dat /opt/panoxy/Country.mmdb "$PKG/assets/geo/"
cp /opt/panoxy/rule_provider/HyperADRules-Ads.yaml "$PKG/assets/rule/"
```

</details>

### Final package structure

```
panoxy-V0.0.1-amd64/
├── panoxy                                    ← Go binary (~46MB, mihomo kernel embedded)
├── README.md
└── assets/
    ├── geo/GeoIP.dat GeoSite.dat Country.mmdb
    ├── rule/HyperADRules-Ads.yaml            ← ad rules
    └── ui/official/                          ← metacubexd panel (161 files)
```

**~50 MB total** · the recipient runs `tar xzf` → `sudo ./panoxy deploy` and installation is done.

### CI auto-packaging

Pushing a `V*` tag triggers GitHub Actions, using the same script as local `build.sh package`:

```bash
git tag V0.0.1 && git push origin V0.0.1
# → CI auto-builds, packages and publishes a Release
```

**Subscription URLs never enter the package**: a pre-packaging scan aborts on patterns like `token=`.

## 📋 Command reference

Global flags (every command): `--root <dir>` custom install dir · `--verbose` step-by-step detail · `--debug` full transparency

| Command (parameters) | Effect |
|---|---|
| `panoxy try [URL] [--dir] [--name] [--file] [--proxy-mode] [--secret] [--mirror]` | rootless sandboxed trial of the full install flow |
| `sudo panoxy init [URL] [--name] [--file] [--proxy-mode] [--secret] [--mirror] [--dry-run]` | bare-metal init (downloads assets → deploy → import subscription) |
| `sudo panoxy deploy [URL] [--name] [--file] [--proxy-mode] [--secret] [--dry-run]` | deploy from the offline package |
| `sudo panoxy redeploy [--dry-run]` | refresh the CLI/systemd units in place (config & data preserved), re-mount firewall + restart |
| `sudo panoxy sub import [URL] [--name] [--file] [--group]` | import/replace a subscription |
| `sudo panoxy sub del --name N` | delete a subscription |
| `panoxy sub list [--json]` | per-subscription status / node count |
| `panoxy status [--detail] [-q] [--json]` | health overview (service/firewall/subscription/egress) |
| `sudo panoxy merge-conf <yaml> [--dry-run] [--dns keep\|mine] [--no-wire] [--rollback]` | overlay-merge a personal config onto the default template |
| `panoxy config [--mode tun\|tproxy] [--secret]` | print the default config template (rootless) |
| `panoxy check [yaml]` | validate a config with the embedded kernel (read-only) |
| `sudo panoxy apply-conf <yaml>` | apply a config (hot-reload first, then restart) |
| `sudo panoxy start` / `stop` / `restart` | start (enable on boot) / stop (disable + clear firewall) / restart (self-heal) |
| `sudo panoxy mode [tun\|tproxy]` | view/switch transparent-proxy mode (atomic switch) |
| `sudo panoxy upgrade [--ui] [--ui-version vX] [--check]` | upgrade the web UI (`--ui` forces a manual re-upgrade) |
| `sudo panoxy uninstall` | uninstall (data & config preserved) |
| `sudo panoxy fw <apply\|clean>` | firewall management (advanced; auto-invoked by units) |
| `panoxy units` / `log [n]` | print rendered unit text / view service logs |
| `panoxy man [command] [--raw]` | view manual (root page or subcommand page) |
| `panoxy upstream` | check mihomo Alpha upstream for newer commits (hint only) |

## 🧪 Testing

```bash
make test          # unit tests (YAML editor / firewall-rule text / template -t)
make e2e           # end-to-end tests (real binary + fake systemd, ~60s)
make test-all      # everything
make lint          # go vet
```

<details>
<summary>📖 Testing pyramid</summary>

| Layer | Tests what | In panoxy | Count | Speed |
|---|---|---|---|---|
| Unit | single function | YAML merge / firewall-rule generation / template rendering | ~15 | <1s |
| Integration | component interplay | config through the embedded kernel (`panoxy check`) | ~5 | 1-2s |
| E2E | full flow | deploy → sub import → status end-to-end | 3 | ~50s |

E2E uses the real compiled binary (embedded kernel) + a mock subscription server + fake systemd; business logic is not mocked, so it validates the actual user experience.

</details>

## 📖 More docs

| Doc | Contents |
|---|---|
| [docs/TPROXY.md](docs/TPROXY.md) | complete TPROXY-mode guide (precheck / switch / verify / network topology / troubleshooting) |
| [docs/MIGRATION.md](docs/MIGRATION.md) | migration steps from the bash version |
| [docs/KNOWN-LIMITATIONS.md](docs/KNOWN-LIMITATIONS.md) | known limitations (mihomo limits / DoH / CPU requirements, etc.) |
| [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) | troubleshooting guide |
| `panoxy man` | manual (after deploy: `man panoxy` / `man panoxy-<command>`) |

## 📄 License

panoxy is licensed under the **GNU General Public License v3.0 (GPL-3.0)** — it is a derivative work of the
embedded [mihomo](https://github.com/MetaCubeX/mihomo) kernel (GPL-3.0). Copyright © 2026 deadship2003.
See [LICENSE](LICENSE).

Bundled/downloaded third-party components (metacubexd, meta-rules-dat, etc.) each carry their own
open-source license, see [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES) and [LICENSES/](LICENSES/).

---

<div align="center">
<sub>Built with Go · Powered by <a href="https://github.com/MetaCubeX/mihomo">mihomo</a></sub>
</div>
