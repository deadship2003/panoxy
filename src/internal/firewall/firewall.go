// Package firewall manages panoxy's own firewall rules (DNS hijack; TPROXY mode adds
// marking/policy routing on top).
//
// Design points:
//   - a dedicated table inet <prog> (= constants.NftTable, following the compile-time
//     ProgName injection), never reusing the system nat/filter tables ->
//     CleanAll = drop the whole table, idempotent
//   - startup unconditionally CleanAll's before Apply -> kill -9/OOM leftovers
//     self-heal on every systemctl restart
//   - local OUTPUT hijack exemptions: reserved ranges/loopback (protects LAN DNS) +
//     mark 6666 (the kernel's own upstream queries, preventing a DNS loop deadlock —
//     coupled with the config template's routing-mark)
//   - DNS hijack uses redirect (the kernel listens on [::]:1053 dual-stack): OUTPUT
//     lands on 127.0.0.1, PREROUTING lands on the ingress interface's primary address,
//     v4/v6 both covered, no route_localnet needed
//   - nftables is the only backend (every kernel 4.18+ has nf_tproxy support); no
//     iptables fallback is kept
package firewall

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/deadship2003/panoxy/internal/constants"
	"github.com/deadship2003/panoxy/internal/logx"
)

// BackendName is the sole firewall backend name (shown in health reports).
const BackendName = "nftables"

// ensureNft verifies the nftables userspace is available; panoxy supports nftables only,
// so a missing binary fails fast with an install hint.
func ensureNft() error {
	if _, err := exec.LookPath("nft"); err != nil {
		return fmt.Errorf("nftables not found: panoxy requires the nftables userspace (install the 'nftables' package)")
	}
	return nil
}

func runNft(script string) error {
	c := exec.Command("nft", "-f", "-")
	c.Stdin = strings.NewReader(script)
	out, err := c.CombinedOutput()
	logx.DebugCmd("nft", []string{"-f", "-"}, string(out), err)
	if err != nil {
		return fmt.Errorf("nft failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// CleanAll unconditionally drops the own table + policy routing (the first step of
// startup; a missing table counts as success — idempotent).
func CleanAll() error {
	if err := ensureNft(); err != nil {
		return err
	}
	args := []string{"delete", "table", constants.NftFamily, constants.NftTable}
	out, err := exec.Command("nft", args...).CombinedOutput()
	logx.DebugCmd("nft", args, string(out), err)
	if err != nil && !isNotExist(out, err) {
		return fmt.Errorf("failed to clean old rules: %s", strings.TrimSpace(string(out)))
	}
	if err := TproxyPolicyDel(); err != nil {
		return err
	}
	logx.Step("firewall: cleaned own table %s %s and policy routing (including kill -9 residue)", constants.NftFamily, constants.NftTable)
	return nil
}

// ApplyDnsHijack is TUN mode: DNS hijack only (CleanAll first, then load — idempotent).
func ApplyDnsHijack() error {
	if err := CleanAll(); err != nil {
		return err
	}
	if err := runNft(BuildNftScript(constants.DnsListenPort, constants.MarkSelf)); err != nil {
		return err
	}
	logx.Info("firewall: loaded DNS hijack")
	return nil
}

// ApplyTproxy is TPROXY mode: DNS + mark/policy-routing/tproxy chains (CleanAll first,
// then load — idempotent).
func ApplyTproxy() error {
	if err := CleanAll(); err != nil {
		return err
	}
	if err := runNft(BuildNftTproxyScript(constants.DnsListenPort, constants.MarkSelf,
		constants.MarkTproxy, constants.TproxyTable, constants.TproxyPort)); err != nil {
		return err
	}
	if err := TproxyPolicyAdd(); err != nil {
		return err
	}
	logx.Info("firewall: loaded full TPROXY rules")
	return nil
}

// HasStaleRules: the table existing at all counts as leftover rules present.
func HasStaleRules() (bool, error) {
	if err := ensureNft(); err != nil {
		return false, err
	}
	args := []string{"list", "table", constants.NftFamily, constants.NftTable}
	out, err := exec.Command("nft", args...).CombinedOutput()
	logx.DebugCmd("nft", args, string(out), err)
	if err != nil {
		if isNotExist(out, err) {
			return false, nil
		}
		return false, fmt.Errorf("nft list failed: %s", strings.TrimSpace(string(out)))
	}
	return true, nil
}

// CheckTproxySupport dry-runs a minimal tproxy rule via nft -c: validates both the
// userspace syntax and the kernel's nf_tproxy support in one shot. nftables TPROXY uses
// the inet family `tproxy to :port` statement (needs the nf_tproxy_ipv4/ipv6 modules).
func CheckTproxySupport() error {
	if err := ensureNft(); err != nil {
		return err
	}
	script := fmt.Sprintf(`table inet %s_tproxy_probe {
  chain tproxy_probe {
    type filter hook prerouting priority mangle; policy accept;
    meta l4proto { tcp, udp } tproxy to :%d
  }
}
`, constants.NftTable, constants.TproxyPort)
	c := exec.Command("nft", "-c", "-f", "-")
	c.Stdin = strings.NewReader(script)
	out, err := c.CombinedOutput()
	logx.DebugCmd("nft", []string{"-c", "-f", "-"}, string(out), err)
	if err != nil {
		return fmt.Errorf("nftables cannot express tproxy: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func isNotExist(out []byte, err error) bool {
	s := string(out)
	if err != nil {
		s += err.Error()
	}
	return strings.Contains(s, "No such file or directory") ||
		strings.Contains(s, "does not exist") ||
		strings.Contains(s, "No such device")
}
