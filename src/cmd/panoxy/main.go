// panoxy — a mihomo-based Linux transparent proxy gateway deploy/management tool (single-binary, Go).
// Responsibility boundary: template rendering, file/unit management, firewall DNS hijack, subscription
// management, upgrade, and health checks; traffic forwarding and DNS resolution are all done by
// the mihomo kernel.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
	"github.com/spf13/pflag"

	"github.com/deadship2003/panoxy/internal/constants"
	"github.com/deadship2003/panoxy/internal/firewall"
	"github.com/deadship2003/panoxy/internal/logx"
	"github.com/deadship2003/panoxy/internal/paths"
	"github.com/deadship2003/panoxy/internal/statemode"
)

// version is injected by the build script via -ldflags -X; defaults to the constant.
var version = constants.Version

func main() {
	// cobra does not ship -?: normalize it at the entry point.
	for i, a := range os.Args {
		if a == "-?" {
			os.Args[i] = "--help"
		}
	}
	if err := NewRootCmd().Execute(); err != nil {
		logx.Error("%v", err)
		// LIF-001 unified exit codes: an error may carry a specific code (2 privileges,
		// 3 service operation failed, 4 unsupported scope); everything else is 1.
		code := 1
		var ec exitCoder
		if errors.As(err, &ec) {
			code = ec.ExitCode()
		}
		cleanExit(code)
	}
	cleanExit(0)
}

// cleanExit ensures the terminal cleanly returns to a prompt after any command:
// progress-bar \r residue and background-process stdin holding both cause the shell not to show a prompt.
func cleanExit(code int) {
	os.Stdout.Sync()
	os.Stderr.Sync()
	os.Exit(code)
}

func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "panixy",
		Short: "mihomo-based transparent proxy gateway deploy/management tool",
		Long: `panixy — Linux transparent proxy gateway deploy/management tool built on mihomo (TUN/TPROXY)

The data plane (node/policy-group selection) lives in the Web UI; the transport plane
(tun/tproxy mode, firewall) lives in this CLI.

Getting started:
  panixy init --dry-run                  # dry-run (no root needed)
  panixy try 'SUBSCRIPTION_URL'          # sandbox full install test (no root needed)
  sudo panixy init 'SUBSCRIPTION_URL'    # initialize and deploy directly
  sudo ./panixy deploy 'SUBSCRIPTION_URL' # deploy from an offline package

Subscription / config:
  sudo panixy sub import 'SUBSCRIPTION_URL' # import a subscription (paste mode, no quoting)
  sudo panixy merge-conf ~/my.yaml       # merge personal config (--dry-run to preview)
  panixy config                           # print default config template (no root)

Daily use:
  panixy status                          # health overview (service/firewall/subscription/egress)
  sudo panixy start                      # start the service now (transient; boot state untouched)
  sudo panixy stop                       # stop the service + clear firewall (boot state untouched)
  sudo panixy restart                    # restart the service (self-heals firewall)
  sudo panixy service enable             # register auto-start on boot (start/stop never change it)
  sudo panixy mode tproxy                # switch to TPROXY (nftables tproxy; needs kernel support)
  sudo panixy upgrade --check            # show what can be upgraded

Operations:
  sudo panixy redeploy                   # refresh the CLI in place (keep config/data)
  sudo panixy uninstall                  # uninstall (keep data and config)

Commands (all accept --root/--verbose/--debug):
  init [URL]       --name --file --proxy-mode --secret --mirror --dry-run    bare-metal network install + sub import
  deploy [URL]     --name --file --proxy-mode --secret --dry-run                        offline-package install
  redeploy         --dry-run                                                            refresh CLI/units in place (config kept)
  try [URL]        (init flags) --dir                                                    rootless sandboxed full-install trial
  sub              import [URL] --name --file --group | del --name | list --json        subscription management
  status           --detail -q --json                                                   health overview (service/firewall/sub/egress)
  start | stop | restart                                                              service lifecycle (transient; enable/disable via service)
  service <install|uninstall|start|stop|restart|enable|disable|status> [--system|--user] [--json]
                                                                                      LIF-001 lifecycle set (strict transient/registration separation; fixed exit codes)
  mode [tun|tproxy]                                                                     view/switch transparent-proxy mode (atomic)
  upgrade          --ui --ui-version vX --check                                         web-UI upgrade (--ui = manual re-upgrade)
  merge-conf <yaml> --dry-run --dns keep|mine --no-wire --rollback                      overlay-merge a personal config
  config           --mode tun|tproxy --secret                                           print the default config template
  check [yaml] | apply-conf <yaml>                                                    validate / apply a config
  uninstall | units | log [n] | man [cmd] --raw | upstream | fw <apply|clean>           ops, info & advanced

Details: panixy man, or man panixy-<command> (after deployment)`,
		Version: version,
	}
	root.PersistentFlags().String("root", "", "install directory (default /opt/panixy; the data home can be relocated wholesale; /etc/clash.yaml stays the system-level config)")
	root.PersistentFlags().Bool("verbose", false, "step-by-step detail: each transaction step, files written, rules applied")
	root.PersistentFlags().Bool("debug", false, "full passthrough: echo external commands verbatim, mihomo API request/response, config diff")
	root.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		if r, _ := cmd.Flags().GetString("root"); r != "" {
			if !filepath.IsAbs(r) {
				logx.Error("--root requires an absolute path: %s", r)
				os.Exit(1)
			}
			os.Setenv(constants.EnvPrefix()+"_ROOT", r) // all paths.Get() take effect immediately; service units also inject this
		}
		if d, _ := cmd.Flags().GetBool("debug"); d {
			logx.SetLevel(logx.LevelDebug)
		} else if v, _ := cmd.Flags().GetBool("verbose"); v {
			logx.SetLevel(logx.LevelVerbose)
		}
	}
	root.AddCommand(
		cmdInit(), cmdDeploy(), cmdRedeploy(), cmdSub(),
		cmdTry(), cmdMergeConf(), cmdStatus(), cmdStart(), cmdStop(), cmdRestart(), cmdRun(),
		cmdService(), cmdMode(), cmdUpgrade(),
		cmdUninstall(), cmdUnits(), cmdLog(), cmdCheck(), cmdApplyConf(), cmdConfig(),
		cmdFw(), cmdMan(), cmdUpstream(),
	)
	rebrand(root) // replace hard-coded "panixy"/"/etc/clash.yaml" with the compile-time injected ProgName/DefConfPath
	return root
}

// rebrand replaces hard-coded "panixy" and "/etc/clash.yaml" in the command tree with the compile-time
// injected ProgName / DefConfPath, so --help/man examples and flag descriptions match the renamed program.
func rebrand(cmd *cobra.Command) {
	rep := func(s string) string {
		s = strings.ReplaceAll(s, "panixy", constants.ProgName)
		s = strings.ReplaceAll(s, "/etc/clash.yaml", constants.DefConfPath)
		return s
	}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		c.Use = rep(c.Use)
		c.Short = rep(c.Short)
		c.Long = rep(c.Long)
		c.Example = rep(c.Example)
		c.Flags().VisitAll(func(f *pflag.Flag) { f.Usage = rep(f.Usage) })
		c.PersistentFlags().VisitAll(func(f *pflag.Flag) { f.Usage = rep(f.Usage) })
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(cmd)
}

// upperFirst capitalizes the first letter (the program name is an ASCII binary/filename, safe).
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// manHeader generates the man page header: title/manual name/source all derive from the program name.
func manHeader() *doc.GenManHeader {
	return &doc.GenManHeader{
		Title:   strings.ToUpper(constants.ProgName),
		Section: "1",
		Manual:  upperFirst(constants.ProgName) + " Manual",
		Source:  constants.ProgName + " " + version,
	}
}

func cmdTry() *cobra.Command {
	c := &cobra.Command{
		Use:   "try [SUBSCRIPTION_URL]",
		Short: "pre-install: run the full install flow in a sandbox without root (pass = safe to install)",
		Long: `Pre-install (test install, no root): run the whole init flow for real inside a sandbox —
real asset download, real kernel startup, real subscription import with node-count>0
verification, real health checks. Everything lands in the sandbox dir; it does not touch the
real system (no /opt or /etc writes, no service install, no firewall changes).

Only two differences from a real deploy (both due to non-root limits, absent with sudo):
  - the tun section is stripped when booting the kernel (TUN device needs CAP_NET_ADMIN)
  - routing-mark is stripped (SO_MARK needs privileges); firewall rules are not applied
After it passes, deploy for real with: sudo panixy init 'SUBSCRIPTION_URL'`,
		Example: `  panixy try 'https://example.com/sub?token=x'   # full-flow test
  panixy try --dir ~/panixy-sandbox             # sandbox dir (default: temp dir)
  panixy try                                    # paste a subscription on Enter`,
		RunE: runTry,
	}
	addSubSourceFlags(c)
	addDeployFlags(c)
	addDownloadFlags(c)
	c.Flags().String("dir", "", "sandbox directory (default /tmp/panixy-try-<timestamp>)")
	return c
}

func cmdMergeConf() *cobra.Command {
	c := &cobra.Command{
		Use:   "merge-conf <personal-config.yaml>",
		Short: "merge personal config: same-name groups merge, new ones append, base kept; backup + rollback",
		Long: `Merge a personal clash.yaml (any filename) onto the default template (config.default.yaml) —
same-name groups are merged rather than replaced.

Base:      /opt/panixy/config.default.yaml (clean template from init/deploy, with SUB_URL_PLACEHOLDER)
Groups:    same name -> field-level merge (proxies/use union, scalars overridden by personal)
           new personal group -> appended at the end
           base groups (region/app groups) -> kept (references stay valid)
Rules:     personal rules prepended (matched first) + base rules as fallback (MATCH last, deduped)
Subs:      personal subscriptions appended; placeholder sub (SUB_URL_PLACEHOLDER) auto-retires
Takeover (personal): ports/secret/external-controller/proxies
Keep (base): tun mode section/routing-mark/dns.listen(secret)/geo/ntp/sniffer
Auto:      PROCESS- rules -> find-process-mode=strict; placeholder sub retires

Backup & rollback:
  auto-backup before merge -> /etc/clash.yaml.panixy-premerge
  any step failing restores automatically; after success use --rollback to revert manually`,
		Example: `  panixy merge-conf --dry-run ~/my-clash.yaml    # dry-run (no write, no backup)
  sudo panixy merge-conf ~/my-clash.yaml         # merge and apply
  sudo panixy merge-conf --rollback              # revert to pre-merge
  sudo panixy merge-conf --dns mine ~/my-clash.yaml`,
		RunE: runMergeConf,
	}
	addDryRunFlag(c, "dry-run: print the decision report and merged result preview only; no write, no backup")
	c.Flags().String("dns", "keep", "DNS section policy: keep (base) | mine (personal, listen forced to 1053)")
	c.Flags().Bool("no-wire", false, "do not auto-wire base subscriptions into groups")
	c.Flags().Bool("rollback", false, "restore from the .panixy-premerge backup")
	return c
}

func cmdInit() *cobra.Command {
	c := &cobra.Command{
		Use:   "init [SUBSCRIPTION_URL]",
		Short: "single-binary init (no package): download assets + deploy + import sub, with progress; --dry-run",
		Long: `Single-binary init without a package or offline assets — deploy directly on any bare machine.

Three-tier download strategy (each step shows a progress bar; --verbose for steps, --debug for full detail):
  direct (hard-fail after 15s) > subscription-bootstrap proxy (start a local proxy via a
  subscription node; needs the local panixy CLI) > gh mirror (--mirror, third-party source; for
  friends prefer the offline package deploy)

Eight steps: pre-check -> fetch subscription -> network probe -> geo/rules -> UI -> place assets
+ render config -> deploy service (firewall/health) -> import subscription (node count > 0).`,
		Example: `  sudo panixy init 'https://example.com/sub?token=x&sid=y'
  sudo panixy init --name Nano                            # paste a subscription on Enter
  sudo panixy init --file sub.yaml URL                    # import subscription offline
  sudo panixy init --mirror https://ghfast.top/ URL       # when direct is unreachable
  panixy init --dry-run                                   # dry-run (no root needed)`,
		RunE: runInit,
	}
	addSubSourceFlags(c)
	addDeployFlags(c)
	addDownloadFlags(c)
	addDryRunFlag(c, "dry-run: preview environment/download strategy/placement/config render; no execution, no root")
	return c
}

func cmdDeploy() *cobra.Command {
	c := &cobra.Command{
		Use:   "deploy [SUBSCRIPTION_URL]",
		Short: "fresh deploy (run inside an unpacked offline package; --dry-run)",
		Long: `Fresh deploy; must be run from the root of an unpacked offline package.

Flow: place geo/UI/ad-block rules -> render config (existing > hand-edited in package >
template) -> install CLI and man pages -> write systemd units -> enable ip_forward -> start
the service (with firewall). Any step failing rolls back everything. If legacy bash-deploy
leftovers are detected (units with resolvectl / config with dns-hijack), it aborts and prints
manual cleanup guidance.`,
		Example: `  sudo ./panixy deploy 'https://example.com/sub?token=x&sid=y'   # deploy and import subscription
  sudo ./panixy deploy --name Nano                              # deploy; paste sub on Enter
  sudo ./panixy deploy --proxy-mode tproxy                      # deploy in TPROXY mode`,
		RunE: runDeploy,
	}
	addSubSourceFlags(c)
	addDeployFlags(c)
	addDryRunFlag(c, "dry-run: preview environment/assets/download strategy/config render; no execution, no root")
	return c
}

func cmdSub() *cobra.Command {
	c := &cobra.Command{
		Use:   "sub",
		Short: "subscription management: import/delete/list proxy-providers",
		Long: `Manage mihomo subscriptions (proxy-providers): import or replace, delete, view status and node count.

Subscription import uses incremental yaml editing to write proxy-providers[NAME] (reusing anchor
<<: *p), and pre-populates cache, restarts the kernel, and verifies node count > 0; any step
failing rolls back automatically.`,
		Example: `  sudo panixy sub import 'https://example.com/sub?token=x'   # import (paste mode, no quoting)
  sudo panixy sub import --name airport2 'https://example.com/sub2'
  sudo panixy sub del --name airport2
  panixy sub list`,
	}
	c.AddCommand(cmdSubImport(), cmdSubDel(), cmdSubList())
	return c
}

func cmdSubImport() *cobra.Command {
	c := &cobra.Command{
		Use:   "import [SUBSCRIPTION_URL]",
		Short: "import/replace subscription: prefetch -> pre-cache -> restart -> verify node count > 0",
		Long: `Import or replace a subscription. With no argument it enters paste mode (reads a whole line;
URLs with & ? etc. need no quoting).

Flow: prefetch (local file > direct > via local proxy) -> validate as Clash YAML with nodes ->
incremental yaml edit into proxy-providers[NAME] (reuse anchor <<: *p, keep other providers
and comments, and merge NAME into each group's use list) -> validate (embedded kernel) -> pre-populate
provider cache -> restart (hot-reload does not re-fetch provider content) -> query
that provider's node count, and roll back automatically if it is 0.

Prerequisite: the config has an &p anchor (the base template ships it).`,
		Example: "  sudo panixy sub import --name airport2 'https://example.com/sub2'\n  sudo panixy sub import   # paste mode",
		RunE:    runSubImport,
	}
	addSubSourceFlags(c)
	c.Flags().StringSlice("group", nil, "limit merged groups (default: all groups with non-empty use / anchor holders)")
	return c
}

func cmdSubDel() *cobra.Command {
	c := &cobra.Command{
		Use:   "del --name NAME",
		Short: "delete a subscription provider (backup, validate, restart; roll back on failure)",
		Long: `Delete a subscription from proxy-providers and remove it from every group's use list.

Transaction flow: backup config -> delete provider + unwire -> validate (embedded kernel) -> restart ->
health check; any step failing rolls back automatically. Note: deleting the only subscription
leaves a group without use, which -t rejects (import a new subscription first).

The provider name is the --name from sub import (default SUB); list existing names with sub list.`,
		Example: "  sudo panixy sub del --name airport2",
		RunE:    runSubDel,
	}
	c.Flags().String("name", "", "provider name to delete (required)")
	_ = c.MarkFlagRequired("name")
	return c
}

func cmdSubList() *cobra.Command {
	c := &cobra.Command{
		Use:   "list",
		Short: "list all subscriptions: status/node count/errors (one bad sub doesn't hide the rest)",
		Long: `Read every proxy-provider from the config and query each one via the mihomo API.

Status: ✅ ok / ⚠️ fetch failed / ⚠️ parse failed / ⚠️ zero nodes. --json emits machine-readable output.`,
		Example: "  panixy sub list            # table\n  panixy sub list --json     # machine-readable",
		RunE:    runSubList,
	}
	c.Flags().Bool("json", false, "output as JSON")
	return c
}

func cmdStatus() *cobra.Command {
	c := &cobra.Command{
		Use:   "status [-q|--json|--detail]",
		Short: "health overview: service/firewall/per-sub node counts/core & UI versions/egress",
		Long: `Health overview. Includes: service status, firewall backend and leftover rules, every
proxy-provider status, core/UI versions, last upgrade time, and proxy egress connectivity;
it also notes that browser DoH cannot be intercepted by the kernel.

  --detail  append details: current proxy mode (tun/tproxy), TUN stack risk hints, route/cache details
  -q        quiet, exit code only: 0 healthy 1 degraded (zero nodes or proxy egress down) 2 fault (service/API unavailable)
  --json    machine-readable single line`,
		Example: `  panixy status              # overview
  panixy status --detail      # append details
  panixy status -q            # exit code only (for monitoring scripts)
  panixy status --json        # machine-readable`,
		RunE: runStatus,
	}
	c.Flags().Bool("detail", false, "append details")
	c.Flags().BoolP("quiet", "q", false, "quiet, exit code only")
	c.Flags().Bool("json", false, "output as JSON")
	return c
}

func cmdMode() *cobra.Command {
	return &cobra.Command{
		Use:   "mode [tun|tproxy]",
		Short: "view or switch transparent proxy mode (atomic switch: firewall + config + restart)",
		Long: `View or switch tun/tproxy mode.

Switching is an atomic transaction: teardown old firewall rules -> render the matching config
variant -> -t validate -> restart service -> load new firewall -> health check; any step failing
rolls back everything. TPROXY requires kernel TPROXY support (the nftables tproxy statement,
i.e. the nf_tproxy_ipv4/ipv6 modules); it refuses to switch when unavailable.

Traffic policy (unified for both modes):
  no protocol is blocked (QUIC/DoT/DoQ/DoH all get normal routing)
  DNS 53 hijacked (domain-level routing for most devices)
  32 direct-connect base services: SSH(22) RDP(3389) VNC(5900)
    VPN(Tailscale/WG/OpenVPN/IPSec/L2TP/PPTP) VoIP(SIP) domain-auth(Kerberos/LDAP)
    IoT(MQTT/CoAP) storage(iSCSI/MySQL/PG/Redis/Mongo) etc.

TUN (default) vs TPROXY:
  TUN:    simple and stable, auto-route handles routing; recommended for home
          source IP is lost (everything shows as the gateway IP)
          good WSL2/virtualization/Docker compatibility
  TPROXY: keeps the real client IP (per-device visible in logs)
          kernel forwards directly, better performance on weak CPUs
          needs kernel TPROXY support; Docker containers may be mis-captured
          IPv6 policy routing needs extra care

TPROXY pre-checks (nftables):
  sudo modprobe nf_tproxy_ipv4 nf_tproxy_ipv6          # nftables tproxy modules

Verify after switching:
  ip rule show | grep fwmark          # should have fwmark 0x1 lookup 100
  ip route show table 100             # should have local default dev lo
  sudo nft list table inet panixy | grep tproxy

Transparent gateway network setup (LAN devices):
  have the router DHCP hand out gateway = panixy machine's LAN IP and a public DNS
  (53 will be hijacked); or point a single device's gateway at the panixy machine manually.

Note: mode cannot be switched from the Web UI — firewall rules and config must change in one
transaction; the UI only handles the data plane (nodes/groups). With no argument it shows the
current mode.`,
		Example: `  panixy mode              # view current mode
  sudo panixy mode tproxy  # switch to TPROXY (nftables tproxy; needs kernel support)
  sudo panixy mode tun     # switch back to TUN (default)`,
		RunE: func(cmd *cobra.Command, args []string) error { return runMode(cmd, args) },
	}
}

func cmdUpgrade() *cobra.Command {
	c := &cobra.Command{
		Use:   "upgrade [--ui] [--ui-version vX] [--check]",
		Short: "upgrade the web UI (auto-invoked daily by the timer)",
		Long: `Upgrade the metacubexd web UI. Only when the upgrade succeeds is .last-upgrade updated.

The mihomo kernel is fused into the CLI, so there is no separate kernel to upgrade here; a new
CLI version is shipped by compiling a new binary and running sudo panixy redeploy (or simply
copying the freshly built binary over the CLI path).

  upgrade (bare)             upgrade the UI to the latest (the daily-timer default)
  upgrade --ui               manually (re)upgrade the UI, even if already at the latest
  upgrade --ui-version vX    pin a UI version
  upgrade --check            show current/latest version and the action to take (no change)`,
		Example: "  panixy upgrade --check             # show current/latest UI version\n  sudo panixy upgrade                 # upgrade the UI (daily-timer default)\n  sudo panixy upgrade --ui             # manual UI upgrade (re-applies even if latest)\n  sudo panixy upgrade --ui-version vX  # pin a UI version",
		RunE:    runUpgrade,
	}
	c.Flags().Bool("ui", false, "manually (re)upgrade the web UI now, even if already at the latest")
	c.Flags().String("ui-version", "", "pin a UI version")
	c.Flags().Bool("check", false, "dry-run: show current/latest UI version and the action to take")
	return c
}

func cmdUninstall() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "stop the service, clean firewall and systemd units (keep /opt data and config)",
		Long: `Stop and remove the panixy service and the scheduled upgrade task; clean up its own firewall
rules, sysctl, and man pages.

Kept: the /opt/panixy data directory (geo/UI/subscription cache) and the /etc/clash.yaml
config, plus the CLI binary itself — re-running init/deploy afterwards reuses the data.`,
		Example: "  sudo panixy uninstall",
		RunE:    runUninstall,
	}
}

func cmdUnits() *cobra.Command {
	return &cobra.Command{
		Use:   "units",
		Short: "print the rendered systemd unit text (offline review, no system changes)",
		Long: `Print the full unit text of panixy.service / panixy-upgrade.service / panixy-upgrade.timer,
rendered for the current install directory (--root). Read-only, writes no files, for pre-install
review or diffing.`,
		Example: "  panixy units > units.txt    # export for review",
		RunE:    runUnits,
	}
}

func cmdLog() *cobra.Command {
	return &cobra.Command{
		Use:   "log [lines]",
		Short: "view panixy service logs (journalctl)",
		Long: `Pass through journalctl to view the recent logs of panixy.service and panixy-upgrade.service.
No argument shows the last 80 lines; a numeric argument sets the line count.`,
		Example: "  panixy log        # last 80 lines\n  panixy log 200    # last 200 lines",
		RunE:    runLog,
	}
}

func cmdCheck() *cobra.Command {
	return &cobra.Command{
		Use:   "check [yaml]",
		Short: "validate config syntax with the embedded kernel in-process (default: current config; read-only, no root)",
		Long: `Validate the config with the embedded kernel in-process, passing through the kernel's first error. Read-only,
changes no files, no root needed.

With no argument it validates the current /etc/clash.yaml; with a path it validates that file
(e.g. before apply-conf).`,
		Example: "  panixy check                 # validate current config\n  panixy check ~/my-clash.yaml  # validate a specific file",
		RunE:    runCheck,
	}
}

func cmdApplyConf() *cobra.Command {
	return &cobra.Command{
		Use:   "apply-conf <yaml>",
		Short: "apply a custom config (prefer hot-reload; note it does not re-fetch proxy-providers)",
		Long: `After validation, apply the given YAML to /etc/clash.yaml: prefer hot-reload (only effective for
non-provider changes); if that doesn't take effect, fall back to restart, then restore the original
on failure. Auto-backup before applying; success clears the backup.

Note: kernel hot-reload rebuilds providers but does not re-fetch their content, so subscription
changes (add/remove/URL) need a restart to take effect.`,
		Example: "  sudo panixy apply-conf ~/my-clash.yaml",
		RunE:    runApplyConf,
	}
}

func cmdConfig() *cobra.Command {
	c := &cobra.Command{
		Use:   "config",
		Short: "render/print the default config template (read-only, no root)",
		Long: `Render the embedded default template (config.tpl) and print to stdout — same source as the
/etc/clash.yaml written by init/deploy on first install; keeps SUB_URL_PLACEHOLDER, no subscription.

Default secret/ports: secret=deadship, mixed-port=33833, HTTP 9966, SOCKS 6699, API 9999.
--mode tun|tproxy selects the tun/tproxy variant; --secret overrides the UI secret.

Read-only, no deploy, no firewall/service changes; no root needed. The clean default copy
(config.default.yaml, merge-conf's baseline) is maintained by init/deploy/redeploy.`,
		Example: `  panixy config               # print default config (stdout)
  panixy config > clash.yaml  # export to a file
  panixy config --mode tproxy # TPROXY variant`,
		RunE: runConfig,
	}
	c.Flags().String("mode", "tun", "transparent proxy mode: tun | tproxy")
	c.Flags().String("secret", constants.DefSecret, "UI/API secret")
	return c
}

func cmdFw() *cobra.Command {
	c := &cobra.Command{
		Use:   "fw <apply|clean>",
		Short: "firewall management (advanced; invoked automatically by service units)",
		Long: `Firewall DNS-hijack management (auto-invoked by systemd units; normally no need to run manually):

  apply    unconditionally clean own tables, then load the current mode's full rules (idempotent; kill -9 leftovers self-heal on service restart)
  clean    remove all own tables/chains/policy routes (invoked on service stop)`,
		Args:      cobra.ExactValidArgs(1),
		ValidArgs: []string{"apply", "clean"},
		Example:   "  sudo panixy fw apply   # idempotently remount current-mode rules\n  sudo panixy fw clean   # remove all own rules",
		RunE: func(cmd *cobra.Command, args []string) error {
			// In tproxy mode, apply must load the full rules (reads the state file; defaults to tun).
			mode := statemode.Read(paths.Get().State)
			switch args[0] {
			case "apply":
				if mode == "tproxy" {
					return firewall.ApplyTproxy()
				}
				return firewall.ApplyDnsHijack()
			case "clean":
				return firewall.CleanAll()
			}
			return nil
		},
	}
	return c
}

func cmdMan() *cobra.Command {
	c := &cobra.Command{
		Use:   "man [command] [--raw]",
		Short: "show the manual (root page or a subcommand page)",
		Long: `Show a manual page in the terminal. No argument shows the root page; a command name shows that
subcommand's page (e.g. man init, man sub). Prefers rendering via the system man; falls back to
plain text when no man is available.

After deployment the system man works too: man panixy / man panixy-<command>. --raw emits raw
roff for install-time man generation.`,
		Example: "  panixy man          # root page\n  panixy man init     # init command page\n  panixy man sub       # sub command page",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := os.MkdirTemp("", constants.ProgName+"-man-")
			if err != nil {
				return err
			}
			defer os.RemoveAll(dir)
			hdr := manHeader()
			if err := genAllMan(cmd.Root(), hdr, dir); err != nil {
				return fmt.Errorf("generate manual failed: %w", err)
			}
			page := constants.ProgName + ".1"
			if len(args) > 0 { // subcommand page: <prog> man init → <prog>-init.1
				page = constants.ProgName + "-" + args[0] + ".1"
			}
			b, err := os.ReadFile(dir + "/" + page)
			if err != nil {
				pages, _ := filepath.Glob(dir + "/" + constants.ProgName + "*.1")
				names := []string{}
				for _, f := range pages {
					n := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), constants.ProgName), ".1")
					if n == "" {
						names = append(names, "(root)")
					} else {
						names = append(names, strings.TrimPrefix(n, "-"))
					}
				}
				return fmt.Errorf("no manual page for %q; available: %v", args[0], names)
			}
			// --raw: output raw roff (installMan uses it to generate system man pages).
			if raw, _ := cmd.Flags().GetBool("raw"); raw {
				os.Stdout.Write(b)
				return nil
			}
			// Prefer rendering via the system man; with no man/groff, degrade to readable plain text (dropping roff control lines).
			if _, err := exec.LookPath("man"); err == nil {
				c := exec.Command("man", "-l", dir+"/"+page)
				c.Stdout, c.Stderr = os.Stdout, os.Stderr
				if c.Run() == nil {
					return nil
				}
			}
			fmt.Print(roffToText(b))
			return nil
		},
	}
	c.Flags().Bool("raw", false, "output raw roff (for install-time man generation)")
	return c
}

// roffToText is a minimal roff degradation: drop control lines and restore escapes, so it stays readable without man.
func roffToText(b []byte) string {
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, ".") || strings.HasPrefix(t, "'") {
			continue
		}
		t = strings.ReplaceAll(t, `\-`, "-")
		t = strings.ReplaceAll(t, `\&`, "")
		out = append(out, t)
	}
	return strings.Join(out, "\n") + "\n"
}

// genAllMan generates the root page + all subcommand pages (cobra doc.GenManTree only renders the passed command itself,
// not its subcommand pages — so it must recurse; parent commands like sub also carry import/del/list underneath).
func genAllMan(root *cobra.Command, hdr *doc.GenManHeader, dir string) error {
	var walk func(c *cobra.Command) error
	walk = func(c *cobra.Command) error {
		if err := doc.GenManTree(c, hdr, dir); err != nil {
			return err
		}
		for _, sub := range c.Commands() {
			if sub.Name() == "help" || sub.Name() == "completion" {
				continue
			}
			if err := walk(sub); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(root)
}
