package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/deadship2003/panoxy/internal/constants"
	"github.com/deadship2003/panoxy/internal/firewall"
	"github.com/deadship2003/panoxy/internal/health"
	"github.com/deadship2003/panoxy/internal/logx"
	"github.com/deadship2003/panoxy/internal/paths"
	"github.com/deadship2003/panoxy/internal/statemode"
	"github.com/deadship2003/panoxy/internal/systemdunit"
)

// Service lifecycle commands (LIF-001): a unified `service` group plus the strict-semantics
// top-level start/stop/restart verbs. Transient control (start/stop/restart) touches only the
// current instance; persistent registration (enable/disable) never starts or stops anything.

// ---- unified exit codes (LIF-001 rule 5) ----
// 0 success / 1 invalid arguments (cobra default) / 2 insufficient privileges /
// 3 service operation failed / 4 platform does not support this scope.

// exitCoder lets a command error request a specific process exit code.
type exitCoder interface{ ExitCode() int }

type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }
func (e *exitError) ExitCode() int { return e.code }

// errCode builds an error carrying one of the unified exit codes.
func errCode(code int, format string, a ...any) error {
	return &exitError{code, fmt.Sprintf(format, a...)}
}

// requireRootSvc is the privilege gate for service operations: non-root yields exit code 2
// with escalation guidance (the e2e sandbox hook mirrors needRoot's).
func requireRootSvc() error {
	if os.Getenv(constants.EnvPrefix()+"_ALLOW_NONROOT") != "" {
		return nil
	}
	if os.Geteuid() != 0 {
		return errCode(2, "insufficient privileges: run with sudo (this operation manages the system service)")
	}
	return nil
}

// scopeOf resolves the --system/--user scope flags. A transparent gateway needs root for
// the firewall, TUN device and /etc, so user scope is unsupported — explicit error, never
// a silent downgrade (LIF-001 rule 4).
func scopeOf(cmd *cobra.Command) (string, error) {
	sys, _ := cmd.Flags().GetBool("system")
	usr, _ := cmd.Flags().GetBool("user")
	if sys && usr {
		return "", fmt.Errorf("--system and --user are mutually exclusive")
	}
	if usr {
		return "", errCode(4, "user scope is unsupported: a transparent gateway needs root (nftables/TUN//etc); use the system scope (default, with sudo)")
	}
	return "system", nil
}

// serviceStatusJSON is the fixed-field machine-readable status (LIF-001 rule 6).
type serviceStatusJSON struct {
	Scope         string `json:"scope"`
	UnitPath      string `json:"unitPath"`
	Installed     bool   `json:"installed"`
	Enabled       bool   `json:"enabled"`
	Running       bool   `json:"running"`
	PID           int    `json:"pid"`
	UptimeSeconds int64  `json:"uptimeSeconds"`
	LastExitCode  int    `json:"lastExitCode"`
	LastError     string `json:"lastError"`
	RestartCount  int    `json:"restartCount"`
	PlatformInit  string `json:"platformInit"`
}

func cmdService() *cobra.Command {
	c := &cobra.Command{
		Use:   "service <install|uninstall|start|stop|restart|enable|disable|status>",
		Short: "service lifecycle management (LIF-001 set; strict start/stop vs enable/disable separation)",
		Long: `Manage the ` + constants.ProgName + ` systemd service with the standard lifecycle subcommand set.

Strict separation (transient vs persistent):
  start / stop / restart   touch only the current instance — boot auto-start is never changed
  enable / disable         change only the auto-start registration — the running service is
                           never started/stopped (enable also arms the daily upgrade timer,
                           which is part of the registration; disable disarms it)
  install / uninstall      service-registration layer: write/remove the systemd units.
                           Unit files are generated only by this program — re-install to
                           refresh them, hand-editing is pointless (they get overwritten).

Scope: only --system is supported (a transparent gateway needs root for nftables/TUN//etc);
--user fails explicitly with exit code 4.

Exit codes: 0 success · 1 invalid arguments · 2 insufficient privileges ·
3 service operation failed (not installed / systemctl failure / health timeout) ·
4 unsupported scope.`,
		Example: `  sudo panixy service install              # write/refresh units (no start, no enable)
  sudo panixy service start                # start now (survives nothing; boot state untouched)
  sudo panixy service enable               # register auto-start on boot (+ arm upgrade timer)
  sudo panixy service status --json        # fixed-field machine-readable status
  sudo panixy service disable              # unregister auto-start (a running service keeps running)
  sudo panixy service stop                 # stop now + clear firewall (boot state untouched)
  sudo panixy service uninstall            # remove units (data and config kept)`,
	}
	c.PersistentFlags().Bool("system", false, "operate on the system-scope service (default; the only supported scope)")
	c.PersistentFlags().Bool("user", false, "operate on the user-scope service (unsupported: transparent proxying needs root)")
	c.PersistentFlags().Bool("json", false, "machine-readable output (status carries the fixed field set)")
	c.AddCommand(
		cmdServiceInstall(), cmdServiceUninstall(),
		cmdServiceStart(), cmdServiceStop(), cmdServiceRestart(),
		cmdServiceEnable(), cmdServiceDisable(), cmdServiceStatus(),
	)
	return c
}

func svcRunner(fn func(p paths.Paths) error) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if _, err := scopeOf(cmd); err != nil {
			return err
		}
		if err := requireRootSvc(); err != nil {
			return err
		}
		return withRootLock(func(p paths.Paths) error { return fn(p) })
	}
}

func cmdServiceInstall() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "write the systemd units (idempotent; overwrite = reset config; no start/enable)",
		RunE:  svcRunner(svcInstall),
	}
}

func cmdServiceUninstall() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "remove the units (stop + deregister first; data and config kept; not installed = success)",
		RunE:  svcRunner(svcUninstall),
	}
}

func cmdServiceStart() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "start the service now (transient; boot auto-start unchanged) and verify health",
		RunE:  svcRunner(svcStart),
	}
}

func cmdServiceStop() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "stop the service and clear the firewall (transient; boot auto-start unchanged)",
		RunE:  svcRunner(svcStop),
	}
}

func cmdServiceRestart() *cobra.Command {
	return &cobra.Command{
		Use:   "restart",
		Short: "restart the service (self-heals the firewall) and verify health",
		RunE:  svcRunner(svcRestart),
	}
}

func cmdServiceEnable() *cobra.Command {
	return &cobra.Command{
		Use:   "enable",
		Short: "register auto-start on boot and arm the upgrade timer (never starts the service)",
		RunE:  svcRunner(svcEnable),
	}
}

func cmdServiceDisable() *cobra.Command {
	return &cobra.Command{
		Use:   "disable",
		Short: "unregister auto-start and disarm the upgrade timer (never stops the service)",
		RunE:  svcRunner(svcDisable),
	}
}

func cmdServiceStatus() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "service status from the init system (fixed fields; --json for machine output)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := scopeOf(cmd); err != nil {
				return err
			}
			return svcStatus(cmd, paths.Get())
		},
	}
}

// ---- shared implementations (the top-level start/stop/restart verbs reuse them) ----

// svcInstall writes the units for the current mode (service-registration layer only).
// Idempotent; re-running over an existing installation overwrites and resets the unit
// configuration without starting or enabling anything (LIF-001 rule 2).
func svcInstall(p paths.Paths) error {
	mode := statemode.Read(p.State)
	logx.Step("validate config with the embedded kernel (-t)")
	if out, err := mihomoTest(p, p.Conf); err != nil {
		return errCode(3, "config validation failed (%s)", firstErrLine(out))
	}
	if err := systemdunit.Write(p, mode); err != nil {
		return errCode(3, "%v", err)
	}
	logx.Info("service installed for mode %s (units written, daemon reloaded; not started — use %s service start / enable)", mode, constants.ProgName)
	return nil
}

// svcUninstall removes the registration: deregister, stop, clear firewall, remove units and
// the sysctl/man files. Data dir and config are kept. Not installed = success message.
func svcUninstall(p paths.Paths) error {
	if !systemdunit.Installed(p) {
		logx.Info("service not installed; nothing to do")
		return nil
	}
	if err := systemdunit.Disable(); err != nil {
		return errCode(3, "%v", err)
	}
	if err := systemdunit.StopSvc(); err != nil {
		return errCode(3, "%v", err)
	}
	if err := firewall.CleanAll(); err != nil {
		logx.Warn("firewall cleanup failed: %v (retry uninstall after restart)", err)
	}
	systemdunit.Remove(p)
	os.Remove(p.Sysctl)
	os.Remove(p.ManGz)
	logx.Info("service uninstalled (unit/timer/sysctl/man removed); data dir %s and %s are kept", p.Root, p.Conf)
	return nil
}

// svcStart starts the current instance and waits for health. Boot enablement is untouched.
func svcStart(p paths.Paths) error {
	if err := requireInstalledSvc(p); err != nil {
		return err
	}
	if systemdunit.IsActive() {
		logx.Info("service already active (boot enablement unchanged; see %s service status)", constants.ProgName)
		return nil
	}
	logx.Step("start service (transient; boot auto-start unchanged)")
	if err := systemdunit.Start(); err != nil {
		return errCode(3, "%v", err)
	}
	if err := health.WaitHealthy(p.Conf, 30*time.Second, ""); err != nil {
		return errCode(3, "service started but health check timed out: %v", err)
	}
	logx.Info("service started (firewall loaded via the unit's ExecStartPost); enable auto-start with %s service enable", constants.ProgName)
	return nil
}

// svcStop stops the current instance and explicitly clears the firewall (a crashed unit may
// not have run ExecStop; stale rules must never survive a stopped gateway). Boot enablement
// is untouched.
func svcStop(p paths.Paths) error {
	if err := requireInstalledSvc(p); err != nil {
		return err
	}
	logx.Step("stop service (transient; boot auto-start unchanged)")
	if err := systemdunit.StopSvc(); err != nil {
		return errCode(3, "%v", err)
	}
	if err := firewall.CleanAll(); err != nil {
		logx.Warn("firewall cleanup failed: %v (retry %s fw clean)", err, constants.ProgName)
	}
	logx.Info("service stopped and firewall rules removed; auto-start unchanged (%s service start resumes, %s service enable/disable controls boot)", constants.ProgName, constants.ProgName)
	return nil
}

// svcRestart restarts the current instance (unit hooks re-load the firewall, self-healing
// kill -9 leftovers) and waits for health.
func svcRestart(p paths.Paths) error {
	if err := requireInstalledSvc(p); err != nil {
		return err
	}
	logx.Step("restart service (unit re-loads the firewall)")
	if err := systemdunit.Restart(); err != nil {
		return errCode(3, "%v", err)
	}
	if err := health.WaitHealthy(p.Conf, 30*time.Second, ""); err != nil {
		return errCode(3, "service restarted but health check timed out: %v", err)
	}
	logx.Info("service restarted (firewall self-healed); %s status to verify", constants.ProgName)
	return nil
}

// svcEnable registers auto-start for the service and arms the daily upgrade timer.
// It never starts or stops the service (LIF-001 rule 3).
func svcEnable(p paths.Paths) error {
	if err := requireInstalledSvc(p); err != nil {
		return err
	}
	logx.Step("register auto-start on boot (+ arm the upgrade timer; the service itself is not started)")
	if err := systemdunit.Enable(); err != nil {
		return errCode(3, "%v", err)
	}
	logx.Info("auto-start registered on boot; upgrade timer armed (a stopped service stays stopped until %s service start)", constants.ProgName)
	return nil
}

// svcDisable removes the auto-start registration and disarms the timer. A running service
// keeps running (LIF-001 rule 3).
func svcDisable(p paths.Paths) error {
	if err := requireInstalledSvc(p); err != nil {
		return err
	}
	logx.Step("unregister auto-start on boot (+ disarm the upgrade timer; a running service is not stopped)")
	if err := systemdunit.Disable(); err != nil {
		return errCode(3, "%v", err)
	}
	logx.Info("auto-start unregistered; upgrade timer disarmed (stop the running service with %s service stop)", constants.ProgName)
	return nil
}

// svcStatus prints the init-system view of the service (no root, no lock needed).
func svcStatus(cmd *cobra.Command, p paths.Paths) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	st := systemdunit.Status(p)
	unitPath := filepath.Join(p.UnitDir, constants.ProgName+".service")
	if asJSON {
		b, err := json.Marshal(serviceStatusJSON{
			Scope:         "system",
			UnitPath:      unitPath,
			Installed:     st.Installed,
			Enabled:       st.Enabled,
			Running:       st.Running,
			PID:           st.PID,
			UptimeSeconds: st.UptimeSeconds,
			LastExitCode:  st.LastExitCode,
			LastError:     st.LastError,
			RestartCount:  st.RestartCount,
			PlatformInit:  "systemd",
		})
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}
	fmt.Printf("service:   %s (system scope)\n", constants.ProgName)
	fmt.Printf("unit:      %s\n", unitPath)
	fmt.Printf("installed: %s\n", yesNo(st.Installed))
	fmt.Printf("enabled:   %s (auto-start on boot)\n", yesNo(st.Enabled))
	fmt.Printf("running:   %s\n", runningDesc(st))
	if st.LastError != "" || st.RestartCount > 0 {
		fmt.Printf("exit:      code %d, restarts %d, last error %q\n", st.LastExitCode, st.RestartCount, st.LastError)
	}
	fmt.Printf("platform:  systemd\n")
	return nil
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func runningDesc(st systemdunit.StatusInfo) string {
	if !st.Running {
		return "no (inactive)"
	}
	if st.PID > 0 {
		return fmt.Sprintf("yes (pid %d, up %s)", st.PID, humanDuration(st.UptimeSeconds))
	}
	return "yes"
}

// humanDuration renders seconds as "2h13m" style (sub-minute shows seconds).
func humanDuration(sec int64) string {
	d := time.Duration(sec) * time.Second
	if d < time.Minute {
		return d.Truncate(time.Second).String()
	}
	return d.Truncate(time.Minute).String()
}

// requireInstalledSvc fails with exit code 3 when the unit has not been written
// (init/deploy have not run), instead of leaking systemctl's raw "unit not found".
func requireInstalledSvc(p paths.Paths) error {
	if !systemdunit.Installed(p) {
		return errCode(3, "no installed %s service detected (missing %s); install first with sudo %s init/deploy",
			constants.ProgName, filepath.Join(p.UnitDir, constants.ProgName+".service"), constants.ProgName)
	}
	return nil
}
