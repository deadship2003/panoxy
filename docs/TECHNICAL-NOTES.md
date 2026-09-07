# Technical notes

Deep dives into two core mechanisms, aimed at troubleshooting and follow-up development —
not a changelog.

## 1. TPROXY mode: the local-traffic loop and failure recovery

### 1. Symptom

- TUN mode works; after `sudo panoxy mode tproxy`, applications **on the gateway machine
  itself** cannot reach the Internet.
- LAN clients usually keep working (they take a different path via PREROUTING), so this
  gets misdiagnosed as "one broken app" rather than a firewall-model problem.

### 2. Root cause

TPROXY takes effect only in the **prerouting** hook by default and cannot catch the
gateway's own **output**-direction traffic. Local DNS gets hijacked and receives a
fake-ip (`198.18.0.1/16`), but TPROXY mode has no TUN device to capture that range ->
the fake-ip follows the default route out of the gateway and gets blackholed -> the
whole local machine loses connectivity (domestic and foreign alike).

For TUN equivalence, the local traffic's **three loop elements** must be completed:

1. **Marking** — the `local_output` chain (`type route hook output priority mangle`; it
   must be `type route` for the fwmark re-route to trigger) marks all non-keep-out local
   tcp/udp with `mark 0x1`.
2. **Loop re-entry** — policy routing `ip rule fwmark 1 lookup 100` + `ip route add
   local 0.0.0.0/0 dev lo table 100` sends the marked local traffic back into the
   kernel, re-entering prerouting tproxy.
3. **Re-entry admission** — the `iifname "lo" return` in tproxy_prerouting is removed
   (it would swallow the re-entering packets); loopback target addresses are covered by
   the keep4/keep6 sets.

The **DIVERT** optimization is also kept: `socket transparent 1 meta mark set 1 accept`
placed before the tproxy statement handles follow-up packets of established transparent
connections re-entering via loopback (the standard practice from the kernel's
`tproxy.txt`).

### 3. Diagnosis

- Does the firewall ruleset contain the local loop: presence of the `local_output`
  chain, absence of `iifname "lo" return`, and policy routing `fwmark 1 lookup 100` in
  place.
- Is the deployed binary identical to the source (source has the fix but the binary was
  never rebuilt — the most common way to get bitten).

### 4. Recovery procedure

1. Confirm the source contains the loop fix; if the branch lags, merge the commits first.
2. Back up the old binary -> rebuild -> redeploy (a static binary can simply be copied
   over).
3. After `sudo panoxy mode tproxy`, verify: local outbound works + domestic direct +
   LAN-side regression.
4. Stay on tproxy, or switch back with `sudo panoxy mode tun`.

### 5. Key references

- `internal/firewall/rules.go` -> `BuildNftTproxyScript` (the `local_output` chain +
  `tproxy_prerouting`)
- `internal/firewall/policy.go` -> `tproxyPolicyCmds` (policy routing)
- `internal/constants/constants.go` -> `MarkTproxy=1`, `TproxyTable=100`,
  `TproxyPort=7893`, `MarkSelf=6666`
- `internal/asset/config.tpl` -> with `.TProxy` true it emits `tproxy-port: 7893` and
  omits the `tun` block

## 2. Dual-stack fake-ip design

### 1. Goal

Enable IPv6 fake-ip and change the DNS listen from v4-only to dual-stack, so IPv6 flows
through fake-ip routing split exactly like IPv4.

### 2. Why IPv6 did not work before

- In fake-ip mode without `fake-ip-range6` configured, AAAA queries return empty
  (clients fall back to v4).
- The old `dns.listen: 0.0.0.0:1053` bound v4 only; DNS queries over v6 transport never
  reached the kernel.

### 3. Key design

- `dns.listen: "[::]:1053` dual-stack: the field is a **single address** and does not
  support comma-separated lists; dual-stack comes from `[::]` + the kernel's
  `net.ipv6.bindv6only=0` (v4 arrives as v4-mapped).
- `fake-ip-range: 198.18.0.1/16` (v4), `fake-ip-range6: 2001:2::1/48` (v6, the RFC 5180
  benchmark block, not publicly routable).
- The `keep6` whitelist must **not** contain `2001:2::/48` (it would be passed through
  as direct); the chosen block lies outside ULA (`fc00::/7`), so the `fc00::/7` entry in
  `keep6` needs no narrowing.
- Address space \(2^{80}\) — the range needs no change for the foreseeable future.

### 4. Key references

- `internal/firewall/rules.go` -> the `fakeIpv4Range`/`fakeIpv6Range` constants (single
  source of truth; network form `198.18.0.0/16`, `2001:2::/48`)
- `internal/asset/config.tpl` -> `dns.listen`, `fake-ip-range`, `fake-ip-range6`
- `internal/config/merge.go` -> with `--dns mine` it force-writes `[::]:1053` back
  (preventing a merge from overriding it back to v4)
- `cmd/panoxy/misccmds.go` -> the `dns.listen` consistency warning in `warnCompat`
- `internal/constants` -> `DnsListenPort=1053`
