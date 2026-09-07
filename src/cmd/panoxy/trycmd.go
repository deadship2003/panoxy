package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/deadship2003/panoxy/internal/constants"
	"github.com/deadship2003/panoxy/internal/logx"
)

// runTry is a pre-install dry run (sandboxed, no root): it truly runs the full init/deploy
// flow without touching the real system — real asset downloads, real kernel boot (TUN/routing-mark
// stripped under the non-root constraint), real subscription import with node-count verification,
// and a real health check. Passing = safe to sudo for a real install.
//
// Implementation is a "productized e2e sandbox": all paths are redirected into a sandbox dir via
// env vars; systemd/ip/sysctl use built-in shims (kernel boot strips tun and routing-mark —
// without root, TUN device creation and SO_MARK fail with EPERM; a real root deploy has no such limit).
func runTry(cmd *cobra.Command, args []string) error {
	dir, _ := cmd.Flags().GetString("dir")
	if dir == "" {
		dir = filepath.Join(os.TempDir(), fmt.Sprintf("%s-try-%d", constants.ProgName, time.Now().Unix()))
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		return err
	}

	// Sandbox systemd shim: enable/restart boot the kernel directly with tun+routing-mark stripped.
	shim := filepath.Join(dir, "bin", "systemctl")
	pidf := filepath.Join(dir, "pid")
	// On exit, stop the sandbox kernel as a last resort: do not leave a background kernel holding
	// the transparent-proxy ports, otherwise a subsequent sudo panoxy init/deploy would be blocked
	// by our own leftover process (port conflict).
	defer stopSandboxKernel(pidf)
	shimScript := fmt.Sprintf(`#!/bin/sh
# {{PROG}} try sandbox shim (productized e2e): strip tun section and routing-mark when booting the kernel as non-root
# (TUN device creation / SO_MARK need CAP_NET_ADMIN; a real root deploy has no such limit).
# Lifecycle verbs map 1:1 onto the sandbox kernel: start/stop/restart drive the process,
# enable/disable are registration-only no-ops (there is no real systemd here).
PIDF=%s
start_mh() {
  awk '/^tun:/{s=1;next} /^routing-mark:/{next} s && /^[^ \t#]/{s=0} !s{print}' "${{PREFIX}}_CONF" > "${{PREFIX}}_CONF.notun"
  # INVOCATION_ID marks the kernel as deliberately spawned by this shim (which plays
  # systemd), skipping the run command's single-instance probe.
  INVOCATION_ID=1 {{PREFIX}}_CONF="${{PREFIX}}_CONF.notun" "${{PREFIX}}_CLI" run >> "${{PREFIX}}_ROOT/run.log" 2>&1 < /dev/null &
  echo $! > "$PIDF"
}
kill_mh() { while read p; do kill "$p" 2>/dev/null; done < "$PIDF" 2>/dev/null; : > "$PIDF"; }
alive_mh() { a=0; while read p; do kill -0 "$p" 2>/dev/null && a=1; done < "$PIDF" 2>/dev/null; [ "$a" = 1 ]; }
case "$1" in
  start)   [ "$2" = {{PROG}}.service ] && start_mh ;;
  stop)    [ "$2" = {{PROG}}.service ] && kill_mh ;;
  restart) [ "$2" = {{PROG}}.service ] && { kill_mh; sleep 1; start_mh; } ;;
  enable|disable) : ;;  # registration only — never starts/stops anything
  is-active) alive_mh && echo active || { echo inactive; exit 3; } ;;
  is-enabled) echo disabled; exit 1 ;;
  show) if [ "$2" = {{PROG}}.service ]; then
          if alive_mh; then echo "ActiveState=active"; echo "MainPID=$(tail -n 1 "$PIDF" 2>/dev/null)";
          else echo "ActiveState=inactive"; fi
          echo "Result=success"; echo "NRestarts=0"
        fi ;;
esac
exit 0
`, pidf)
	shimScript = strings.ReplaceAll(shimScript, "{{PROG}}", constants.ProgName)
	shimScript = strings.ReplaceAll(shimScript, "{{PREFIX}}", constants.EnvPrefix())
	if err := os.WriteFile(shim, []byte(shimScript), 0o755); err != nil {
		return err
	}
	for _, name := range []string{"ip", "sysctl"} {
		os.WriteFile(filepath.Join(dir, "bin", name), []byte("#!/bin/sh\nexit 0\n"), 0o755)
	}

	// Redirect all paths into the sandbox; prepend shim to PATH; no root.
	pfx := constants.EnvPrefix()
	for k, v := range map[string]string{
		pfx + "_ROOT":          filepath.Join(dir, "root"),
		pfx + "_CONF":          filepath.Join(dir, "clash.yaml"),
		pfx + "_UNIT_DIR":      filepath.Join(dir, "units"),
		pfx + "_CLI":           filepath.Join(dir, "cli", constants.ProgName),
		pfx + "_MAN":           filepath.Join(dir, "man", constants.ProgName+".1.gz"),
		pfx + "_STATE":         filepath.Join(dir, "state.yaml"),
		pfx + "_SYSCTL":        filepath.Join(dir, "99-sysctl.conf"),
		pfx + "_LOCK":          filepath.Join(dir, "lock"),
		pfx + "_ALLOW_NONROOT": "1",
		"PATH":                 filepath.Join(dir, "bin") + ":" + os.Getenv("PATH"),
	} {
		os.Setenv(k, v)
	}
	// Reusable environment: after sourcing, you can run status/sub list etc. against the sandbox in the same shell.
	envFile := filepath.Join(dir, "env.sh")
	os.WriteFile(envFile, []byte(fmt.Sprintf(`# after sourcing, you can run %[1]s status / sub list / sub import etc. against this sandbox
export %[2]s_ROOT=%[3]q
export %[2]s_CONF=%[4]q
export %[2]s_UNIT_DIR=%[5]q
export %[2]s_CLI=%[6]q
export %[2]s_MAN=%[7]q
export %[2]s_STATE=%[8]q
export %[2]s_SYSCTL=%[9]q
export %[2]s_LOCK=%[10]q
export %[2]s_ALLOW_NONROOT=1
# sandbox shim takes priority (status/is-active etc. act on the sandbox)
case ":$PATH:" in
  *":%[11]s:"*) ;;
  *) export PATH="%[11]s:$PATH" ;;
esac
`,
		constants.ProgName,
		pfx, filepath.Join(dir, "root"), filepath.Join(dir, "clash.yaml"), filepath.Join(dir, "units"),
		filepath.Join(dir, "cli", constants.ProgName), filepath.Join(dir, "man", constants.ProgName+".1.gz"),
		filepath.Join(dir, "state.yaml"), filepath.Join(dir, "99-sysctl.conf"), filepath.Join(dir, "lock"),
		filepath.Join(dir, "bin"))), 0o644)

	logx.Info("sandbox: %s (no root; real download/kernel/subscription, firewall and TUN not installed)", dir)
	logx.Info("sandbox constraint: tun and routing-mark stripped at boot (non-root limit); a real deploy (sudo init) has no such limit")
	logx.Info("tip: if an existing deployment on this machine holds ports 33833/9999/1053, stop the service first or try on another machine")

	// Reuse the full init flow (its internal needRoot is bypassed by PANIXY_ALLOW_NONROOT).
	if err := runInit(cmd, args); err != nil {
		return fmt.Errorf("pre-install check failed: %w\nsandbox kept at %s (see %s/root/run.log), fix and retry or rm -rf to clean up", err, dir, dir)
	}

	fmt.Fprintln(os.Stderr)
	logx.Info("pre-install check passed ✓ for a real deployment run: sudo %s init %s", constants.ProgName, subArgsHint(args))
	logx.Info("sandbox kernel stopped (no background process left, won't affect a later deploy/init); sandbox files kept at %s (see %s/root/run.log)", dir, dir)
	logx.Info("clean up the sandbox: rm -rf %s   # safe to delete anytime, does not affect the system", dir)
	// Ensure a clean prompt return (append a newline to stdout+stderr and flush).
	fmt.Fprintln(os.Stdout)
	os.Stdout.Sync()
	fmt.Fprintln(os.Stderr)
	os.Stderr.Sync()
	return nil
}

func subArgsHint(args []string) string {
	if len(args) > 0 {
		return "'" + args[0] + "'   # remember the quotes"
	}
	return "(press enter to paste subscription)"
}

// stopSandboxKernel stops the kernel booted by the sandbox shim (the shim's start_mh writes its pid into pidf).
// It must be called when try finishes, otherwise the leftover kernel would hold the transparent-proxy ports
// and block a subsequent real init/deploy.
func stopSandboxKernel(pidf string) {
	b, err := os.ReadFile(pidf)
	if err != nil {
		return
	}
	for _, p := range strings.Fields(string(b)) {
		syscallKill(p)
	}
}
