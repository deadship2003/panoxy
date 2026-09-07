package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/deadship2003/panoxy/internal/asset"
	"github.com/deadship2003/panoxy/internal/constants"
)

// TestValidateRenderedConfig 用进程内内核校验 panoxy 渲染的 tun/tproxy 配置,
// 等价外部 `mihomo -t`(M1 起替代该外部调用)。
// 需 geodata 文件,本机缺失时跳过(CI/打包阶段再验)。
func TestValidateRenderedConfig(t *testing.T) {
	geoSrc := geodataSrc(t)
	for _, tc := range []struct {
		name   string
		tproxy bool
	}{{"tun", false}, {"tproxy", true}} {
		d := asset.DefaultConfigData()
		d.TProxy = tc.tproxy
		out, err := asset.RenderConfig(d)
		if err != nil {
			t.Fatalf("%s: render: %v", tc.name, err)
		}
		dir := t.TempDir()
		for _, f := range []string{"GeoIP.dat", "GeoSite.dat", "Country.mmdb"} {
			if b, err := os.ReadFile(filepath.Join(geoSrc, f)); err == nil {
				os.WriteFile(filepath.Join(dir, f), b, 0o644)
			}
		}
		os.MkdirAll(filepath.Join(dir, "ui", "official"), 0o755)
		if err := Validate(dir, []byte(out)); err != nil {
			t.Errorf("%s: 进程内 -t 校验失败: %v", tc.name, err)
		}
	}
}

// geodataSrc 定位 geodata 文件目录(与 asset 包测试同一套来源);找不到则 skip。
func geodataSrc(t *testing.T) string {
	t.Helper()
	if s := os.Getenv("GEO_SRC"); s != "" {
		return s
	}
	for _, c := range []string{
		filepath.Join("/opt", constants.ProgName),
		"/opt/panixy", // 旧版残留
	} {
		if _, err := os.Stat(filepath.Join(c, "GeoSite.dat")); err == nil {
			return c
		}
	}
	if h, err := os.UserHomeDir(); err == nil {
		if _, err := os.Stat(filepath.Join(h, "panoxy-e2e", "GeoSite.dat")); err == nil {
			return filepath.Join(h, "panoxy-e2e")
		}
	}
	t.Skip("本机无 geodata(GeoSite.dat),跳过进程内 -t 实测")
	return ""
}
