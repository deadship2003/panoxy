# Traffic policy

## Core principle

> **No protocol is blocked; the first goal of transparent proxying is normal access —
> routing split is only an optimization of which path traffic takes.**

## DNS handling

| Protocol | Port | Handling | Effect |
|---|---|---|---|
| Plain DNS | UDP/TCP 53 | **hijacked** -> kernel :1053 | fake-ip mode; precise domain-level routing |
| DoT | TCP 853 | normal routing | encrypted DNS goes through the proxy (preserved for custom devices) |
| DoQ | UDP 853 | normal routing | same as above |
| DoH | TCP 443 | normal routing | same port as HTTPS; cannot and should not be blocked |
| QUIC/HTTP3 | UDP 443 | normal routing | native HTTP/3 experience; SNI is encrypted, so domain rules do not apply |

## Tailscale exclusions

| Item | Value | Exclusion mechanism |
|---|---|---|
| Direct UDP | 41641 | template DST-PORT + firewall tproxy chains |
| STUN/TURN | 3478 | same as above |
| MagicDNS | 100.100.100.100:53 | firewall DNS chain + template IP-CIDR |
| CGNAT subnet | 100.64.0.0/10 | the keep4 set + template IP-CIDR |
| Interface | tailscale0 | firewall DNS chain + tproxy chains iifname exemption |

## Basic direct services (31 rules)

### Remote management
| Port | Service | Reason |
|---|---|---|
| 23 | Telnet | latency-sensitive; proxying adds delay |

> **Note:** SSH (22) is **not** in the direct list — it now follows split routing
> (foreign SSH goes through the proxy; GitHub SSH avoids pollution); see the commented
> `DST-PORT,22,DIRECT` in `src/internal/asset/config.tpl`.

### Remote desktop
| Port | Service | Reason |
|---|---|---|
| 3389 | RDP | latency-sensitive (stuttering display) |
| 5900 | VNC | same as above |

### VPN / overlay
| Port | Service | Reason |
|---|---|---|
| 41641 | Tailscale | UDP hole-punching; P2P fails through a proxy |
| 3478 | STUN/TURN | NAT traversal |
| 51820 | WireGuard | UDP encapsulation; severe latency through a proxy |
| 1194 | OpenVPN | same as above |
| 500 | IPSec IKE | UDP negotiation; tunnel setup fails through a proxy |
| 4500 | IPSec NAT-T | IPSec after NAT traversal |
| 1701 | L2TP | UDP encapsulation; severe latency through a proxy |
| 1723 | PPTP | TCP/GRE; handshake fails through a proxy |

### VoIP
| Port | Service | Reason |
|---|---|---|
| 5060 | SIP | voice signaling; latency = poor call quality |
| 5061 | SIPS | SIP over TLS |

### Domain auth / directory
| Port | Service | Reason |
|---|---|---|
| 88 | Kerberos | AD domain logon; may fail authentication through a proxy |
| 389 | LDAP | directory queries |
| 636 | LDAPS | LDAP over TLS |
| 1812 | RADIUS | network authentication (WiFi etc.) |
| 1813 | RADIUS accounting | same as above |

### Discovery / time / management
| Port | Service | Reason |
|---|---|---|
| 5353 | mDNS/DNS-SD | LAN device discovery; cannot discover through a proxy |
| 123 | NTP | time sync |
| 161 | SNMP | network monitoring |
| 1900 | SSDP/UPnP | smart-device discovery |

### IoT / smart home
| Port | Service | Reason |
|---|---|---|
| 1883 | MQTT | IoT communication (Home Assistant etc.) |
| 8883 | MQTT/TLS | same as above |
| 5683 | CoAP | lightweight IoT protocol |

### Storage / databases
| Port | Service | Reason |
|---|---|---|
| 3260 | iSCSI | IP storage; extremely latency-sensitive |
| 3306 | MySQL | primary-replica sync |
| 5432 | PostgreSQL | same as above |
| 6379 | Redis | cache sync |
| 27017 | MongoDB | replica sets |
| 873 | Rsync | file sync |

## Implementation locations

| Layer | File | Effective scope |
|---|---|---|
| Kernel rule engine | `src/internal/asset/config.tpl` rules section | TUN + TPROXY |
| nftables DNS hijack | `src/internal/firewall/rules.go` dns_prerouting/dns_output | TUN + TPROXY |
| nftables tproxy chains | `src/internal/firewall/rules.go` tproxy_prerouting | TPROXY only |
