package firewall

import (
	"fmt"

	"github.com/deadship2003/panoxy/internal/constants"
)

// Keep-out ranges: the address set the kernel layer passes through directly, never
// entering the kernel (equivalent to TUN route-exclude). The fake-ip ranges are
// deliberately NOT here (see fakeIpv4Range / fakeIpv6Range below) — those must enter
// the kernel to have their domains restored, so they must not be exempted.
// Single source of truth: shared by BuildNftScript / BuildNftTproxyScript.
const (
	keep4Elements = "0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, " +
		"172.16.0.0/12, 192.0.0.0/24, 192.0.2.0/24, 192.168.0.0/16, " +
		"198.51.100.0/24, 203.0.113.0/24, 224.0.0.0/4, 240.0.0.0/4"
	// fc00::/7 (ULA) is passed through here. The fake-ip6 range sits in the RFC 5180
	// benchmark block 2001:2::/48 (outside ULA), so fc00::/7 needs no narrowing; we only
	// must keep 2001:2::/48 out of keep6 (see fakeIpv6Range and the test assertions).
	keep6Elements = "::/128, ::1/128, 64:ff9b::/96, 100::/64, 2001:db8::/32, fc00::/7, fe80::/10, ff00::/8"

	// Port-level keep-out: key ports passed through directly at the kernel layer
	// (Telnet/VPN/NAT/mDNS/NTP). This is the kernel-level subset of the config.tpl rules
	// section's "basic direct services" — once these ports get hijacked the VPN dies, so
	// they must be exempted right here (the remaining basic-service ports go direct via
	// the mihomo rules section). Shared by both chains; changes must stay in sync.
	// Note: SSH (22) has been removed from the kernel-level exemptions — config.tpl now
	// keeps DST-PORT,22,DIRECT commented out (foreign SSH goes through the proxy; GitHub
	// SSH avoids pollution). If the kernel layer still exempted 22, SSH would never enter
	// the kernel under TPROXY and GitHub SSH would go direct into the wall, diverging
	// from TUN behavior (BuildNftScript never exempted 22). This kernel subset must stay
	// in sync with the config.tpl rules section.
	keepPortsTCP = "tcp dport { 23 }"
	keepPortsUDP = "udp dport { 41641, 3478, 51820, 1194, 5353, 123 }"
)

// fake-ip ranges: must enter the kernel to have their domains restored, so they are
// deliberately kept out of the keep whitelist. Single source of truth, coupled with the
// config template's fake-ip-range / fake-ip-range6 (CIDR form here; first-address form
// in the config).
const (
	fakeIpv4Range = "198.18.0.0/16"
	fakeIpv6Range = "2001:2::/48" // RFC 5180 benchmark block (not publicly routable); the IPv6 version of 198.18.0.0/15
)

// BuildNftScript generates the full nft script for TUN mode.
// Principle: no protocol is blocked (QUIC/DoT/DoQ/DoH all get normal routing); normal
// access takes priority over routing precision.
func BuildNftScript(dnsPort, markSelf int) string {
	return fmt.Sprintf(`table inet %s {
  set keep4 {
    type ipv4_addr
    flags interval
    elements = { %s }
  }
  set keep6 {
    type ipv6_addr
    flags interval
    elements = { %s }
  }
  chain dns_prerouting {
    type nat hook prerouting priority dstnat; policy accept;
    ip daddr 100.100.100.100 return
    iifname "tailscale0" return
    iifname != "lo" meta l4proto { tcp, udp } th dport 53 redirect to :%d
  }
  chain dns_output {
    type nat hook output priority dstnat; policy accept;
    ip daddr 100.100.100.100 return
    ip daddr @keep4 return
    ip6 daddr @keep6 return
    meta mark %d return
    meta l4proto { tcp, udp } th dport 53 redirect to :%d
  }
  chain dns_input {
    type filter hook input priority filter; policy accept;
    iifname "lo" th dport %d accept
    ip saddr @keep4 th dport %d accept
    ip6 saddr @keep6 th dport %d accept
  }
}
`, constants.NftTable, keep4Elements, keep6Elements,
		dnsPort, markSelf, dnsPort, dnsPort, dnsPort, dnsPort)
}

// BuildNftTproxyScript generates the TPROXY-mode script: adds the tproxy chain and the
// local-output marking chain on top of the TUN version.
//
// Local outbound traffic goes through the output hook (TPROXY cannot catch it), so the
// local_output chain marks "non-keep-out local tcp/udp traffic" with markTproxy; policy
// routing `ip rule fwmark 1 lookup 100` -> `local 0.0.0.0/0 dev lo` loops it back onto
// lo, and tproxy_prerouting then hands it to the kernel — equivalent to TUN (v6 and
// direct-IP included). Key point: local_output must be `type route` (forced re-route;
// marking in a plain output hook does not trigger re-routing).
//
// The `socket transparent 1` in tproxy_prerouting is the DIVERT optimization (the
// standard practice from the kernel's tproxy.txt): it matches follow-up packets of
// already-established transparent connections (IP_TRANSPARENT sockets) re-entering via
// loopback, marking and accepting them so the tproxy statement does not repeat a
// pointless socket lookup.
func BuildNftTproxyScript(dnsPort, markSelf, markTproxy, table, tproxyPort int) string {
	return fmt.Sprintf(`table inet %s {
  set keep4 {
    type ipv4_addr
    flags interval
    elements = { %s }
  }
  set keep6 {
    type ipv6_addr
    flags interval
    elements = { %s }
  }
  chain dns_prerouting {
    type nat hook prerouting priority dstnat; policy accept;
    ip daddr 100.100.100.100 return
    iifname "tailscale0" return
    iifname != "lo" meta l4proto { tcp, udp } th dport 53 redirect to :%d
  }
  chain dns_output {
    type nat hook output priority dstnat; policy accept;
    ip daddr 100.100.100.100 return
    ip daddr @keep4 return
    ip6 daddr @keep6 return
    meta mark %d return
    meta l4proto { tcp, udp } th dport 53 redirect to :%d
  }
  chain dns_input {
    type filter hook input priority filter; policy accept;
    iifname "lo" th dport %d accept
    ip saddr @keep4 th dport %d accept
    ip6 saddr @keep6 th dport %d accept
  }
  chain local_output {
    type route hook output priority mangle; policy accept;
    oifname "tailscale0" return
    %s return
    %s return
    ip daddr @keep4 return
    ip6 daddr @keep6 return
    meta mark != 0 return
    meta l4proto { tcp, udp } th dport 53 return
    meta l4proto { tcp, udp } meta mark set %d accept
  }
  chain tproxy_prerouting {
    type filter hook prerouting priority mangle; policy accept;
    iifname "tailscale0" return
    %s return
    %s return
    ip daddr @keep4 return
    ip6 daddr @keep6 return
    meta mark %d return
    meta l4proto { tcp, udp } th dport 53 return
    meta l4proto { tcp, udp } socket transparent 1 meta mark set %d accept
    meta l4proto { tcp, udp } tproxy to :%d meta mark set %d accept
  }
}
`, constants.NftTable, keep4Elements, keep6Elements,
		dnsPort, markSelf, dnsPort, dnsPort, dnsPort, dnsPort,
		keepPortsTCP, keepPortsUDP, markTproxy,
		keepPortsTCP, keepPortsUDP, markSelf, markTproxy, tproxyPort, markTproxy)
}
