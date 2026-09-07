# TPROXY mode guide

### Detailed guide for TPROXY mode

**Prechecks (physical/real machines)**:
```bash
# Uses nftables, depending on the nf_tproxy_ipv4/ipv6 kernel modules (kernel 4.18+
# includes them by default):
sudo modprobe nf_tproxy_ipv4 nf_tproxy_ipv6
```
Standard Arch/Debian/Ubuntu kernels include them by default; WSL2's trimmed kernels do
not support it.

**Switching**:
```bash
sudo panoxy mode tproxy     # atomic switch: old rules unloaded -> config variant -> restart -> new rules -> health check
sudo panoxy mode tun        # switch back to TUN (TPROXY rules cleaned up automatically)
```

**Verify after switching**:
```bash
panoxy mode                              # "tproxy"
ip rule show | grep fwmark              # fwmark 0x1 lookup 100
ip route show table 100                 # local default dev lo
sudo nft list table inet panoxy | grep tproxy
```

**TUN vs TPROXY comparison**:

| Aspect | TUN (default) | TPROXY |
|---|---|---|
| Source IP | ❌ lost (shows as the gateway IP) | ✅ **client's real IP preserved** |
| Performance | gvisor userspace | kernel forwarding, **theoretically optimal** |
| Config complexity | low (auto-route) | medium (mark/policy routing) |
| Kernel requirement | TUN driver | nf_tproxy module (nftables) |
| Docker/containers | good compatibility | may get hijacked by mistake |
| WSL2/virtualization | ✅ | ❌ (trimmed kernels) |

**Transparent-gateway network topology**:
- Chain: `Internet <- WAN <- panoxy machine (LAN port 192.168.1.1) <- LAN devices`
- Outbound (device -> Internet): the panoxy machine does `nftables DNS hijack` +
  `TPROXY mark+tproxy`, handing traffic to the kernel on `:7893`
- Return path (DHCP): LAN devices get gateway = `192.168.1.1`, DNS = a public server
  (53 gets hijacked); the devices themselves need no configuration at all

**LAN device onboarding (pick one)**:
1. The router's DHCP hands out gateway = the panoxy machine's LAN IP, DNS = a public address
2. A single device gets its gateway pointed at the panoxy machine manually
3. The panoxy machine itself runs DHCP (dnsmasq example):
```bash
sudo tee /etc/dnsmasq.conf << 'EOF'
interface=eth0
dhcp-range=192.168.1.100,192.168.1.200,12h
dhcp-option=3,192.168.1.1
dhcp-option=6,223.5.5.5
EOF
sudo systemctl enable --now dnsmasq
```

**Troubleshooting**:
- Network down after switching: `sudo systemctl restart panoxy` (self-heals)
- Policy routing lost: confirm with `ip rule show | grep fwmark`; reload with `sudo panoxy fw apply`
- A device not proxied: check whether its gateway points at the panoxy machine
