package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/deadship2003/panoxy/internal/asset"
	"github.com/deadship2003/panoxy/internal/constants"
)

// TestValidateRenderedConfig validates the tun/tproxy configs rendered by panoxy with
// the in-process kernel, equivalent to the external `mihomo -t` (replacing that external
// call since M1). Needs geodata files; skips when absent on this machine (verified again
// in CI/packaging).
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
			t.Errorf("%s: in-process -t validation failed: %v", tc.name, err)
		}
	}
}

// geodataSrc locates the geodata directory (the same source set as the asset package
// tests); skips when not found.
func geodataSrc(t *testing.T) string {
	t.Helper()
	if s := os.Getenv("GEO_SRC"); s != "" {
		return s
	}
	for _, c := range []string{
		filepath.Join("/opt", constants.ProgName),
		"/opt/panixy", // legacy leftover name
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
	t.Skip("no geodata on this machine (GeoSite.dat); skipping the in-process -t verification")
	return ""
}
