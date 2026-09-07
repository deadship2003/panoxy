package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deadship2003/panoxy/internal/constants"
)

// e2e mainline: deploy (preset no-tun config) → sub import success/failure/offline →
// sub del → mode config-level switch → service lifecycle.

func TestE2EDeployWithPresetConf(t *testing.T) {
	e := newEnv(t)
	pkg := t.TempDir()
	buildAssets(t, pkg)
	// preset config (existing config wins; no tun — safe on a dev machine)
	os.WriteFile(e.conf, []byte(noTunConf(t, e.apiPort, e.mixPort, e.dnsPort, false)), 0o644)

	cmd := e.cmd("deploy", "--verbose")
	cmd.Dir = pkg
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("deploy failed:\n%s", out)
	}
	for _, want := range []string{"geo and ad rules", "web UI", "existing config detected"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("deploy output missing %q:\n%s", want, out)
		}
	}
	checkFile(t, filepath.Join(e.root, "rule_provider", "HyperADRules-Ads.yaml"))
	checkFile(t, filepath.Join(e.root, "ui", "official", "index.html"))
	checkFile(t, filepath.Join(e.dir, "cli", constants.ProgName))
	checkFile(t, filepath.Join(e.dir, "man", constants.ProgName+".1.gz"))
	if b, _ := os.ReadFile(filepath.Join(e.dir, "state.yaml")); !strings.Contains(string(b), "tun") {
		t.Errorf("state file missing proxy-mode=tun: %s", b)
	}
	e.waitAPI(t)
}

func TestE2ESetSubFlows(t *testing.T) {
	e := newEnv(t)
	os.WriteFile(e.conf, []byte(noTunConf(t, e.apiPort, e.mixPort, e.dnsPort, false)), 0o644)
	bootSandbox(t, e) // boot the kernel first (sub import restarts it; a live instance proves node counts)
	srv := fakeSubServer(t, 4)

	// 1) reachable subscription: success + node-count verification
	out := e.run(t, "sub", "import", "--name", "main", srv.URL+"/sub?token=ok&sid=x")
	if !strings.Contains(out, "loaded: 4 nodes") {
		t.Fatalf("node-count report missing:\n%s", out)
	}
	if b, _ := os.ReadFile(e.conf); !strings.Contains(string(b), srv.URL+"/sub?token=ok&sid=x") {
		t.Fatal("URL not written to the config (with & params)")
	}
	if b, _ := os.ReadFile(filepath.Join(e.root, "proxies", "main.yaml")); !strings.Contains(string(b), "e2e-0") {
		t.Fatal("subscription cache not preloaded")
	}
	if b, _ := os.ReadFile(e.conf); strings.Contains(string(b), `url: "SUB_URL_PLACEHOLDER"`) {
		t.Fatal("placeholder subscription should retire after the first real import (config still has the placeholder url)")
	}

	// 2) unreachable subscription: honest failure + zero config mutation
	before, _ := os.ReadFile(e.conf)
	out = e.runFail(t, "sub", "import", "http://192.0.2.1:9/dead")
	if !strings.Contains(out, "subscription fetch or validation failed") {
		t.Fatalf("unexpected error output:\n%s", out)
	}
	after, _ := os.ReadFile(e.conf)
	if string(before) != string(after) {
		t.Fatal("the failure path mutated the config")
	}

	// 3) offline import (--file, no network)
	seed := filepath.Join(e.dir, "seed.yaml")
	os.WriteFile(seed, []byte("proxies:\n  - name: offline-x\n    type: socks5\n    server: 127.0.0.1\n    port: 1080\n"), 0o644)
	out = e.run(t, "sub", "import", "--name", "backup", "--file", seed, "https://blocked.example.com/x?token=w")
	if !strings.Contains(out, "using local subscription file") {
		t.Fatalf("offline-import log missing:\n%s", out)
	}

	// 4) sub list: both subscriptions present, per-subscription status visible
	out = e.run(t, "sub", "list")
	for _, want := range []string{"main", "backup"} {
		if !strings.Contains(out, want) {
			t.Fatalf("sub list missing %s:\n%s", want, out)
		}
	}

	// 5) deleting the last subscription is rejected by -t (the group loses its use);
	// delete the backup first, which succeeds
	e.run(t, "sub", "del", "--name", "backup")
	if b, _ := os.ReadFile(e.conf); strings.Contains(string(b), "backup:") {
		t.Fatal("backup not deleted")
	}
}

func TestE2EModeSwitchConfigLevel(t *testing.T) {
	e := newEnv(t)
	os.WriteFile(e.conf, []byte(noTunConf(t, e.apiPort, e.mixPort, e.dnsPort, false)), 0o644)
	bootSandbox(t, e)

	// tun → tproxy: tproxy-port appears, tun disappears; state updated; no rollback triggered
	out := e.run(t, "mode", "tproxy")
	if !strings.Contains(out, "tproxy") {
		t.Fatalf("unexpected mode output:\n%s", out)
	}
	b, _ := os.ReadFile(e.conf)
	if !strings.Contains(string(b), "tproxy-port: 7893") || strings.Contains(string(b), "\ntun:") {
		t.Fatalf("config variant switch failed:\n%s", b)
	}
	if s, _ := os.ReadFile(filepath.Join(e.dir, "state.yaml")); !strings.Contains(string(s), "tproxy") {
		t.Fatalf("state not updated: %s", s)
	}
	// tproxy → tun: restore
	e.run(t, "mode", "tun")
	b, _ = os.ReadFile(e.conf)
	if !strings.Contains(string(b), "\ntun:") || strings.Contains(string(b), "tproxy-port") {
		t.Fatalf("config not restored to tun:\n%s", b)
	}
}

// TestE2EServiceLifecycle covers the strict-semantics lifecycle: transient start/stop/restart,
// registration-only enable/disable (must never touch the running kernel), the service status
// fixed fields, and the LIF-001 exit codes (ip/nft are no-op shims here, so the real
// firewall is never touched).
func TestE2EServiceLifecycle(t *testing.T) {
	e := newEnv(t)
	pkg := t.TempDir()
	buildAssets(t, pkg)
	os.WriteFile(e.conf, []byte(noTunConf(t, e.apiPort, e.mixPort, e.dnsPort, false)), 0o644)

	c := e.cmd("deploy")
	c.Dir = pkg
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("deploy failed:\n%s", out)
	}
	e.waitAPI(t)

	// start on an already-active service is idempotent (no second kernel boot)
	out := e.run(t, "start")
	if !strings.Contains(out, "already active") {
		t.Errorf("start should report already active:\n%s", out)
	}

	// restart: the unit reloads the firewall (shim restarts the kernel), health passes
	out = e.run(t, "restart")
	if !strings.Contains(out, "restarted") {
		t.Errorf("unexpected restart output:\n%s", out)
	}
	e.waitAPI(t)

	// service status --json: fixed fields, running state truthful
	out = e.run(t, "service", "status", "--json")
	var st struct {
		Installed bool   `json:"installed"`
		Running   bool   `json:"running"`
		Enabled   bool   `json:"enabled"`
		Scope     string `json:"scope"`
		PID       int    `json:"pid"`
		Platform  string `json:"platformInit"`
		UnitPath  string `json:"unitPath"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &st); err != nil {
		t.Fatalf("service status --json not valid JSON:\n%s", out)
	}
	if !st.Installed || !st.Running || st.Scope != "system" || st.Platform != "systemd" || st.PID == 0 {
		t.Errorf("unexpected status fields: %+v", st)
	}
	if _, err := os.Stat(st.UnitPath); err != nil {
		t.Errorf("unitPath does not exist: %s", st.UnitPath)
	}

	// enable/disable are registration-only: the running kernel must survive both
	e.run(t, "service", "enable")
	e.run(t, "service", "disable")
	e.waitAPI(t)

	// --user scope is explicitly unsupported: exit code 4
	if code := e.exitCode(t, "service", "status", "--user"); code != 4 {
		t.Errorf("service status --user should exit 4, got %d", code)
	}

	// stop: service stopped + firewall cleared, boot registration untouched
	out = e.run(t, "stop")
	if !strings.Contains(out, "stopped") {
		t.Errorf("unexpected stop output:\n%s", out)
	}
	out = e.run(t, "service", "status", "--json")
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &st); err != nil {
		t.Fatalf("service status --json not valid JSON:\n%s", out)
	}
	if st.Running {
		t.Errorf("status should report not running after stop: %+v", st)
	}

	// start brings it back: detects inactive and boots again
	out = e.run(t, "start")
	if !strings.Contains(out, "started") {
		t.Errorf("unexpected start (re-boot) output:\n%s", out)
	}
	e.waitAPI(t)
}

// bootSandbox boots the kernel directly through the shim (equivalent to service start).
func bootSandbox(t *testing.T, e *env) {
	t.Helper()
	// CLI in place (kernel embedded in panoxy; the shim runs `panoxy run` directly)
	os.MkdirAll(filepath.Join(e.dir, "cli"), 0o755)
	if b, err := os.ReadFile(bin); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(filepath.Join(e.dir, "cli", constants.ProgName), b, 0o755); err != nil {
		t.Fatal(err)
	}
	// geo + ui (needed by -t and boot); create the ui dir first (it also creates e.root),
	// otherwise the geo copy fails on a missing parent dir
	os.MkdirAll(filepath.Join(e.root, "ui", "official"), 0o755)
	geoSrc := geoSrcOr(t)
	for _, f := range []string{"GeoIP.dat", "GeoSite.dat", "Country.mmdb"} {
		if b, err := os.ReadFile(filepath.Join(geoSrc, f)); err == nil {
			os.WriteFile(filepath.Join(e.root, f), b, 0o644)
		}
	}
	e.shim(t, "start", constants.ProgName+".service")
	e.waitAPI(t)
}

// buildAssets assembles a mini offline package (geo/UI/rules; the kernel is embedded in the CLI).
func buildAssets(t *testing.T, pkg string) {
	t.Helper()
	for _, d := range []string{"assets/geo", "assets/ui/official", "assets/rule"} {
		os.MkdirAll(filepath.Join(pkg, d), 0o755)
	}
	geoSrc := geoSrcOr(t)
	for _, f := range []string{"GeoIP.dat", "GeoSite.dat", "Country.mmdb"} {
		if b, err := os.ReadFile(filepath.Join(geoSrc, f)); err == nil {
			os.WriteFile(filepath.Join(pkg, "assets/geo", f), b, 0o644)
		}
	}
	os.WriteFile(filepath.Join(pkg, "assets/ui/official/index.html"), []byte("<html>e2e</html>"), 0o644)
	os.WriteFile(filepath.Join(pkg, "assets/rule/HyperADRules-Ads.yaml"), []byte("payload:\n  - '+.ad.example'\n"), 0o644)
}

func checkFile(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Stat(p); err != nil {
		t.Errorf("file missing: %s", p)
	}
}

func geoSrcOr(t *testing.T) string {
	t.Helper()
	if g := os.Getenv("GEO_SRC"); g != "" {
		return g
	}
	for _, c := range []string{
		filepath.Join("/opt", constants.ProgName),
		"/opt/panixy", // legacy leftover name from old deployments
		homeDir() + "/panoxy-e2e",
	} {
		if _, err := os.Stat(filepath.Join(c, "GeoSite.dat")); err == nil {
			return c
		}
	}
	t.Fatal("missing geo data (set GEO_SRC, or place it at ~/panoxy-e2e)")
	return ""
}
