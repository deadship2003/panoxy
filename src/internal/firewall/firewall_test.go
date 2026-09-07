package firewall

import (
	"strings"
	"testing"

	"github.com/deadship2003/panoxy/internal/constants"
)

// Golden assertions: the rule text's key lines must exist — these lines are the entire
// skeleton of the DNS hijack / anti-loop / 853 handling.
func TestBuildNftScriptGolden(t *testing.T) {
	s := BuildNftScript(1053, 6666)
	for _, want := range []string{
		"table inet " + constants.NftTable + " {",
		"elements = {",
		"iifname != \"lo\" meta l4proto { tcp, udp } th dport 53 redirect to :1053",
		"ip daddr @keep4 return",
		"ip6 daddr @keep6 return",
		"meta mark 6666 return", // exempt the kernel itself (prevents the DNS loop)
		"th dport 53 redirect to :1053",
		"ip daddr 100.100.100.100 return",          // Tailscale MagicDNS never hijacked
		"type nat hook prerouting priority dstnat", // PREROUTING: LAN clients
		"type nat hook output priority dstnat",     // OUTPUT: local machine
	} {
		if !strings.Contains(s, want) {
			t.Errorf("nft script missing key rule: %q", want)
		}
	}
	// Single source of truth: the keep sets are injected from the constants and must be
	// fully present — and must never contain the fake-ip ranges.
	if !strings.Contains(s, keep4Elements) {
		t.Errorf("keep4 missing reserved ranges: %s", keep4Elements)
	}
	if !strings.Contains(s, keep6Elements) {
		t.Errorf("keep6 missing reserved ranges: %s", keep6Elements)
	}
	if strings.Contains(keep4Elements, fakeIpv4Range) {
		t.Errorf("keep4 must not contain the fake-ip range %s (domains could never be restored in the kernel)", fakeIpv4Range)
	}
	if strings.Contains(keep6Elements, fakeIpv6Range) {
		t.Errorf("keep6 must not contain the fake-ip6 range %s (domains could never be restored in the kernel)", fakeIpv6Range)
	}
	// no protocol blocked (the DoT/DoQ blocks were removed; both get normal routing)
	if strings.Contains(s, "853 reject") {
		t.Errorf("853 must not be blocked (DoT/DoQ get normal routing)")
	}
	if strings.Contains(s, "127.0.0.1:1053") || strings.Contains(s, "dnat to 127.0.0.1") {
		t.Errorf("must not DNAT to 127.0.0.1 (unreachable in the PREROUTING scenario; use redirect)")
	}
}

func TestBuildNftTproxyScriptGolden(t *testing.T) {
	s := BuildNftTproxyScript(1053, 6666, 1, 100, 7893)
	for _, want := range []string{
		"chain tproxy_prerouting {",
		"type filter hook prerouting priority mangle",
		"meta mark 6666 return",
		"th dport 53 return", // DNS goes to the nat chain, never into tproxy
		"meta l4proto { tcp, udp } tproxy to :7893 meta mark set 1 accept",
		// DIVERT optimization (standard per the kernel's tproxy.txt): follow-up packets of
		// established transparent connections re-entering via loopback get marked+accepted directly
		"meta l4proto { tcp, udp } socket transparent 1 meta mark set 1 accept",
		// the local-output marking chain (the key to TUN equivalence):
		"chain local_output {",
		"type route hook output priority mangle", // must be type route to trigger the fwmark re-route
		"meta mark != 0 return",                  // the kernel itself (6666) and already-marked (1) are never touched again
		"meta l4proto { tcp, udp } meta mark set 1 accept",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("tproxy script missing key rule: %q", want)
		}
	}
	// Loop-re-entering local traffic must be able to reach tproxy, so there must be no
	// `iifname "lo" return` anymore (the keep4/keep6 sets already cover it).
	if strings.Contains(s, `iifname "lo" return`) {
		t.Errorf("tproxy_prerouting must not have iifname lo return (it would swallow loop-re-entering local traffic)")
	}
	// Regression: SSH (22) must not be kernel-level exempted — config.tpl keeps
	// DST-PORT,22,DIRECT commented out (foreign SSH goes through the proxy). If the
	// kernel level still exempted 22, SSH would never enter the kernel under TPROXY and
	// GitHub SSH would go direct into the wall, diverging from TUN behavior.
	if strings.Contains(keepPortsTCP, "22") {
		t.Errorf("keepPortsTCP must not contain 22 (SSH): it should be routed in the kernel, in sync with config.tpl's commented DST-PORT,22,DIRECT")
	}
	if strings.Contains(s, "dport { 22") {
		t.Errorf("the TPROXY script must not exempt port 22 at the kernel level (SSH should be routed in the kernel)")
	}
}

func TestTproxyPolicyCmds(t *testing.T) {
	add := tproxyPolicyCmds(true, 1, 100)
	joined := strings.Join(flatten(add), " ")
	for _, want := range []string{"rule add fwmark 1 lookup 100", "route add local 0.0.0.0/0 dev lo table 100", "route add local ::/0"} {
		if !strings.Contains(joined, want) {
			t.Errorf("policy routing missing: %q", want)
		}
	}
	del := tproxyPolicyCmds(false, 1, 100)
	if j := strings.Join(flatten(del), " "); !strings.Contains(j, "rule del fwmark 1 lookup 100") {
		t.Errorf("cleanup missing rule del")
	}
}

func flatten(cmds [][]string) []string {
	var out []string
	for _, c := range cmds {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

// TestConstantsInvariants guards against accidents: unexpectedly changing the mark/port
// constants would break the coupling with the config template.
func TestConstantsInvariants(t *testing.T) {
	if constants.MarkSelf != 6666 {
		t.Errorf("MarkSelf must stay coupled with the config template's routing-mark (6666)")
	}
	if constants.DnsListenPort != 1053 {
		t.Errorf("DnsListenPort must stay coupled with the config template's dns.listen (1053)")
	}
}

// TestTolerantError is the regression for a real first-install lesson: `ip rule del` on a
// nonexistent rule reports RTNETLINK ENOENT ("No such file or directory"); without the
// tolerance, fw apply fails and systemd declares the service dead.
func TestTolerantError(t *testing.T) {
	for _, s := range []string{
		"RTNETLINK answers: No such file or directory",
		"RTNETLINK answers: File exists",
		"Error: ipv4: FIB rule does not exist",
	} {
		if !tolerantError(s) {
			t.Errorf("should tolerate: %q", s)
		}
	}
	for _, s := range []string{
		"Operation not permitted",
		"memory allocation failure",
	} {
		if tolerantError(s) {
			t.Errorf("must not tolerate: %q", s)
		}
	}
}
