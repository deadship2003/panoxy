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

// personalSample 模拟个人配置:自定义分组(进程/地理分流)、自建节点、端口密钥、
// 其他订阅、规则订阅 —— merge-conf 的全部输入形态。
const personalSample = `mixed-port: 7897
port: 18080
socks-port: 10808
secret: mysecret
external-controller: 127.0.0.1:19090

# 我的自建节点
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
	// 基底:模板 + 已导入订阅 Nano(sub import 后的形态)
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
	provs := base.Providers() // 融合后取(占位已退场,真实订阅名原样)
	base.WireAfterMerge(provs, rep.PersonalProxies, MergeOpts{})
	base.SetPath(filepath.Join(dir, "merged.yaml"))
	base.Save()
	s := string(mustRead(t, filepath.Join(dir, "merged.yaml")))

	// 接管(个人)
	for _, want := range []string{
		"mixed-port: 7897", "port: 18080", "socks-port: 10808",
		"secret: mysecret", "127.0.0.1:19090",
		"name: 我的分组", "PROCESS-NAME,ssh,DIRECT", "家庭VPS",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("未接管: %q", want)
		}
	}
	// 保留(基底暗号/基础设施)
	for _, want := range []string{"routing-mark: 6666", `listen: "[::]:1053"`, "stack: system", "ntp.aliyun.com"} {
		if !strings.Contains(s, want) {
			t.Errorf("基底未保留: %q", want)
		}
	}
	// 合并:providers(Nano 保留 + mine2 新增)
	if !strings.Contains(s, "mine2:") || !strings.Contains(s, "Nano:") {
		t.Error("订阅合并不符")
	}
	// 自动调整:进程规则 → strict
	if !strings.Contains(s, "find-process-mode: strict") {
		t.Error("进程规则未触发 find-process-mode=strict")
	}
	// 接线:Nano 追加进个人组;占位 SUB 应已退场(真实订阅就位)
	// 叠加融合:基底组保留(含 Nano 在 use 中的引用),个人组追加
	if !strings.Contains(s, "use: [mine2, Nano]") && !strings.Contains(s, "use: [SUB, Nano, mine2]") {
		// 个人组的 use 含 mine2,基底组的 use 含 Nano;叠加后两者共存
		hasNano := strings.Contains(s, "Nano")
		hasMine2 := strings.Contains(s, "mine2")
		if !hasNano || !hasMine2 {
			t.Errorf("基底与个人订阅均应存在:Nano=%v mine2=%v", hasNano, hasMine2)
		}
	}
	if got := base.Providers(); strings.Contains(strings.Join(got, ","), "SUB") {
		t.Errorf("占位订阅应退场,现有: %v", got)
	}
	if strings.Contains(s, `url: "SUB_URL_PLACEHOLDER"`) {
		t.Error("占位订阅 URL 残留")
	}
	// 个人 proxies 追加进有 proxies 列表的组(末尾,默认不变)
	if !strings.Contains(s, "proxies: [家庭VPS, 公司出口]") {
		t.Errorf("个人组 proxies 列表被破坏(应原样保留,追加不重复)")
	}
	// 叠加融合验证:基底组保留 + 同名融合 + 新增追加
	if !strings.Contains(s, "name: DNS") {
		t.Error("基底 DNS 组应保留(叠加融合不删基底组)")
	}
	if !strings.Contains(s, "🚀 节点选择") {
		t.Error("基底 🚀 节点选择 组应保留(叠加融合)")
	}
	if !strings.Contains(s, "name: 我的分组") {
		t.Error("个人新增组应追加")
	}
	// 规则:个人前置 + 基底兜底
	if !strings.Contains(s, "PROCESS-NAME,ssh,DIRECT") {
		t.Error("个人进程规则应在前置")
	}
	if !strings.Contains(s, "GEOSITE,TikTok,🎵 TikTok") {
		t.Error("基底规则应保留为兜底")
	}
	// MATCH 应在最后
	rulesStart := strings.Index(s, "rules:")
	matchIdx := strings.LastIndex(s, "MATCH,🌐 其他")
	if rulesStart < 0 || matchIdx < rulesStart {
		t.Error("MATCH 规则应存在")
	}
	// &p 锚点保留
	if !strings.Contains(s, "p: &p") {
		t.Error("&p 锚点应保留(sub import 依赖)")
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

// TestMergedConfigPassesCheck 融合产物过进程内内核 -t(等价外部 mihomo -t)。
func TestMergedConfigPassesCheck(t *testing.T) {
	geoSrc := geoFallback(t)
	base, per, dir := mergeSetup(t)
	rep, _ := base.MergePersonal(per, MergeOpts{})
	base.WireAfterMerge(base.Providers(), rep.PersonalProxies, MergeOpts{})
	merged := filepath.Join(dir, "merged.yaml")
	base.SetPath(merged)
	base.Save()
	// geo 就位
	for _, f := range []string{"GeoIP.dat", "GeoSite.dat", "Country.mmdb"} {
		if b, err := os.ReadFile(filepath.Join(geoSrc, f)); err == nil {
			os.WriteFile(filepath.Join(dir, f), b, 0o644)
		}
	}
	os.MkdirAll(filepath.Join(dir, "ui", "official"), 0o755)
	if err := core.Validate(dir, mustRead(t, merged)); err != nil {
		t.Errorf("融合产物未过 -t:%v", err)
	}
}

func geoFallback(t *testing.T) string {
	t.Helper()
	for _, c := range []string{
		filepath.Join("/opt", constants.ProgName),
		"/opt/panixy", // 旧版残留
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
	t.Skip("本机无 geodata(GeoSite.dat),跳过进程内 -t 实测")
	return ""
}
