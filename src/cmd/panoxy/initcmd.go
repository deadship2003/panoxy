package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/deadship2003/panoxy/internal/asset"
	"github.com/deadship2003/panoxy/internal/constants"
	"github.com/deadship2003/panoxy/internal/logx"
	"github.com/deadship2003/panoxy/internal/mihomoapi"
	"github.com/deadship2003/panoxy/internal/paths"
	"github.com/deadship2003/panoxy/internal/statemode"
	"github.com/deadship2003/panoxy/internal/subscribe"
	"github.com/deadship2003/panoxy/internal/systemdunit"
	"github.com/deadship2003/panoxy/internal/upgrade"
)

// runInit: no packaging, single-binary bare-metal init — downloads all assets and deploys by itself, then imports the subscription.
// Three-tier download strategy: direct (15s hard cap) > subscription bootstrap proxy (needs the panoxy CLI on this machine) > gh mirror.
func runInit(cmd *cobra.Command, args []string) error {
	if dry, _ := cmd.Flags().GetBool("dry-run"); dry {
		return initDryRun(cmd, args)
	}
	return withRootLock(func(p paths.Paths) error { return runInitBody(p, cmd, args) })
}

func runInitBody(p paths.Paths, cmd *cobra.Command, args []string) error {
	total := 8
	stepf := func(i int, f string, a ...any) { logx.Step("[%d/%d] %s", i, total, fmt.Sprintf(f, a...)) }

	// The subscription bootstrap proxy (started lazily during downloads) must die on EVERY
	// exit path — success or failure. A leaked temp kernel keeps holding ports and blocks a
	// later retry (LIF-001 single-instance red line). bootProxyStop is a no-op when the
	// proxy was never started.
	defer func() {
		if bp := bootProxyAddr(); bp != "" {
			bootProxyStop()
			logx.Info("bootstrap proxy cleaned up")
		}
	}()

	name, _ := cmd.Flags().GetString("name")
	file, _ := cmd.Flags().GetString("file")
	mode, _ := cmd.Flags().GetString("proxy-mode")
	secret, _ := cmd.Flags().GetString("secret")
	mirrors, _ := cmd.Flags().GetStringSlice("mirror")
	if err := subscribe.CheckName(name); err != nil {
		return err
	}
	var url string
	var err error
	if len(args) > 0 {
		url = args[0]
	}

	stepf(1, "environment precheck (root/systemd/arch/legacy residue)")
	if runtimeArch() == "" {
		return fmt.Errorf("unsupported architecture (only amd64/arm64)")
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("systemd required")
	}
	if legacy := systemdunit.DetectLegacy(p); legacy != "" {
		return fmt.Errorf("bash legacy deployment residue detected: %s (first sudo %s uninstall and delete %s, see the README migration section)", legacy, constants.ProgName, constants.DefConfPath)
	}

	stepf(2, "fetch subscription content (%s)", orURL(url, file))
	var body []byte
	if file != "" {
		if body, err = subscribe.ValidateFile(file); err != nil {
			return err
		}
	} else {
		if url == "" {
			if url, err = promptSubURL(constants.ProgName + " init [subscription-URL] [--file local-subscription-file]"); err != nil {
				return err
			}
		}
		if err := subscribe.CheckURL(url); err != nil {
			return err
		}
		api := mihomoapi.NewFromConf(p.Conf)
		if body, err = fetchSubBody(url, api); err != nil {
			return fmt.Errorf("subscription fetch failed: %v (the subscription must be directly reachable; or download it on any device and import offline with --file)", err)
		}
	}

	stepf(3, "network probe: direct GitHub release assets (15s hard cap)")
	mirrorList := mirrors
	// The probe must hit a real asset URL: in CN environments the github.com homepage often works while
	// the releases asset domain (objects.githubusercontent) is blocked.
	probeURL := "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geosite.dat"
	allowDirect := directAssetReachable(probeURL, 15*time.Second)
	if allowDirect {
		logx.Info("direct connection works, downloading directly")
	} else {
		logx.Info("direct connection unavailable (asset domain unreachable), downloads will go through the subscription bootstrap proxy / mirror")
	}
	// Lazy bootstrap proxy: only starts when the first download that needs it happens (using the fetched subscription body).
	proxyOnce := false
	proxyFn := func() string {
		if !proxyOnce {
			proxyOnce = true
			bootProxyFromSub(body) // sets bootProxyAddr()
		}
		return bootProxyAddr()
	}

	tmp, _ := os.MkdirTemp("", constants.ProgName+"-init-")
	defer os.RemoveAll(tmp)

	stepf(4, "download geo data and ad rules")
	geoBase := "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest"
	geos := map[string]string{
		"GeoIP.dat":    geoBase + "/geoip.dat",
		"GeoSite.dat":  geoBase + "/geosite.dat",
		"Country.mmdb": geoBase + "/country.mmdb",
	}
	geoOK := 0
	for f, u := range geos {
		if downloadAny(u, allowDirect, proxyFn, mirrorList, filepath.Join(tmp, f), "geo "+f) {
			geoOK++
		}
	}
	ruleURL := "https://github.com/Lynricsy/HyperADRules/releases/latest/download/hyper_adrules_ads_clash.yaml"
	haveRule := downloadAny(ruleURL, allowDirect, proxyFn, mirrorList, filepath.Join(tmp, "HyperADRules-Ads.yaml"), "ad rules")
	if geoOK < 3 {
		return fmt.Errorf("geo data download incomplete (%d/3)", geoOK)
	}

	stepf(5, "download metacubexd web UI")
	uiOK := downloadAny("https://github.com/MetaCubeX/metacubexd/releases/latest/download/compressed-dist.tgz",
		allowDirect, proxyFn, mirrorList, filepath.Join(tmp, "ui.tgz"), "web UI")

	// ---- assets are all ready now; from here on it is structurally identical to deploy ----
	snap := snapshot(p)
	stepf(6, "place assets in %s + render config (secret %s)", constants.DefRootDir, secret)
	for f := range geos {
		copyFile(filepath.Join(tmp, f), filepath.Join(p.Root, f))
	}
	os.MkdirAll(p.RuleProv, 0o755)
	if haveRule {
		copyFile(filepath.Join(tmp, "HyperADRules-Ads.yaml"), filepath.Join(p.RuleProv, "HyperADRules-Ads.yaml"))
	}
	if uiOK {
		os.MkdirAll(p.UiDir, 0o755)
		if err := runExtractTgz(filepath.Join(tmp, "ui.tgz"), p.UiDir); err != nil {
			logx.Warn("UI unpack failed (%s), skipping UI", firstLineOf(err.Error()))
		} else {
			os.WriteFile(p.UiStamp, []byte("unknown\n"), 0o644)
		}
	}
	if _, err := os.Stat(p.Conf); err == nil {
		logx.Info("existing config detected, kept untouched: %s (group rules and custom parameters are all inherited)", p.Conf)
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
	}
	// Always write the clean default-template copy (config.default.yaml, merge-conf's baseline)
	// to the data dir, whether or not a config already existed.
	if err := writeDefaultConf(p, mode, secret); err != nil {
		return err
	}
	self, _ := os.Executable()
	self, _ = filepath.EvalSymlinks(self)
	copyBinary(self, p.Cli)
	installMan(p.ManGz, self)
	statemode.Write(p.State, statemode.State{ProxyMode: mode})

	stepf(7, "deploy service (units/firewall/health verification)")
	if err := runInstall(cmd, args); err != nil {
		deployRollback(p, snap)
		return err
	}

	stepf(8, "import subscription (%s)", name)
	subFile := filepath.Join(tmp, "sub.yaml")
	os.WriteFile(subFile, body, 0o644)
	setCmd := &cobra.Command{}
	setCmd.Flags().String("name", name, "")
	setCmd.Flags().String("file", subFile, "")
	setCmd.Flags().StringSlice("group", nil, "")
	if err := runSubImport(setCmd, []string{url}); err != nil {
		return fmt.Errorf("subscription import failed: %v (assets and service are ready, you can retry later with sudo %s sub import)", err, constants.ProgName)
	}
	logx.Info("init complete: %s status to check health; web UI http://<host-IP>:%d/ui/ (secret %s)", constants.ProgName, constants.ApiPortDef, secret)
	return nil
}

// ---- helpers ----

func orURL(u, f string) string {
	if f != "" {
		return "local file " + f
	}
	if u != "" {
		return u
	}
	return "paste mode"
}

// directAssetReachable probes a real release asset with a Range request (fetch 1 byte only, 15s hard cap).
func directAssetReachable(u string, timeout time.Duration) bool {
	hc := &http.Client{Timeout: timeout}
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("Range", "bytes=0-0")
	resp, err := hc.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode < 400
}

// downloadAny tries in order: direct (only when the probe passed, to avoid burning another 15s) → subscription proxy (lazy start) → mirror prefixes.
func downloadAny(url string, allowDirect bool, proxyFn func() string, mirrors []string, dst, label string) bool {
	if allowDirect {
		if err := upgrade.DownloadProgress(url, "", dst, label); err == nil {
			return true
		}
	}
	if p := proxyFn(); p != "" {
		if err := upgrade.DownloadProgress(url, p, dst, label); err == nil {
			return true
		}
	}
	for _, m := range mirrors {
		m = strings.TrimRight(m, "/") + "/" + url
		if err := upgrade.DownloadProgress(m, "", dst, label+"(mirror)"); err == nil {
			logx.Info("%s downloaded via mirror (mirror is a third-party source)", label)
			return true
		}
	}
	return false
}

// bootProxyAddr / bootProxyStop / bootProxyFromSub subscription bootstrap proxy (package-level state).
var bootProxyDir, bootProxyPort = "", ""

func bootProxyAddr() string {
	if bootProxyDir == "" {
		return ""
	}
	return "http://127.0.0.1:" + bootProxyPort
}

func bootProxyStop() {
	if bootProxyDir == "" {
		return
	}
	if b, err := os.ReadFile(filepath.Join(bootProxyDir, "pid")); err == nil {
		for _, l := range strings.Fields(string(b)) {
			syscallKill(l)
		}
	}
	os.RemoveAll(bootProxyDir)
	bootProxyDir = ""
}

func syscallKill(pid string) {
	var p int
	if n, _ := fmt.Sscanf(pid, "%d", &p); n == 1 && p > 1 {
		syscall.Kill(p, syscall.SIGTERM)
	}
}

// bootProxyFromSub starts a temporary bootstrap proxy using the fetched subscription body. After fusion the
// kernel is embedded in the CLI, so it boots `<prog> run` in a subprocess (temp dir as the data home) rather
// than a separate mihomo binary.
func bootProxyFromSub(body []byte) {
	bootBin := paths.Get().Cli // the installed CLI (embedded kernel); follows --root/env
	if _, err := os.Stat(bootBin); err != nil {
		bootBin, _ = os.Executable() // fall back to the running binary (bare-metal init)
	}
	if _, err := os.Stat(bootBin); err != nil {
		// No CLI binary: cannot start a bootstrap proxy via a subscription node. Print clear guidance then skip, falling back to mirror/offline package.
		logx.Step("no %s CLI (%s), cannot download assets via a subscription node", constants.ProgName, bootBin)
		logx.Step("  option 1 (recommended): run make package on a machine with internet to build an offline package → then sudo ./%s deploy on the target", constants.ProgName)
		logx.Step("  option 2: manually place %s at %s and chmod +x, then rerun init", constants.ProgName, bootBin)
		return
	}
	port := freePortStr()
	dir, _ := os.MkdirTemp("", constants.ProgName+"-boot-")
	conf := fmt.Sprintf(`mixed-port: %s
mode: rule
log-level: warning
proxy-providers:
  boot:
    type: file
    path: ./boot.sub.yaml
    health-check: {enable: false}
proxy-groups:
  - {name: P, type: select, use: [boot]}
rules:
  - MATCH,P
`, port)
	os.WriteFile(filepath.Join(dir, "boot.yaml"), []byte(conf), 0o644)
	// The bootstrap kernel likewise only accepts Clash YAML: normalize non-Clash formats (sing-box/Surge/base64-Clash) first,
	// otherwise the bootstrap proxy won't start and all subsequent asset downloads will fail.
	bootBody, _, err := subscribe.Normalize(body)
	if err != nil {
		bootBody = body // fall back to the original on normalization failure, letting mihomo report its own error (consistent with the real import path)
	}
	os.WriteFile(filepath.Join(dir, "boot.sub.yaml"), bootBody, 0o644)
	// Boot the embedded kernel via `panoxy run`; the temp dir is the data home, the temp config the source.
	// INVOCATION_ID marks this as a deliberately spawned kernel (different ports, temp data
	// home) so the run command's single-instance guard does not treat it as a stray duplicate.
	c := exec.Command(bootBin, "run")
	c.Dir = dir
	c.Env = append(os.Environ(),
		"INVOCATION_ID=boot",
		constants.EnvPrefix()+"_ROOT="+dir,
		constants.EnvPrefix()+"_CONF="+filepath.Join(dir, "boot.yaml"))
	if err := c.Start(); err != nil {
		os.RemoveAll(dir)
		return
	}
	os.WriteFile(filepath.Join(dir, "pid"), []byte(fmt.Sprint(c.Process.Pid)), 0o644)
	bootProxyDir, bootProxyPort = dir, port
	// Wait for readiness (15s).
	for i := 0; i < 15; i++ {
		hc := &http.Client{Timeout: 3 * time.Second}
		pu, _ := url.Parse("http://127.0.0.1:" + port)
		hc.Transport = &http.Transport{Proxy: http.ProxyURL(pu)}
		req, _ := http.NewRequest("GET", "https://www.gstatic.com/generate_204", nil)
		if resp, err := hc.Do(req); err == nil {
			resp.Body.Close()
			logx.Info("subscription bootstrap proxy ready (127.0.0.1:%s, temp kernel pid %d)", port, c.Process.Pid)
			return
		}
		time.Sleep(time.Second)
	}
	bootProxyStop()
}

func freePortStr() string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "33999"
	}
	defer l.Close()
	return fmt.Sprint(l.Addr().(*net.TCPAddr).Port)
}

// fetchSubBody fetches a subscription: direct first, falling back to the local mixed-port proxy (upgrade-subscription scenario).
func fetchSubBody(u string, api *mihomoapi.Client) ([]byte, error) {
	var buf bytes.Buffer
	if err := subscribe.Fetch(u, api.Proxy(), subscribe.UA(), &buf); err != nil {
		return nil, err
	}
	if err := subscribe.Validate(buf.Bytes()); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// initDryRun is a dry-run: environment checks + download strategy decision + placement list + config render preview.
// No downloads, no writes, no root needed; for a full sandbox run use panoxy try.
func initDryRun(cmd *cobra.Command, args []string) error {
	p := paths.Get()
	logx.Info("== init --dry-run (dry-run mode, does not execute) ==")
	logx.Info("target dir: %s (changeable via --root; config stays system-level %s)", p.Root, p.Conf)
	logx.Info("CLI/man: %s / %s", p.Cli, p.ManGz)

	logx.Step("[precheck] environment")
	arch := runtimeArch()
	if arch == "" {
		return fmt.Errorf("unsupported architecture (only amd64/arm64)")
	}
	logx.Info("  arch %s ✓  systemd:%s", arch, orOK(exists("/usr/bin/systemctl") || exists("/bin/systemctl")))
	if legacy := systemdunit.DetectLegacy(p); legacy != "" {
		logx.Warn("  ⚠️ bash legacy residue detected: %s (a real install would be aborted, clean up first)", legacy)
	} else {
		logx.Info("  legacy residue: none ✓")
	}
	if _, err := os.Stat(p.Conf); err == nil {
		logx.Info("  existing config: present, will be inherited as-is (groups/custom parameters untouched)")
	} else {
		logx.Info("  existing config: none, will render the default template (secret %s, ports 33833/9966/6699/9999)", drySecret(cmd))
	}

	logx.Step("[precheck] download strategy (probe the real asset domain, 15s hard cap)")
	probe := "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geosite.dat"
	if directAssetReachable(probe, 15*time.Second) {
		logx.Info("  direct works → download directly")
	} else {
		logx.Info("  direct unavailable → subscription bootstrap proxy (needs the %s CLI on this machine: %s)%s",
			constants.ProgName, orOK(exists(paths.Get().Cli)), " → mirror (--mirror)")
	}

	logx.Step("[plan] download list")
	logx.Info("  geo:  GeoIP.dat / GeoSite.dat / Country.mmdb")
	logx.Info("  rules: HyperADRules-Ads.yaml   web UI: metacubexd compressed-dist.tgz")

	logx.Step("[plan] config render preview (stdout)")
	d := asset.DefaultConfigData()
	d.TProxy = modeOf(cmd) == "tproxy"
	d.Secret = drySecret(cmd)
	out, err := asset.RenderConfig(d)
	if err != nil {
		return err
	}
	fmt.Print(out)
	logx.Info("== dry-run done. Real run: sudo %s init ...; full sandbox run: %s try ...", constants.ProgName, constants.ProgName)
	return nil
}

func drySecret(cmd *cobra.Command) string {
	s, _ := cmd.Flags().GetString("secret")
	return s
}
func modeOf(cmd *cobra.Command) string {
	m, _ := cmd.Flags().GetString("proxy-mode")
	return m
}
func orOK(ok bool) string {
	if ok {
		return "available ✓"
	}
	return "missing"
}
