// Package health implements the five-part health check: service state, API, per-provider
// node counts, proxy egress connectivity, and firewall leftovers.
// Core lesson (from bash-era practice): when the kernel cannot fetch a subscription the
// API still answers normally — checking only the API yields a false success; the node
// count is the precondition for "can actually forward".
package health

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/deadship2003/panoxy/internal/config"
	"github.com/deadship2003/panoxy/internal/core"
	"github.com/deadship2003/panoxy/internal/firewall"
	"github.com/deadship2003/panoxy/internal/mihomoapi"
	"github.com/deadship2003/panoxy/internal/statemode"
	"github.com/deadship2003/panoxy/internal/systemdunit"
)

type Report struct {
	Service   string                   `json:"service"`
	APIAlive  bool                     `json:"api"`
	APIVer    string                   `json:"api_version,omitempty"`
	FwBackend string                   `json:"firewall"`
	Stale     bool                     `json:"stale_rules"`
	Mode      string                   `json:"mode"`
	Providers []mihomoapi.ProviderStat `json:"providers"`
	Nodes     int                      `json:"nodes"`        // total nodes across all providers
	Egress    string                   `json:"proxy_egress"` // generate_204 status code via the mixed-port
	Direct    string                   `json:"direct_egress"`
	CoreVer   string                   `json:"core,omitempty"`
	UIVer     string                   `json:"ui,omitempty"`
	LastUp    string                   `json:"last_upgrade,omitempty"`
}

// Collect gathers a health snapshot. confPath feeds the API client and the provider list.
// A single failing probe never affects the others (probing is never fatal).
func Collect(confPath, uiStamp, lastUp, statePath string) Report {
	r := Report{Service: systemdunit.Active(), Mode: modeOf(statePath)}
	api := mihomoapi.NewFromConf(confPath)
	if v, err := api.Version(); err == nil {
		r.APIAlive = true
		r.APIVer = v
	}
	r.FwBackend = firewall.BackendName
	r.Stale, _ = firewall.HasStaleRules()
	if e, err := config.Load(confPath); err == nil {
		for _, name := range e.Providers() {
			st, _ := api.Provider(name)
			r.Providers = append(r.Providers, st)
			r.Nodes += st.Nodes
		}
	}
	r.Egress = probe204(true, api.Mixed)
	r.Direct = probe204(false, 0)
	r.CoreVer = core.Version()
	if b, err := os.ReadFile(uiStamp); err == nil {
		r.UIVer = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile(lastUp); err == nil {
		r.LastUp = strings.TrimSpace(string(b))
	}
	return r
}

func modeOf(statePath string) string {
	return statemode.Read(statePath)
}

func probe204(viaProxy bool, port int) string {
	target := "http://connect.rom.miui.com/generate_204"
	if viaProxy {
		target = "https://www.gstatic.com/generate_204"
	}
	req, _ := http.NewRequest("GET", target, nil)
	hc := &http.Client{Timeout: 8 * time.Second}
	if viaProxy && port > 0 {
		u, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
		hc.Transport = &http.Transport{Proxy: http.ProxyURL(u)}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "000"
	}
	resp.Body.Close()
	return fmt.Sprintf("%d", resp.StatusCode)
}

// EgressOK checks for a 204 through the proxy, retrying `retries` times (used by the
// upgrade health check).
func EgressOK(port int, retries int) bool {
	for i := 0; i < retries; i++ {
		if code := probe204(true, port); code == "204" {
			return true
		}
		time.Sleep(3 * time.Second)
	}
	return false
}

// WaitHealthy polls until the service + API are ready (with a timeout, replacing fixed
// sleeps; slow gateways are not misjudged). A non-empty expectVer additionally requires
// the API to report that version.
func WaitHealthy(confPath string, timeout time.Duration, expectVer string) error {
	api := mihomoapi.NewFromConf(confPath)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		v, err := api.Version()
		if err == nil && systemdunit.IsActive() &&
			(expectVer == "" || v == expectVer) {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("health check timed out (service/API not ready)")
}
