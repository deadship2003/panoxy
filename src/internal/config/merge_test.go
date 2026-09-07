package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deadship2003/panoxy/internal/asset"
	"github.com/deadship2003/panoxy/internal/constants"
	"github.com/deadship2003/panoxy/internal/core"
)

// personalSample emulates a personal config: custom groups (process/geo routing),
// self-hosted nodes, ports/secret, another subscription, a rule provider — every input
// shape merge-conf deals with.
const personalSample = `mixed-port: 7897
port: 18080
socks-port: 10808
secret: mysecret
external-controller: 127.0.0.1:19090

# my self-hosted nodes
proxies:
  - name: "家庭VPS"
    type: vmess
    server: 1.2.3.4
    port: 443
    uuid: aaaabbbb-cccc-dddd-eeee-ffff00001111
    alterId: 0
    cipher: auto
  - name: "公司出口"
    type: socks5
    server: 5.6.7.8
    port: 1080

proxy-providers:
  mine2:
    type: http
    url: "https://other-airport.example/sub2"
    path: ./proxies/mine2.yaml
    interval: 86400

proxy-groups:
  - name: 我的分组
    type: select
    proxies: [家庭VPS, 公司出口]
    use: [mine2]
  - name: 进程分流
    type: select
    proxies: [我的分组, DIRECT]

rule-providers:
  my-reject:
    type: http
    behavior: domain
    format: yaml
    url: "https://example.com/reject.yaml"
    interval: 86400

rules:
  - PROCESS-NAME,ssh,DIRECT
  - RULE-SET,my-reject,REJECT
  - GEOIP,CN,DIRECT
  - MATCH,我的分组
`

func mergeSetup(t *testing.T) (*Editor, *Editor, string) {
	t.Helper()
	// base: the template + an already-imported subscription Nano (the shape after sub import)
	out, err := asset.RenderConfig(asset.DefaultConfigData())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	baseP := filepath.Join(dir, "base.yaml")
	os.WriteFile(baseP, []byte(out), 0o644)
	base, err := Load(baseP)
	if err != nil {
		t.Fatal(err)
	}
	if err := base.SetProvider("Nano", "https://nano.example/sub", "./proxies/Nano.yaml"); err != nil {
		t.Fatal(err)
	}
	base.WireProvider("Nano", true, nil)
	base.Save()

	perP := filepath.Join(dir, "personal.yaml")
	os.WriteFile(perP, []byte(personalSample), 0o644)
	per, err := Load(perP)
	if err != nil {
		t.Fatal(err)
	}
	return base, per, dir
}

func TestMergePersonalDecisionTable(t *testing.T) {
	base, per, dir := mergeSetup(t)
	rep, err := base.MergePersonal(per, MergeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	provs := base.Providers() // taken after the merge (the placeholder has retired; real subscription names stay)
	base.WireAfterMerge(provs, rep.PersonalProxies, MergeOpts{})
	base.SetPath(filepath.Join(dir, "merged.yaml"))
	base.Save()
	s := string(mustRead(t, filepath.Join(dir, "merged.yaml")))

	// taken over (personal)
	for _, want := range []string{
		"mixed-port: 7897", "port: 18080", "socks-port: 10808",
		"secret: mysecret", "127.0.0.1:19090",
		"name: 我的分组", "PROCESS-NAME,ssh,DIRECT", "家庭VPS",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("not taken over: %q", want)
		}
	}
	// kept (base marks/infrastructure)
	for _, want := range []string{"routing-mark: 6666", `listen: "[::]:1053"`, "stack: system", "ntp.aliyun.com"} {
		if !strings.Contains(s, want) {
			t.Errorf("base value not kept: %q", want)
		}
	}
	// merged: providers (Nano kept + mine2 added)
	if !strings.Contains(s, "mine2:") || !strings.Contains(s, "Nano:") {
		t.Error("subscription merge mismatch")
	}
	// auto adjustment: process rules -> strict
	if !strings.Contains(s, "find-process-mode: strict") {
		t.Error("process rule did not trigger find-process-mode=strict")
	}
	// wiring: Nano appended into the personal group; the placeholder SUB must have
	// retired (a real subscription is in place). Additive merge: base groups kept
	// (including Nano in their use), personal groups appended.
	if !strings.Contains(s, "use: [mine2, Nano]") && !strings.Contains(s, "use: [SUB, Nano, mine2]") {
		// the personal group's use has mine2, the base groups' use has Nano; both coexist after the overlay
		hasNano := strings.Contains(s, "Nano")
		hasMine2 := strings.Contains(s, "mine2")
		if !hasNano || !hasMine2 {
			t.Errorf("both the base and personal subscriptions should exist: Nano=%v mine2=%v", hasNano, hasMine2)
		}
	}
	if got := base.Providers(); strings.Contains(strings.Join(got, ","), "SUB") {
		t.Errorf("the placeholder subscription should retire, current: %v", got)
	}
	if strings.Contains(s, `url: "SUB_URL_PLACEHOLDER"`) {
		t.Error("placeholder subscription URL residue")
	}
	// personal proxies appended into groups that have a proxies list (at the end, defaults unchanged)
	if !strings.Contains(s, "proxies: [家庭VPS, 公司出口]") {
		t.Errorf("the personal group's proxies list was broken (should stay as-is; appends dedupe)")
	}
	// additive-merge verification: base groups kept + same-name merged + new appended
	if !strings.Contains(s, "name: DNS") {
		t.Error("the base DNS group should be kept (additive merge never deletes base groups)")
	}
	if !strings.Contains(s, "🚀 节点选择") {
		t.Error("the base 🚀 节点选择 group should be kept (additive merge)")
	}
	if !strings.Contains(s, "name: 我的分组") {
		t.Error("the personal new group should be appended")
	}
	// rules: personal first + base fallback
	if !strings.Contains(s, "PROCESS-NAME,ssh,DIRECT") {
		t.Error("the personal process rule should come first")
	}
	if !strings.Contains(s, "GEOSITE,TikTok,🎵 TikTok") {
		t.Error("the base rules should be kept as fallback")
	}
	// MATCH should be last
	rulesStart := strings.Index(s, "rules:")
	matchIdx := strings.LastIndex(s, "MATCH,🌐 其他")
	if rulesStart < 0 || matchIdx < rulesStart {
		t.Error("the MATCH rule should exist")
	}
	// the &p anchor is kept
	if !strings.Contains(s, "p: &p") {
		t.Error("the &p anchor should be kept (sub import depends on it)")
	}
}

func subSnippet(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, "use:") {
			return l
		}
	}
	return ""
}

// TestMergedConfigPassesCheck runs the merge product through the in-process kernel -t
// (equivalent to the external mihomo -t).
func TestMergedConfigPassesCheck(t *testing.T) {
	geoSrc := geoFallback(t)
	base, per, dir := mergeSetup(t)
	rep, _ := base.MergePersonal(per, MergeOpts{})
	base.WireAfterMerge(base.Providers(), rep.PersonalProxies, MergeOpts{})
	merged := filepath.Join(dir, "merged.yaml")
	base.SetPath(merged)
	base.Save()
	// geo in place
	for _, f := range []string{"GeoIP.dat", "GeoSite.dat", "Country.mmdb"} {
		if b, err := os.ReadFile(filepath.Join(geoSrc, f)); err == nil {
			os.WriteFile(filepath.Join(dir, f), b, 0o644)
		}
	}
	os.MkdirAll(filepath.Join(dir, "ui", "official"), 0o755)
	if err := core.Validate(dir, mustRead(t, merged)); err != nil {
		t.Errorf("the merged product failed -t: %v", err)
	}
}

func geoFallback(t *testing.T) string {
	t.Helper()
	for _, c := range []string{
		filepath.Join("/opt", constants.ProgName),
		"/opt/panixy", // legacy leftover name
		os.Getenv("GEO_SRC"),
	} {
		if c == "" {
			continue
		}
		if st, err := os.Stat(filepath.Join(c, "GeoSite.dat")); err == nil && st != nil {
			return c
		}
	}
	if h, _ := os.UserHomeDir(); h != "" {
		if _, err := os.Stat(h + "/panoxy-e2e/GeoSite.dat"); err == nil {
			return h + "/panoxy-e2e"
		}
	}
	t.Skip("no geodata on this machine (GeoSite.dat); skipping the in-process -t verification")
	return ""
}
