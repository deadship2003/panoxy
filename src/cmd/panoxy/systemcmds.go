package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/deadship2003/panoxy/internal/asset"
	"github.com/deadship2003/panoxy/internal/config"
	"github.com/deadship2003/panoxy/internal/constants"
	"github.com/deadship2003/panoxy/internal/firewall"
	"github.com/deadship2003/panoxy/internal/health"
	"github.com/deadship2003/panoxy/internal/logx"
	"github.com/deadship2003/panoxy/internal/paths"
	"github.com/deadship2003/panoxy/internal/statemode"
	"github.com/deadship2003/panoxy/internal/systemdunit"
)

// runInstall deploys only the service and system settings (files already in place; an internal deploy step).
func runInstall(cmd *cobra.Command, args []string) error {
	return withRootLock(func(p paths.Paths) error { return runInstallBody(p, cmd, args) })
}

func runInstallBody(p paths.Paths, cmd *cobra.Command, args []string) error {
	mode := statemode.Read(p.State)
	logx.Step("[1/4] precheck: config passes -t")
	if out, err := mihomoTest(p, p.Conf); err != nil {
		return fmt.Errorf("config validation failed (%s)", firstErrLine(out))
	}

	logx.Step("[2/4] write systemd units (mode %s) and enable ip_forward", mode)
	prevFwd := readIPForward()
	if err := systemdunit.Write(p, mode); err != nil {
		return err
	}
	writeSysctl(p)
	rollback := func() {
		systemdunit.StopSvc()
		systemdunit.Disable()
		systemdunit.Remove(p)
		os.Remove(p.Sysctl)
		setIPForward(prevFwd)
		logx.Warn("rolled back: unit/timer/sysctl removed, ip_forward restored to %s", prevFwd)
	}

	logx.Step("[3/4] bring up the service (ExecStartPost auto-loads the firewall)")
	if err := systemdunit.PortCheck(p.Conf); err != nil {
		rollback()
		return err
	}
	if err := systemdunit.Start(); err != nil {
		rollback()
		return fmt.Errorf("service failed to start, rolled back")
	}
	// Deployment ends enabled + running (deployment orchestration, not a lifecycle side
	// effect): registration happens after the service proves healthy.
	if err := systemdunit.Enable(); err != nil {
		rollback()
		return fmt.Errorf("auto-start registration failed, rolled back")
	}

	logx.Step("[4/4] health verification (service+API)")
	if err := health.WaitHealthy(p.Conf, 30*time.Second, ""); err != nil {
		rollback()
		return fmt.Errorf("health verification timed out, rolled back")
	}
	logx.Info("install complete %s (health verification passed)", version)
	return nil
}

// runDeploy is a fresh deployment (run inside an offline package): assets → config → CLI/man → service → optional subscription import.
func runDeploy(cmd *cobra.Command, args []string) error {
	if dry, _ := cmd.Flags().GetBool("dry-run"); dry {
		return deployDryRun(cmd, args)
	}
	return withRootLock(func(p paths.Paths) error { return runDeployBody(p, cmd, args) })
}

func runDeployBody(p paths.Paths, cmd *cobra.Command, args []string) error {
	pkgDir, err := os.Getwd() // deploy must be run from the root of the extracted offline package
	if err != nil {
		return err
	}
	assets := filepath.Join(pkgDir, "assets")
	if _, err := os.Stat(assets); err != nil {
		return fmt.Errorf("no offline assets in the current directory (%s) — deploy must run inside the extracted %s offline package", assets, constants.ProgName)
	}
	// bash legacy residue detection: abort with manual cleanup guidance (Q7 decision: no automatic migration).
	if legacy := systemdunit.DetectLegacy(p); legacy != "" {
		return fmt.Errorf(`bash legacy deployment residue detected: %s
clean it up manually first, then retry:
  sudo %s uninstall && sudo systemctl revert ...
  see the README "Migrating from the bash version" section`, legacy, constants.ProgName)
	}

	mode, _ := cmd.Flags().GetString("proxy-mode")
	if !validMode(mode) {
		return fmt.Errorf("--proxy-mode must be tun or tproxy")
	}
	secret, _ := cmd.Flags().GetString("secret")

	snap := snapshot(p)
	defer func() { /* each failure path rolls back explicitly */ _ = snap }()

	logx.Step("[1/5] place geo and ad rules (offline preloaded)")
	placeGeoAndRules(p, assets)
	logx.Step("[2/5] place web UI")
	placeUI(p, assets)

	logx.Step("[3/5] config: existing > in-package manual clash.yaml > template render")
	if _, err := os.Stat(p.Conf); err == nil {
		logx.Info("existing config detected, kept untouched: %s", p.Conf)
	} else if b, err := os.ReadFile(filepath.Join(pkgDir, constants.ProgName+".yaml")); err == nil {
		if err := os.WriteFile(p.Conf, b, 0o644); err != nil {
			return err
		}
		logx.Info("using the in-package manual config: %s.yaml", constants.ProgName)
	} else {
		d := asset.DefaultConfigData()
		d.TProxy = mode == "tproxy"
		d.Secret = secret
		out, err := asset.RenderConfig(d)
		if err != nil {
			return err
		}
		if err := os.WriteFile(p.Conf, []byte(out), 0o644); err != nil {
			return err
		}
		logx.Info("wrote base config (mode %s); web UI secret: %s (view: grep '^secret' %s)", mode, d.Secret, p.Conf)
	}

	// Always write the clean default-template copy (config.default.yaml, merge-conf's baseline)
	// to the data dir, regardless of which config source was chosen above.
	if err := writeDefaultConf(p, mode, secret); err != nil {
		return err
	}

	logx.Step("[4/5] place CLI and man pages; write proxy-mode=%s to the state file", mode)
	self, err := os.Executable()
	if err != nil {
		return err
	}
	self, _ = filepath.EvalSymlinks(self)
	copyBinary(self, p.Cli)
	installMan(p.ManGz, self)
	os.MkdirAll(filepath.Dir(p.State), 0o755)
	statemode.Write(p.State, statemode.State{ProxyMode: mode})

	logx.Step("[5/5] service deployment (incl. firewall; full rollback on failure)")
	if err := runInstall(cmd, args); err != nil {
		deployRollback(p, snap)
		return err
	}

	// Subscription import (aligned with init): imported when a subscription URL is given or --name/--file is explicit; plain deploy imports no subscription.
	if len(args) > 0 || cmd.Flags().Changed("name") || cmd.Flags().Changed("file") {
		return runSubImport(cmd, args)
	}
	logx.Info("deploy complete. tip: sudo %s sub import to set the subscription (press enter for paste mode); %s status to check health", constants.ProgName, constants.ProgName)
	return nil
}

// runUninstall removes the service registration; keeps /opt data and config.
func runUninstall(cmd *cobra.Command, args []string) error {
	return withRootLock(svcUninstall)
}

// runModeSwitch is an atomic switch: unload old firewall → config variant → -t → restart → new firewall → verify.
func modeSwitch(want string) error {
	if !validMode(want) {
		return fmt.Errorf("mode must be tun or tproxy")
	}
	return withRootLock(func(p paths.Paths) error { return modeSwitchBody(p, want) })
}

func modeSwitchBody(p paths.Paths, want string) error {
	old := statemode.Read(p.State)
	// Rollback: restore config + state file + restart (only for the restart/health failure paths after the state was already written).
	rollbackTo := func() {
		config.Restore(p.Conf)
		statemode.Write(p.State, statemode.State{ProxyMode: old})
		systemdunit.Restart()
	}
	if old == want {
		logx.Info("already in %s mode", want)
		return nil
	}
	if want == "tproxy" && os.Getenv(constants.EnvPrefix()+"_SKIP_TPROXY_PROBE") == "" {
		if err := firewall.CheckTproxySupport(); err != nil {
			return fmt.Errorf("TPROXY precondition not met: %v", err)
		}
	} // PANIXY_SKIP_TPROXY_PROBE=1 is for the test sandbox only
	logx.Step("switch %s → %s: unload old firewall", old, want)
	if err := firewall.CleanAll(); err != nil {
		logx.Warn("firewall teardown before switch failed: %v", err)
	}
	logx.Step("render config variant and validate")
	if err := config.Backup(p.Conf); err != nil {
		return err
	}
	e, err := config.Load(p.Conf)
	if err != nil {
		return err
	}
	e.SetMode(want == "tproxy", constants.TproxyPort)
	if err := e.Save(); err != nil {
		config.Restore(p.Conf)
		return err
	}
	if out, err := mihomoTest(p, p.Conf); err != nil {
		msg := firstErrLine(out)
		config.Restore(p.Conf)
		return fmt.Errorf("config validation failed (%s), restored", msg)
	}
	statemode.Write(p.State, statemode.State{ProxyMode: want})
	if err := systemdunit.Restart(); err != nil {
		rollbackTo()
		return fmt.Errorf("restart failed, rolled back to %s", old)
	}
	if err := health.WaitHealthy(p.Conf, 30*time.Second, ""); err != nil {
		rollbackTo()
		return fmt.Errorf("health check timed out after switch, rolled back to %s: %w", old, err)
	}
	config.ClearBackup(p.Conf)
	logx.Info("switched to %s mode (data-plane selection is still done in the web UI)", want)
	return nil
}

// ---- deploy/install helpers ----

type deploySnap struct {
	rootNew, confNew, cliNew, manNew, stateNew bool
	prevFwd                                    string
}

func snapshot(p paths.Paths) deploySnap {
	return deploySnap{
		rootNew:  !exists(p.Root),
		confNew:  !exists(p.Conf),
		cliNew:   !exists(p.Cli),
		manNew:   !exists(p.ManGz),
		stateNew: !exists(p.State),
		prevFwd:  readIPForward(),
	}
}

func deployRollback(p paths.Paths, s deploySnap) {
	systemdunit.StopSvc()
	systemdunit.Disable()
	systemdunit.Remove(p)
	os.Remove(p.Sysctl)
	setIPForward(s.prevFwd)
	if s.confNew {
		os.Remove(p.Conf)
	}
	if s.cliNew {
		os.Remove(p.Cli)
	}
	if s.manNew {
		os.Remove(p.ManGz)
	}
	if s.stateNew {
		os.Remove(p.State)
	}
	if s.rootNew {
		os.RemoveAll(p.Root)
		logx.Warn("rollback: newly created data dir removed")
	} else {
		logx.Warn("rollback: %s already existed, files added this run are left in place", p.Root)
	}
	logx.Warn("rollback complete, system restored to its original state")
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func readIPForward() string {
	b, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err != nil {
		return "0"
	}
	return strings.TrimSpace(string(b))
}

func writeSysctl(p paths.Paths) {
	os.MkdirAll(filepath.Dir(p.Sysctl), 0o755)
	os.WriteFile(p.Sysctl, []byte("net.ipv4.ip_forward = 1\n"), 0o644)
	setIPForward("1")
}

func setIPForward(v string) {
	runCmd("sysctl", "-w", "net.ipv4.ip_forward="+v)
}

// geoFiles lists the GeoIP/GeoSite/Country data filenames needed for deployment (init download and deploy placement share the same source).
var geoFiles = []string{"GeoIP.dat", "GeoSite.dat", "Country.mmdb"}

func placeGeoAndRules(p paths.Paths, assets string) {
	for _, f := range geoFiles {
		src := filepath.Join(assets, "geo", f)
		dst := filepath.Join(p.Root, f)
		if !exists(dst) && exists(src) {
			copyFile(src, dst)
		}
	}
	os.MkdirAll(p.RuleProv, 0o755)
	src := filepath.Join(assets, "rule", "HyperADRules-Ads.yaml")
	dst := filepath.Join(p.RuleProv, "HyperADRules-Ads.yaml")
	if !exists(dst) {
		if exists(src) {
			copyFile(src, dst)
		} else {
			logx.Warn("package has no ad rules file; first start will fetch it from the network via the kernel")
		}
	}
}

func placeUI(p paths.Paths, assets string) {
	if exists(p.UiDir) {
		return
	}
	src := filepath.Join(assets, "ui", "official")
	if exists(src) {
		copyDir(src, p.UiDir)
		os.WriteFile(p.UiStamp, []byte("unknown\n"), 0o644)
	}
}

func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i > 0 {
		return s[:i]
	}
	return strings.TrimSpace(s)
}

// deployDryRun is a dry-run: verify offline package assets, config source decision, placement list, and unit preview.
func deployDryRun(cmd *cobra.Command, args []string) error {
	p := paths.Get()
	logx.Info("== deploy --dry-run (dry-run mode, does not execute) ==")
	pkgDir, err := os.Getwd()
	if err != nil {
		return err
	}
	assets := filepath.Join(pkgDir, "assets")
	logx.Step("[precheck] offline package assets (%s)", assets)
	if _, err := os.Stat(assets); err != nil {
		return fmt.Errorf("no offline assets in the current directory — deploy must run inside the extracted offline package (for a bare-metal direct install use %s init)", constants.ProgName)
	}
	for _, item := range []struct{ name, path string }{
		{"GeoIP.dat", filepath.Join(assets, "geo", "GeoIP.dat")},
		{"GeoSite.dat", filepath.Join(assets, "geo", "GeoSite.dat")},
		{"Country.mmdb", filepath.Join(assets, "geo", "Country.mmdb")},
		{"ad rules", filepath.Join(assets, "rule", "HyperADRules-Ads.yaml")},
		{"web UI", filepath.Join(assets, "ui", "official", "index.html")},
	} {
		if exists(item.path) {
			logx.Info("  ✓ %s", item.name)
		} else {
			logx.Warn("  ✗ %s missing", item.name)
		}
	}
	if legacy := systemdunit.DetectLegacy(p); legacy != "" {
		logx.Warn("[precheck] ⚠️ bash legacy residue: %s (a real install would be aborted)", legacy)
	}
	logx.Step("[decision] config source")
	switch {
	case exists(p.Conf):
		logx.Info("  existing %s present → inherit as-is", p.Conf)
	case exists(filepath.Join(pkgDir, constants.ProgName+".yaml")):
		logx.Info("  in-package manual %s.yaml → use it", constants.ProgName)
	default:
		logx.Info("  render the default template (secret %s)", drySecret(cmd))
	}
	logx.Step("[plan] placement: %s (geo/rules/UI → service → optional subscription import)", p.Root)
	logx.Info("== dry-run done. Real run: sudo ./%s deploy ...", constants.ProgName)
	return nil
}
