// Package mihomoapi wraps the mihomo external-controller REST API.
// Note (verified by experiment, recorded here to prevent misuse): PUT /configs hot-reload
// re-parses the config and rebuilds provider objects but does NOT re-fetch subscription
// content (Initial only reads the local cache, never the remote URL); a no-restart
// re-fetch is PUT /providers/proxies/{name}, while adding/removing providers or rewiring
// groups still requires a process restart.
package mihomoapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/deadship2003/panoxy/internal/constants"

	"gopkg.in/yaml.v3"
)

type Client struct {
	Base   string // http://127.0.0.1:9999
	Secret string
	Mixed  int // mixed-port (the local proxy stepping stone)
	hc     *http.Client
}

// NewFromConf parses secret/external-controller/mixed-port from the mihomo config file;
// the env vars <PROG>_API/<PROG>_SECRET/<PROG>_PROXY_PORT can override them (sandbox
// testing).
func NewFromConf(confPath string) *Client {
	c := &Client{hc: &http.Client{Timeout: 5 * time.Second}}
	var raw struct {
		Secret             string `yaml:"secret"`
		ExternalController string `yaml:"external-controller"`
		MixedPort          int    `yaml:"mixed-port"`
	}
	if b, err := os.ReadFile(confPath); err == nil {
		yaml.Unmarshal(b, &raw) // on parse failure everything falls back to defaults
	}
	c.Secret = raw.Secret
	if c.Secret == "" {
		c.Secret = constants.DefSecret
	}
	c.Mixed = raw.MixedPort
	api := "http://127.0.0.1:" + portOf(raw.ExternalController, constants.ApiPortDef)
	if p := os.Getenv(constants.EnvPrefix() + "_API_PORT"); p != "" {
		api = "http://127.0.0.1:" + p
	}
	if u := os.Getenv(constants.EnvPrefix() + "_API"); u != "" {
		api = u
	}
	if s := os.Getenv(constants.EnvPrefix() + "_SECRET"); s != "" {
		c.Secret = s
	}
	if p := os.Getenv(constants.EnvPrefix() + "_PROXY_PORT"); p != "" && c.Mixed == 0 {
		fmt.Sscanf(p, "%d", &c.Mixed)
	}
	c.Base = strings.TrimRight(api, "/")
	return c
}

func portOf(ctrl string, def int) string {
	if i := strings.LastIndex(ctrl, ":"); i >= 0 && i+1 < len(ctrl) {
		return ctrl[i+1:]
	}
	return fmt.Sprint(def)
}

// Proxy returns the local mixed-port proxy address (empty string when unset).
func (c *Client) Proxy() string {
	if c.Mixed <= 0 {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d", c.Mixed)
}

// call sends an authenticated API request and returns the body; status >= 300 is an error.
func (c *Client) call(method, path string, body any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.Base+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Secret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return b, fmt.Errorf("API %s %s → HTTP %d", method, path, resp.StatusCode)
	}
	return b, nil
}

// Version returns the kernel version string (e.g. v1.19.30); an unreachable API is an error.
func (c *Client) Version() (string, error) {
	b, err := c.call("GET", "/version", nil)
	if err != nil {
		return "", err
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(b, &v); err != nil || v.Version == "" {
		return "", fmt.Errorf("unexpected version response: %s", string(b))
	}
	return v.Version, nil
}

// ProviderStat is one subscription's health snapshot.
type ProviderStat struct {
	Name  string `json:"name"`
	Nodes int    `json:"nodes"`
	Type  string `json:"type"`
	Error string `json:"error,omitempty"`
}

type providerResp struct {
	Name        string `json:"name"`
	VehicleType string `json:"vehicleType"`
	Proxies     []struct {
		Name string `json:"name"`
	} `json:"proxies"`
}

// Provider queries a single provider's node count (properly decoding the JSON, unlike
// the bash-era grep counting).
func (c *Client) Provider(name string) (ProviderStat, error) {
	b, err := c.call("GET", "/providers/proxies/"+name, nil)
	var st ProviderStat
	st.Name = name
	if err != nil {
		st.Error = "fetch failed: " + err.Error()
		return st, err
	}
	var pr providerResp
	if err := json.Unmarshal(b, &pr); err != nil {
		st.Error = "parse failed: " + err.Error()
		return st, err
	}
	st.Nodes = len(pr.Proxies)
	st.Type = pr.VehicleType
	return st, nil
}

// ReloadConf hot-reloads the config: provider objects are rebuilt but subscription
// content is not re-fetched, so this only suits changes that leave providers untouched.
func (c *Client) ReloadConf(path string) error {
	_, err := c.call("PUT", "/configs?force=0", map[string]string{"path": path})
	return err
}

// RawGet is a liveness-probe GET: returns the HTTP status code string (no JSON decoding).
func (c *Client) RawGet(path string) (string, error) {
	resp, err := c.hc.Get(c.Base + path)
	if err != nil {
		return "000", err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return fmt.Sprintf("%d", resp.StatusCode), nil
}
