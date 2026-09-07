package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/deadship2003/panoxy/internal/config"
	"github.com/deadship2003/panoxy/internal/constants"
	"github.com/deadship2003/panoxy/internal/core"
	"github.com/deadship2003/panoxy/internal/health"
	"github.com/deadship2003/panoxy/internal/locker"
	"github.com/deadship2003/panoxy/internal/logx"
	"github.com/deadship2003/panoxy/internal/mihomoapi"
	"github.com/deadship2003/panoxy/internal/paths"
	"github.com/deadship2003/panoxy/internal/subscribe"
	"github.com/deadship2003/panoxy/internal/systemdunit"
)

func needRoot() error {
	if os.Getenv(constants.EnvPrefix()+"_ALLOW_NONROOT") != "" {
		return nil // test sandbox hook: for e2e, never set in production
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("please run with sudo")
	}
	return nil
}

// withRootLock unifies the "root check + process lock" boilerplate: after the check passes it hands
// the paths to fn, and unlocks automatically on return. In-process re-entry is supported by the
// locker, so nested calls like deploy→install and init→sub import are naturally safe.
func withRootLock(fn func(p paths.Paths) error) error {
	if err := needRoot(); err != nil {
		return err
	}
	p := paths.Get()
	lk, err := locker.Lock(p.Lock)
	if err != nil {
		return err
	}
	defer lk.Unlock()
	return fn(p)
}

// mihomoTest validates a config with the in-process kernel (equivalent to mihomo -t;
// since M2 no external binary is invoked).
func mihomoTest(p paths.Paths, conf string) (string, error) {
	b, err := os.ReadFile(conf)
	if err != nil {
		return "", err
	}
	if err := core.Validate(p.Root, b); err != nil {
		return err.Error(), err
	}
	return "", nil
}

// runSubImport implements sub import: prefetch → validate → incremental edit → -t → preload cache → restart → verify node count.
// Any failing step restores the backup (with cache); never a fake success.
func runSubImport(cmd *cobra.Command, args []string) error {
	return withRootLock(func(p paths.Paths) error { return runSubImportBody(p, cmd, args) })
}

func runSubImportBody(p paths.Paths, cmd *cobra.Command, args []string) error {
	name, _ := cmd.Flags().GetString("name")
	file, _ := cmd.Flags().GetString("file")
	groups, _ := cmd.Flags().GetStringSlice("group")
	if err := subscribe.CheckName(name); err != nil {
		return err
	}

	var url string
	var err error
	if len(args) > 0 {
		url = args[0]
	} else {
		if url, err = promptSubURL(constants.ProgName + " sub import [subscription-URL] [--file local-file] (or no argument to enter paste mode)"); err != nil {
			return err
		}
	}
	if err := subscribe.CheckURL(url); err != nil {
		return err
	}

	// 1) Fetch subscription content: local file > direct > via local proxy.
	var body []byte
	if file != "" {
		if body, err = subscribe.ValidateFile(file); err != nil {
			return err
		}
		logx.Info("using local subscription file: %s (skipping network fetch)", file)
	} else {
		if body, err = fetchSubBody(url, mihomoapi.NewFromConf(p.Conf)); err != nil {
			return fmt.Errorf(`subscription fetch or validation failed: %v
  tip: a URL passed on the command line must be single-quoted as a whole (chars like & ? get split by the shell), or just run
  sudo %s sub import and press enter for paste mode; in an offline environment import locally (download the subscription on any device then
  sudo %s sub import --file <subscription-file>), or set a working proxy %s_PROXY`, err, constants.ProgName, constants.ProgName, constants.EnvPrefix())
		}
	}

	// Normalize: non-Clash YAML formats like sing-box/Surge/base64-Clash are converted to Clash YAML;
	// Clash YAML / URI lists are parsed natively by mihomo and passed through as-is (converted decides whether the provider switches to file).
	nb, conv, err := subscribe.Normalize(body)
	if err != nil {
		return err
	}
	body, converted := nb, conv
	if converted {
		logx.Step("subscription is not Clash YAML, normalized; provider switched to local cache mode (type: file)")
	}

	// 2) Back up → incremental edit → -t validation → preload cache.
	// Collect all providers' node names (including this import) to prune derived groups: only keep the region/type groups actually hit.
	nodeNames := collectNodeNames(p, body, name)
	e, err := config.Load(p.Conf)
	if err != nil {
		return err
	}
	if err := config.Backup(p.Conf); err != nil {
		return fmt.Errorf("config backup failed: %w", err)
	}
	cache := filepath.Join(p.Proxies, name+".yaml")
	if b, err := os.ReadFile(cache); err == nil {
		os.WriteFile(cache+constants.BackupSuffix(), b, 0o644)
	}
	recoverAll := func(restart bool) {
		config.Restore(p.Conf)
		if b, err := os.ReadFile(cache + constants.BackupSuffix()); err == nil {
			os.WriteFile(cache, b, 0o644)
		}
		os.Remove(cache + constants.BackupSuffix())
		if restart {
			systemdunit.Restart()
		}
	}
	// Fresh system: the template placeholder subscription (SUB_URL_PLACEHOLDER) automatically retires on the first real
	// subscription import, avoiding leaving an empty provider that can never be fetched (real entries inherited from the
	// existing config are unaffected).
	for _, pn := range e.Providers() {
		if u, ok := e.ProviderURL(pn); ok && u == config.PlaceholderURL && pn != name {
			e.RemoveProvider(pn)
			e.WireProvider(pn, false, nil)
			logx.Step("placeholder subscription %s replaced by the real subscription %s", pn, name)
		}
	}
	rel := "./proxies/" + name + ".yaml"
	if err := e.SetProvider(name, url, rel); err != nil {
		recoverAll(false)
		return err
	}
	if err := e.SetProviderType(name, converted); err != nil {
		recoverAll(false)
		return err
	}
	e.WireProvider(name, true, groups)
	if pruned := e.PruneDerived(nodeNames); pruned > 0 {
		logx.Info("pruned %d region/type groups with no matching node (keeping only effective groups)", pruned)
	}
	if err := e.Save(); err != nil {
		recoverAll(false)
		return fmt.Errorf("config write failed: %w", err)
	}
	if out, err := mihomoTest(p, p.Conf); err != nil {
		msg := firstErrLine(out)
		recoverAll(false)
		return fmt.Errorf("config validation failed (%s), original config restored", msg)
	}
	if err := os.MkdirAll(p.Proxies, 0o755); err != nil {
		recoverAll(false)
		return err
	}
	if err := os.WriteFile(cache, body, 0o644); err != nil {
		recoverAll(false)
		return err
	}

	// 3) Restart to rebuild providers (hot-reload rebuilds objects but does not re-fetch content) + verify node count.
	logx.Step("subscription written to provider %s, cache preloaded, restarting kernel to take effect (changing URL requires a restart)", name)
	if err := systemdunit.Restart(); err != nil {
		recoverAll(false)
		return fmt.Errorf("restart failed, original subscription restored")
	}
	if err := health.WaitHealthy(p.Conf, 30*time.Second, ""); err != nil {
		recoverAll(true)
		return fmt.Errorf("health check timed out after restart, original subscription restored: %w", err)
	}
	api := mihomoapi.NewFromConf(p.Conf)
	nodes := 0
	for i := 0; i < 5; i++ {
		if st, err := api.Provider(name); err == nil {
			nodes = st.Nodes
			if nodes > 0 {
				break
			}
		}
		time.Sleep(2 * time.Second)
	}
	if nodes == 0 {
		recoverAll(true)
		return fmt.Errorf("subscription %s not loaded (node count is 0), original subscription restored; troubleshoot: %s log / %s check", name, constants.ProgName, constants.ProgName)
	}
	logx.Info("subscription (%s) loaded: %d nodes (speed-based selection is handled by the 🔃 auto-select group, fastest node by default)", name, nodes)
	config.ClearBackup(p.Conf)
	os.Remove(cache + constants.BackupSuffix())
	logx.Info("subscription import complete: %s", url)
	return nil
}

func runSubDel(cmd *cobra.Command, args []string) error {
	return withRootLock(func(p paths.Paths) error { return runSubDelBody(p, cmd, args) })
}

func runSubDelBody(p paths.Paths, cmd *cobra.Command, args []string) error {
	name, _ := cmd.Flags().GetString("name")
	if err := subscribe.CheckName(name); err != nil {
		return err
	}
	e, err := config.Load(p.Conf)
	if err != nil {
		return err
	}
	if _, ok := e.ProviderURL(name); !ok {
		return fmt.Errorf("subscription %s does not exist (existing: %v)", name, e.Providers())
	}
	if err := config.Backup(p.Conf); err != nil {
		return err
	}
	e.RemoveProvider(name)
	e.WireProvider(name, false, nil)
	if err := e.Save(); err != nil {
		config.Restore(p.Conf)
		return err
	}
	if out, err := mihomoTest(p, p.Conf); err != nil {
		msg := firstErrLine(out)
		config.Restore(p.Conf)
		return fmt.Errorf("validation failed after deletion (%s — deleting the only subscription makes the group lose its use), restored", msg)
	}
	os.Remove(filepath.Join(p.Proxies, name+".yaml"))
	if err := systemdunit.Restart(); err != nil {
		rollbackRestart(p)
		return fmt.Errorf("restart failed, restored")
	}
	if err := health.WaitHealthy(p.Conf, 30*time.Second, ""); err != nil {
		rollbackRestart(p)
		return fmt.Errorf("health check timed out after restart, restored: %w", err)
	}
	config.ClearBackup(p.Conf)
	logx.Info("subscription %s deleted and applied", name)
	return nil
}

func runSubList(cmd *cobra.Command, args []string) error {
	p := paths.Get()
	asJSON, _ := cmd.Flags().GetBool("json")
	e, err := config.Load(p.Conf)
	if err != nil {
		return err
	}
	names := e.Providers()
	api := mihomoapi.NewFromConf(p.Conf)
	stats := make([]mihomoapi.ProviderStat, 0, len(names))
	for _, n := range names {
		st, _ := api.Provider(n) // a single subscription fault must not affect the others' display
		if st.Name == "" {
			st.Name = n
		}
		stats = append(stats, st)
	}
	if asJSON {
		b, _ := json.Marshal(stats)
		fmt.Println(string(b))
		return nil
	}
	fmt.Printf("%-16s %-8s %-6s %s\n", "NAME", "STATE", "NODES", "ERROR")
	for _, st := range stats {
		state := "✅"
		if st.Error != "" || st.Nodes == 0 {
			state = "⚠️"
		}
		fmt.Printf("%-16s %-8s %-6d %s\n", st.Name, state, st.Nodes, st.Error)
	}
	return nil
}

func isTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func readLine() (string, error) {
	r := bufio.NewReader(os.Stdin)
	s, err := r.ReadString('\n')
	return strings.TrimSpace(s), err
}

// promptSubURL is the paste mode used when no URL argument is given: read a whole line (URLs containing & ? need no quotes), empty input returns a usage error.
func promptSubURL(usage string) (string, error) {
	if isTTY() {
		fmt.Fprint(os.Stderr, "please paste the subscription link (paste the whole line then press enter, no quotes needed): ")
	}
	line, _ := readLine()
	if line == "" {
		return "", fmt.Errorf("usage: %s", usage)
	}
	return line, nil
}

// collectNodeNames aggregates the node names of all providers (including this import's body), for pruning derived groups.
// Must cover every subscription: a region/type group is kept if any subscription hits it, to avoid accidental removal.
func collectNodeNames(p paths.Paths, newBody []byte, newName string) []string {
	seen := map[string]bool{}
	add := func(b []byte) {
		if ns, err := subscribe.NodeNames(b); err == nil {
			for _, n := range ns {
				seen[n] = true
			}
		}
	}
	add(newBody)
	if entries, err := os.ReadDir(p.Proxies); err == nil {
		for _, ent := range entries {
			if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".yaml") || ent.Name() == newName+".yaml" {
				continue
			}
			if b, err := os.ReadFile(filepath.Join(p.Proxies, ent.Name())); err == nil {
				add(b)
			}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	return names
}

var errLineRe = regexp.MustCompile(`level=(error|fatal) msg="([^"]*)"`)

// firstErrLine extracts the first error from mihomo output (a lesson from the bash era: errors are on stdout).
func firstErrLine(out string) string {
	if m := errLineRe.FindStringSubmatch(out); m != nil {
		return m[2]
	}
	if len(out) > 160 {
		return out[len(out)-160:]
	}
	return out
}

// rollbackRestart is the transaction-failure fallback: restore the config backup and restart the service, returning the kernel to its pre-transaction state.
func rollbackRestart(p paths.Paths) {
	config.Restore(p.Conf)
	systemdunit.Restart()
}
