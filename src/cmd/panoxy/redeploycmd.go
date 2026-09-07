package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/deadship2003/panoxy/internal/constants"
	"github.com/deadship2003/panoxy/internal/firewall"
	"github.com/deadship2003/panoxy/internal/health"
	"github.com/deadship2003/panoxy/internal/logx"
	"github.com/deadship2003/panoxy/internal/mihomoapi"
	"github.com/deadship2003/panoxy/internal/paths"
	"github.com/deadship2003/panoxy/internal/statemode"
	"github.com/deadship2003/panoxy/internal/systemdunit"
)

// applyFW explicitly loads firewall rules for the current mode (shared with fw apply).
// redeploy uses it to explicitly re-mount the firewall — a newly compiled panoxy may have
// adjusted rules, so it can't rely solely on the service restart's ExecStartPost fallback.
func applyFW(mode string) error {
	if mode == "tproxy" {
		return firewall.ApplyTproxy()
	}
	return firewall.ApplyDnsHijack()
}

func cmdRedeploy() *cobra.Command {
	c := &cobra.Command{
		Use:   "redeploy",
		Short: "in-place refresh of the CLI/systemd units (config and data kept), re-mount firewall and restart",
		Long: `On an installed machine, refresh the CLI binary, man pages, systemd units, sysctl and the
default-config baseline in place, then restart the service and re-mount the firewall. Config
and subscription data are always kept untouched.

Since the mihomo kernel is fused into the CLI, the only thing that changes between versions is
the binary itself: geo/rules are static data (auto-fetched by the kernel when missing) and the
web UI has its own upgrade command + daily timer. So redeploy no longer needs an offline
package — it copies the currently running binary (the one you just built) to the CLI path.

Flow: precheck (installed?) → copy binary → refresh man/units/sysctl/default-config → validate
→ restart → explicitly re-mount the firewall → health verification.`,
		Example: `  sudo panoxy redeploy              # refresh the installed CLI in place (keep config/data)
  sudo panoxy redeploy --dry-run    # dry-run: report install state and the files to refresh`,
		RunE: runRedeploy,
	}
	addDryRunFlag(c, "dry-run mode: report install state and the files to refresh, do not execute")
	return c
}

func runRedeploy(cmd *cobra.Command, args []string) error {
	if dry, _ := cmd.Flags().GetBool("dry-run"); dry {
		return redeployDryRun(cmd)
	}
	return withRootLock(func(p paths.Paths) error { return runRedeployBody(p, cmd, args) })
}

func runRedeployBody(p paths.Paths, cmd *cobra.Command, args []string) error {
	// Precheck: must already be installed (fresh install uses init/deploy).
	if !exists(p.Conf) || !exists(p.Cli) {
		return fmt.Errorf("no installed %s detected (config/CLI missing) — for a fresh install use sudo %s init/deploy", constants.ProgName, constants.ProgName)
	}
	mode := statemode.Read(p.State)
	secret := mihomoapi.NewFromConf(p.Conf).Secret
	logx.Info("redeploy started: in-place CLI/unit refresh (mode %s, %s and subscriptions kept untouched)", mode, p.Conf)

	// [1] Stop the running instance and explicitly clear the firewall (the new binary may
	// have changed FW rules, can't rely only on restart's ExecStartPost). Boot registration
	// is untouched — redeploy preserves the prior enablement state.
	logx.Step("[1/4] stop service and clear firewall rules")
	if err := systemdunit.StopSvc(); err != nil {
		return err
	}
	if err := firewall.CleanAll(); err != nil {
		logx.Warn("firewall cleanup failed: %v (fw apply will cover it after restart)", err)
	}

	// [2] Refresh the CLI binary, man pages, systemd units, sysctl and the default-config baseline.
	logx.Step("[2/4] refresh CLI/man/systemd units/sysctl/default-config")
	self, err := os.Executable()
	if err != nil {
		return err
	}
	self, _ = filepath.EvalSymlinks(self)
	copyBinary(self, p.Cli)
	installMan(p.ManGz, self)
	if err := systemdunit.Write(p, mode); err != nil {
		return err
	}
	writeSysctl(p)
	if err := writeDefaultConf(p, mode, secret); err != nil {
		logx.Warn("refreshing the default config copy failed: %v", err)
	}

	// [3] Validate + bring up the service.
	logx.Step("[3/4] validate config and restart service")
	if out, err := mihomoTest(p, p.Conf); err != nil {
		return fmt.Errorf("config validation failed (%s)", firstErrLine(out))
	}
	if err := systemdunit.PortCheck(p.Conf); err != nil {
		return err
	}
	if err := systemdunit.Start(); err != nil {
		return fmt.Errorf("service failed to start")
	}
	// Re-ensure registration + timer (idempotent): redeploy ends enabled + running, the
	// same outcome a fresh deploy reaches.
	if err := systemdunit.Enable(); err != nil {
		return fmt.Errorf("auto-start registration failed")
	}

	// [4] Explicitly re-mount the firewall (new rules) + health verification.
	logx.Step("[4/4] redeploy firewall and health verification")
	if err := applyFW(mode); err != nil {
		return fmt.Errorf("firewall deploy failed: %v", err)
	}
	if err := health.WaitHealthy(p.Conf, 30*time.Second, ""); err != nil {
		return fmt.Errorf("health verification timed out")
	}

	os.WriteFile(p.LastUp, []byte(time.Now().Format("2006-01-02 15:04:05")+"\n"), 0o644)
	logx.Info("redeploy complete %s: CLI/units refreshed, firewall re-mounted, config kept", version)
	return nil
}

// redeployDryRun reports the install state and the files a real run would refresh.
func redeployDryRun(cmd *cobra.Command) error {
	p := paths.Get()
	logx.Info("== redeploy --dry-run (dry-run mode, does not execute) ==")
	if !exists(p.Conf) || !exists(p.Cli) {
		logx.Warn("no installed %s detected; for a fresh install use sudo %s init/deploy", constants.ProgName, constants.ProgName)
	} else {
		mode := statemode.Read(p.State)
		logx.Info("installed: %s + %s (mode %s)", p.Conf, p.Cli, mode)
	}
	self, _ := os.Executable()
	self, _ = filepath.EvalSymlinks(self)
	logx.Step("[plan] refresh in place: CLI %s -> %s, man/units/sysctl/default-config, then restart + re-mount FW", self, p.Cli)
	logx.Info("kept untouched: %s (subscription/node selection/groups) and %s", p.Conf, p.Proxies)
	logx.Info("== dry-run done. Real run: sudo %s redeploy", constants.ProgName)
	return nil
}
