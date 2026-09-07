package main

import (
	"github.com/spf13/cobra"

	"github.com/deadship2003/panoxy/internal/constants"
)

// Top-level service lifecycle verbs: thin, strict-semantics aliases over the `service`
// group (LIF-001 rule 3). start/stop/restart touch only the current instance; boot
// auto-start is managed separately via `service enable` / `service disable`.
//
// The firewall is normally loaded/removed by the unit's ExecStartPost (fw apply) /
// ExecStop (fw clean) hooks; stop additionally does an explicit clean + the commands
// wait for health, so a start/stop is verifiable rather than a blind systemctl
// pass-through, and stale firewall rules can never survive a stop.

func cmdStart() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "start the service now (transient; boot auto-start unchanged — enable via `service enable`)",
		Long: `Start the ` + constants.ProgName + ` service for the current boot and wait for the API to become
healthy. The service unit's ExecStartPost loads the firewall, so starting via systemd also
restores the DNS-hijack/TPROXY rules.

Strict semantics: boot auto-start is NOT changed by start (use ` + constants.ProgName + ` service enable
to register auto-start; init/deploy do it as part of deployment). Idempotent: running it
while the service is already active just reports the current state.`,
		Example: "  sudo " + constants.ProgName + " start     # start now (boot state untouched)",
		RunE:    runStart,
	}
}

func cmdStop() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "stop the service and clear the firewall (transient; boot auto-start unchanged)",
		Long: `Stop the ` + constants.ProgName + ` service and explicitly tear down the firewall rules (a failed or
crashed unit may not have run ExecStop; stale rules must never survive a stopped gateway).

Strict semantics: boot auto-start is NOT changed by stop — a stopped service still starts
on the next boot if enabled. To also unregister auto-start use ` + constants.ProgName + ` service disable.
Everything is kept (config/subscriptions/data); ` + constants.ProgName + ` start brings it back. For a
full removal use ` + constants.ProgName + ` service uninstall.`,
		Example: "  sudo " + constants.ProgName + " stop      # stop and clear firewall (boot state untouched)",
		RunE:    runStop,
	}
}

func cmdRestart() *cobra.Command {
	return &cobra.Command{
		Use:   "restart",
		Short: "restart the service (self-heals the firewall) and verify health",
		Long: `Restart the ` + constants.ProgName + ` service. The unit's ExecStop/ExecStartPost re-run the firewall
teardown and apply, so a restart also self-heals any stale rules left by kill -9/OOM. Boot
auto-start is never changed by a restart.`,
		Example: "  sudo " + constants.ProgName + " restart   # restart (self-heals firewall)",
		RunE:    runRestart,
	}
}

// runStart/runStop/runRestart share the service group's implementations (exit codes and
// locking included) — the top-level verbs are the same operations under their short names.
func runStart(cmd *cobra.Command, args []string) error {
	if err := requireRootSvc(); err != nil {
		return err
	}
	return withRootLock(svcStart)
}

func runStop(cmd *cobra.Command, args []string) error {
	if err := requireRootSvc(); err != nil {
		return err
	}
	return withRootLock(svcStop)
}

func runRestart(cmd *cobra.Command, args []string) error {
	if err := requireRootSvc(); err != nil {
		return err
	}
	return withRootLock(svcRestart)
}
