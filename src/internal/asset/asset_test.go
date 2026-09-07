package asset

import (
	"strings"
	"testing"
)

func TestRenderConfigVariants(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tproxy bool
	}{{"tun", false}, {"tproxy", true}} {
		d := DefaultConfigData()
		d.TProxy = tc.tproxy
		d.Secret = "test-secret"
		out, err := RenderConfig(d)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !strings.Contains(out, "mixed-port: 33833") || !strings.Contains(out, "secret: test-secret") {
			t.Errorf("%s: port/secret not rendered", tc.name)
		}
		if !strings.Contains(out, "routing-mark: 6666") {
			t.Errorf("%s: routing-mark missing (DNS-loop prevention)", tc.name)
		}
		if !strings.Contains(out, `listen: "[::]:1053"`) {
			t.Errorf("%s: DNS listen should be [::]:1053 dual-stack (the redirect landing point)", tc.name)
		}
		if !strings.Contains(out, "fake-ip-range6: 2001:2::1/48") {
			t.Errorf("%s: fake-ip-range6 missing (IPv6 fake-ip pool)", tc.name)
		}
		// assertions look at non-comment lines only (comments mention these historical fields)
		var body []string
		for _, l := range strings.Split(out, "\n") {
			if !strings.HasPrefix(strings.TrimSpace(l), "#") {
				body = append(body, l)
			}
		}
		code := strings.Join(body, "\n")
		if strings.Contains(code, "dns-hijack") || strings.Contains(code, "\n  fallback:") {
			t.Errorf("%s: must not contain dns-hijack/fallback", tc.name)
		}
		if tc.tproxy {
			if !strings.Contains(out, "tproxy-port: 7893") || strings.Contains(out, "tun:") {
				t.Errorf("wrong tproxy variant")
			}
		} else {
			if !strings.Contains(out, "stack: system") || strings.Contains(out, "tproxy-port") {
				t.Errorf("wrong tun variant")
			}
		}
	}
}
