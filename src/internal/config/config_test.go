package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deadship2003/panoxy/internal/asset"
	"github.com/deadship2003/panoxy/internal/core"
)

// renderTmp renders the base template to a temp file and returns an Editor over it.
func renderTmp(t *testing.T) (*Editor, string) {
	t.Helper()
	out, err := asset.RenderConfig(asset.DefaultConfigData())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "clash.yaml")
	if err := os.WriteFile(p, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	return e, p
}

func TestRoundTripPreservesCommentsAndAnchors(t *testing.T) {
	e, p := renderTmp(t)
	if err := e.Save(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	s := string(got)
	for _, want := range []string{
		"# ============ Subscription sources", // comments preserved
		"SUB_URL_PLACEHOLDER",                 // original content preserved
		"<<: *p",                              // merge anchor preserved
		"p: &p",                               // anchor definition preserved
		"- {name: DNS, <<: *use,",             // flow-style group preserved
		"🔃 自动选择",                              // emoji not escaped into \U form
		"stack: system",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("round-trip lost: %q", want)
		}
	}
}

func TestSetProviderAddAndWire(t *testing.T) {
	e, p := renderTmp(t)
	if err := e.SetProvider("airport2", "https://example.com/s2?token=a&sid=b", "./proxies/airport2.yaml"); err != nil {
		t.Fatal(err)
	}
	if n := e.WireProvider("airport2", true, nil); n != 3 {
		t.Fatalf("expected 3 anchor holders wired, got %d", n)
	}
	if err := e.Save(); err != nil {
		t.Fatal(err)
	}
	s := string(mustRead(t, p))
	for _, want := range []string{
		"airport2:",
		"url: https://example.com/s2?token=a&sid=b",
		"path: ./proxies/airport2.yaml",
		"use: [SUB, airport2]",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing: %q", want)
		}
	}
	if got := e.Providers(); len(got) != 2 || got[1] != "airport2" {
		t.Errorf("providers = %v", got)
	}
}

func TestSetProviderUpdateOnlyUrlPath(t *testing.T) {
	e, _ := renderTmp(t)
	// overwrite SUB: only the url changes; path stays ./proxies/SUB.yaml by semantics
	if err := e.SetProvider("SUB", "https://new.example.com/x", "./proxies/SUB.yaml"); err != nil {
		t.Fatal(err)
	}
	u, ok := e.ProviderURL("SUB")
	if !ok || u != "https://new.example.com/x" {
		t.Fatalf("url = %q %v", u, ok)
	}
	if got := e.Providers(); len(got) != 1 {
		t.Errorf("an overwrite must not add an entry: %v", got)
	}
}

func TestRemoveProviderUnwires(t *testing.T) {
	e, p := renderTmp(t)
	e.SetProvider("airport2", "https://x/y", "./proxies/airport2.yaml")
	e.WireProvider("airport2", true, nil)
	if !e.RemoveProvider("airport2") {
		t.Fatal("delete failed")
	}
	if n := e.WireProvider("airport2", false, nil); n != 3 {
		t.Fatalf("expected 3 reverse-wires, got %d", n)
	}
	e.Save()
	s := string(mustRead(t, p))
	if strings.Contains(s, "airport2") {
		t.Errorf("airport2 residue left")
	}
	if !strings.Contains(s, "use: [SUB]") {
		t.Errorf("use list not restored")
	}
}

func TestAnchorGuard(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	os.WriteFile(p, []byte("mixed-port: 7890\n"), 0o644)
	e, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.SetProvider("X", "https://a/b", "./proxies/X.yaml"); err == nil {
		t.Fatal("writing without the &p anchor should be rejected")
	}
}

func TestWireCustomConfigFallback(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "custom.yaml")
	os.WriteFile(p, []byte(`mixed-port: 7890
proxy-providers:
  mine:
    type: http
    url: "https://a/b"
    path: ./proxies/mine.yaml
proxy-groups:
  - { name: G1, type: select, use: [mine] }
  - { name: G2, type: select, proxies: [DIRECT] }
rules:
  - MATCH,G1
`), 0o644)
	e, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if n := e.WireProvider("second", true, nil); n != 1 {
		t.Fatalf("a custom config should wire only G1 (non-empty use), got %d", n)
	}
	e.Save()
	s := string(mustRead(t, p))
	if !strings.Contains(s, "use: [mine, second]") {
		t.Errorf("G1 not wired: %s", s)
	}
	if !strings.Contains(s, "proxies: [DIRECT]") {
		t.Errorf("G2 must not be touched")
	}
}

// TestPruneDerivedKeepsOnlyMatchedGroups verifies derived-group pruning: region/type
// groups without a matching real node name are removed, and their names are stripped
// from pr/prd/dns proxies lists too, avoiding dangling references.
func TestPruneDerivedKeepsOnlyMatchedGroups(t *testing.T) {
	e, p := renderTmp(t)
	// simulate a real subscription: only HK + US + streaming nodes
	names := []string{"香港 01 | 原生IP", "香港 02", "美国 流媒体解锁"}
	if n := e.PruneDerived(names); n == 0 {
		t.Fatal("unmatched derived groups should be pruned")
	}
	e.Save()
	s := string(mustRead(t, p))
	for _, keep := range []string{"香港", "美国", "🎬 流媒体", "全部节点", "🔃 自动选择"} {
		if !strings.Contains(s, keep) {
			t.Errorf("expected %q to be kept, but it is missing", keep)
		}
	}
	for _, gone := range []string{"阿根廷", "台湾", "🇨🇳 回国"} {
		if strings.Contains(s, gone) {
			t.Errorf("expected %q to be pruned, but it remains", gone)
		}
	}
}

// TestEditedConfigPassesCheck is the final integration: template -> add/remove provider ->
// in-process kernel -t (equivalent to the external mihomo -t).
func TestEditedConfigPassesCheck(t *testing.T) {
	geoSrc := geoFallback(t)
	dir := t.TempDir()
	for _, f := range []string{"GeoIP.dat", "GeoSite.dat", "Country.mmdb"} {
		if b, err := os.ReadFile(filepath.Join(geoSrc, f)); err == nil {
			os.WriteFile(filepath.Join(dir, f), b, 0o644)
		}
	}
	os.MkdirAll(filepath.Join(dir, "ui", "official"), 0o755)

	e, _ := renderTmp(t)
	e.SetProvider("airport2", "https://example.com/s2", "./proxies/airport2.yaml")
	e.WireProvider("airport2", true, nil)
	// remove every subscription (airport2 and SUB): the groups lose their use — -t must reject
	e.RemoveProvider("airport2")
	e.WireProvider("airport2", false, nil)
	e.RemoveProvider("SUB")
	e.WireProvider("SUB", false, nil)
	e.path = filepath.Join(dir, "clash.yaml")
	e.Save()
	if err := core.Validate(dir, mustRead(t, e.path)); err == nil {
		t.Fatalf("removing every subscription should be rejected by -t, but it passed")
	}

	// a normal edit keeping SUB must pass
	e2, _ := renderTmp(t)
	e2.SetProvider("airport2", "https://example.com/s2", "./proxies/airport2.yaml")
	e2.WireProvider("airport2", true, nil)
	e2.path = filepath.Join(dir, "clash2.yaml")
	e2.Save()
	if err := core.Validate(dir, mustRead(t, e2.path)); err != nil {
		t.Errorf("the edited config failed -t: %v", err)
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
