package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/deadship2003/panoxy/internal/constants"
	"github.com/deadship2003/panoxy/internal/core"
	"github.com/deadship2003/panoxy/internal/logx"
	"github.com/deadship2003/panoxy/internal/paths"
	"github.com/deadship2003/panoxy/internal/systemdunit"

	"github.com/metacubex/mihomo/hub"
)

// cmdRun is the in-process kernel entry, invoked as the systemd unit's ExecStart; it blocks
// until SIGTERM. Config and geodata are read from the install directory; the other
// subcommands manage this daemon from separate processes over the REST API.
func cmdRun() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "run the embedded mihomo kernel in-process (systemd ExecStart; not for direct use)",
		Long: `Run the embedded mihomo kernel in-process, reading the config and geodata from the install
directory. This is the ExecStart of the panoxy systemd unit; it blocks until SIGTERM.

Management is done by the other panoxy subcommands (sub/mode/etc.) in separate processes, which
talk to this daemon over the REST API. Normally you never run this directly.`,
		RunE: runKernel,
	}
}

func runKernel(cmd *cobra.Command, args []string) error {
	if err := guardSingleInstance(); err != nil {
		return err
	}
	return runKernelBody(paths.Get())
}

func runKernelBody(p paths.Paths) error {
	return core.Run(p.Root, p.Conf, hub.WithExternalUI(p.UiDir))
}

// guardSingleInstance refuses to boot a second kernel while the systemd-managed service
// instance is already alive (LIF-001 red line 1: two kernels fighting over the
// transparent-proxy ports randomly corrupt both). When this process was itself spawned
// by systemd (INVOCATION_ID set), the probe is skipped — on that path systemd is the
// single-instance authority.
func guardSingleInstance() error {
	if os.Getenv("INVOCATION_ID") != "" {
		return nil
	}
	if systemdunit.IsActive() {
		return fmt.Errorf("the %s service instance is already running — stop it first (sudo %s service stop), or watch it live with: journalctl -u %s -f",
			constants.ProgName, constants.ProgName, constants.ProgName)
	}
	return nil
}

// runDebugForeground is the LIF-002 --debug run: blocking foreground execution of the
// business logic (the embedded kernel), with the environment restored from the installed
// system unit, mutual exclusion against the systemd-managed instance, and Ctrl-C/SIGTERM
// handled as a graceful shutdown inside core.Run.
func runDebugForeground(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("--debug takes no arguments (got %q); it boots the kernel in the foreground", args[0])
	}
	if usr, _ := cmd.Flags().GetBool("user"); usr {
		return errCode(4, "user scope is unsupported: a transparent gateway needs root (nftables/TUN//etc); run without --user (system scope)")
	}
	if err := guardSingleInstance(); err != nil {
		return err
	}
	p := paths.Get()
	// Environment alignment: restore the unit's Environment= assignments so the foreground
	// run sees the production context (custom --root etc.). The unit sets no
	// WorkingDirectory — ExecStart paths are absolute — so only env needs restoring.
	if env := systemdunit.UnitEnv(p); len(env) > 0 {
		for k, v := range env {
			os.Setenv(k, v)
		}
		p = paths.Get()
		logx.Info("environment restored from %s", filepath.Join(p.UnitDir, constants.ProgName+".service"))
	} else {
		logx.Info("no installed unit found; running with the current environment (config %s)", p.Conf)
	}
	logx.Info("foreground kernel run starting — Ctrl-C to stop; kernel logs stream to this terminal")
	return runKernelBody(p)
}
