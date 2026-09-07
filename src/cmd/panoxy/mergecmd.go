package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/deadship2003/panoxy/internal/config"
	"github.com/deadship2003/panoxy/internal/constants"
	"github.com/deadship2003/panoxy/internal/health"
	"github.com/deadship2003/panoxy/internal/logx"
	"github.com/deadship2003/panoxy/internal/paths"
	"github.com/deadship2003/panoxy/internal/systemdunit"
)

// runMergeConf is an additive merge: same-name group merge + new append + base keep + backup rollback.
func runMergeConf(cmd *cobra.Command, args []string) error {
	// --rollback: restore from the premerge backup.
	if rb, _ := cmd.Flags().GetBool("rollback"); rb {
		return mergeRollback()
	}
	return withRootLock(func(p paths.Paths) error { return runMergeConfBody(p, cmd, args) })
}

func runMergeConfBody(p paths.Paths, cmd *cobra.Command, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: %s merge-conf <personal-config.yaml> (additive merge; --dry-run preview; --rollback revert)", constants.ProgName)
	}

	dryRun, _ := cmd.Flags().GetBool("dry-run")
	dnsMode, _ := cmd.Flags().GetString("dns")
	noWire, _ := cmd.Flags().GetBool("no-wire")

	// The base uses the clean default template (config.default.yaml), not the running /etc/clash.yaml —
	// so the merge result is reproducible: the personal config is overlaid onto a clean baseline, and the
	// placeholder subscription (SUB_URL_PLACEHOLDER) is auto-retired by MergePersonal.
	base, err := config.Load(p.DefaultConf)
	if err != nil {
		return fmt.Errorf("failed to read the default base config: %w (%s does not exist, run sudo %s redeploy first to generate it)", err, p.DefaultConf, constants.ProgName)
	}
	personal, err := config.Load(args[0])
	if err != nil {
		return fmt.Errorf("failed to read the personal config: %w", err)
	}

	opts := config.MergeOpts{DNSMine: dnsMode == "mine", NoWire: noWire}
	rep, err := base.MergePersonal(personal, opts)
	if err != nil {
		return err
	}
	baseProviders := base.Providers()
	base.WireAfterMerge(baseProviders, rep.PersonalProxies, opts)

	printMergeReport(rep, baseProviders)

	if dryRun {
		fmt.Fprintln(os.Stderr, "--dry-run: dry-run mode, no writes and no backup. The merge result preview goes to stdout.")
		os.Stdout.WriteString(mustRender(base))
		return nil
	}

	// Back up (premerge) → merge → -t → apply → health → restore on failure.
	logx.Step("back up current config → %s", p.Conf+constants.PremergeSuffix())
	bakPath, err := config.PremergeBackup(p.Conf)
	if err != nil {
		return fmt.Errorf("premerge backup failed: %w", err)
	}
	rep.BackupPath = bakPath

	tmpConf := filepath.Join(os.TempDir(), fmt.Sprintf("%s-merge-%d.yaml", constants.ProgName, time.Now().UnixNano()))
	defer os.Remove(tmpConf)
	os.WriteFile(tmpConf, []byte(mustRender(base)), 0o644)
	if out, err := mihomoTest(p, tmpConf); err != nil {
		return fmt.Errorf("merge result failed kernel validation (%s), system unchanged; use --dry-run to inspect", firstErrLine(out))
	}

	logx.Step("apply merged config → restart service")
	if err := os.WriteFile(p.Conf, []byte(mustRender(base)), 0o644); err != nil {
		config.PremergeRestore(p.Conf)
		return err
	}
	if err := systemdunit.Restart(); err != nil {
		config.PremergeRestore(p.Conf)
		systemdunit.Restart()
		return fmt.Errorf("restart failed, restored from premerge")
	}
	if err := health.WaitHealthy(p.Conf, 30*time.Second, ""); err != nil {
		config.PremergeRestore(p.Conf)
		systemdunit.Restart()
		return fmt.Errorf("health check timed out after merge, restored from premerge: %w", err)
	}
	logx.Info("merge complete: %s sub list to view subscriptions; group/node selection is done in the web UI", constants.ProgName)
	logx.Info("rollback: sudo %s merge-conf --rollback (restore to pre-merge state)", constants.ProgName)
	return nil
}

func mergeRollback() error {
	return withRootLock(func(p paths.Paths) error {
		if !config.PremergeExists(p.Conf) {
			return fmt.Errorf("no premerge backup (%s%s does not exist)", p.Conf, constants.PremergeSuffix())
		}
		if err := config.PremergeRestore(p.Conf); err != nil {
			return fmt.Errorf("restore failed: %w", err)
		}
		if err := systemdunit.Restart(); err != nil {
			return fmt.Errorf("config restored but restart failed: %w", err)
		}
		logx.Info("restored from the premerge backup and restarted")
		return nil
	})
}

func printMergeReport(r *config.MergeReport, _ []string) {
	logx.Info("merge decision:")
	if len(r.GroupsMerged) > 0 {
		logx.Info("  groups merged (same name): %v", r.GroupsMerged)
	}
	if len(r.GroupsAdded) > 0 {
		logx.Info("  groups added (personal): %v", r.GroupsAdded)
	}
	if len(r.GroupsKept) > 0 {
		logx.Info("  groups kept (base): %v (%d groups total)", r.GroupsKept[:min(5, len(r.GroupsKept))], len(r.GroupsKept))
		if len(r.GroupsKept) > 5 {
			logx.Info("    ...etc")
		}
	}
	if r.RulesPersonal > 0 || r.RulesBase > 0 {
		logx.Info("  rules: %d personal prepended + %d base fallback (deduped %d)", r.RulesPersonal, r.RulesBase, r.RulesDeduped)
	}
	logx.Info("  taken over (personal): %v", r.Taken)
	logx.Info("  kept (base): %v", r.Kept)
	if len(r.Providers.BaseKept) > 0 {
		logx.Info("  subscription base: %v", r.Providers.BaseKept)
	}
	if len(r.Providers.Personal) > 0 {
		logx.Info("  subscription added: %v", r.Providers.Personal)
	}
	if len(r.Providers.Conflict) > 0 {
		logx.Warn("  subscription same-name (base wins): %v", r.Providers.Conflict)
	}
	if len(r.RuleProvidersAdded) > 0 {
		logx.Info("  rule subscription merged in: %v", r.RuleProvidersAdded)
	}
	if len(r.PersonalProxies) > 0 {
		logx.Info("  personal nodes brought in (%d, appended to group end)", len(r.PersonalProxies))
	}
	for _, a := range r.Adjustments {
		logx.Info("  auto-adjustment: %s", a)
	}
	if r.BackupPath != "" {
		logx.Info("  backup: %s (recoverable via --rollback)", r.BackupPath)
	}
}

func mustRender(e *config.Editor) string {
	out, err := e.Render()
	if err != nil {
		return fmt.Sprintf("# render failed: %v\n", err)
	}
	return out
}
