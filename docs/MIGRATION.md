## Migrating from the bash version and upgrading

panoxy is a Go rewrite of the bash version; there are two flows: **fresh migration**
(bash -> panoxy, one time) and **in-place upgrade** (panoxy -> a newer version, config and
subscriptions kept). Migration does no automatic conversion: when bash leftovers are
detected it aborts with guidance, and the user cleans up by hand.

### 1. Fresh migration from the bash version

Bash-version leftover signatures: a systemd unit containing `resolvectl`, or the old
config `/etc/clash.yaml` containing a `tun.dns-hijack` section.

1. Stop the service and clear the units: `sudo panoxy uninstall` (stops the service,
   cleans the firewall, removes the units/sysctl/man pages; keeps the `/opt/panoxy` data
   and the `/etc/panoxy.yaml` config)
2. Delete or empty the old config `/etc/clash.yaml`; to keep your groups, manually remove
   the `tun.dns-hijack` section
3. Deploy: from an offline package `sudo ./panoxy deploy` (or on a networked bare machine
   `sudo panoxy init`)
4. Import the subscription: `sudo panoxy sub import`

> Guard rail: when deploy/init detects bash leftovers (a unit with resolvectl / a config
> with dns-hijack) it aborts with guidance; clean them up and retry (`panoxy deploy
> --dry-run` prechecks without changes).

### 2. In-place upgrade (keep config and subscriptions)

The kernel is embedded in the CLI, so most upgrades only swap the binary:

- Method A (recommended; also refreshes the units/man/sysctl/default-config baseline):
  `sudo panoxy stop` -> run **the freshly built binary** with `sudo ./bin/panoxy redeploy`
  (redeploy copies the currently running binary to `/usr/local/bin/panoxy`)
- Method B (binary swap only): `cp bin/panoxy /usr/local/bin/panoxy` -> `sudo panoxy restart`
