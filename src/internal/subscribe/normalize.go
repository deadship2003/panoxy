// Subscription format detection and normalization: unify any standard subscription
// format into Clash YAML that mihomo can parse.
//
// Key facts (verified against mihomo v1.19.30):
//   - mihomo's proxy-provider (type: http or file alike) natively parses only Clash
//     YAML plus base64/plaintext URI lists (vless/vmess/trojan/ss/ssr/hysteria2/tuic ...).
//   - mihomo cannot natively parse: sing-box JSON, Surge configs, or base64-encoded
//     Clash YAML. Those three must be normalized into Clash YAML by panoxy before the
//     cache is written, with the provider switched to type: file (otherwise the kernel
//     re-fetches the original URL on restart refresh and fails to parse again).
//
// So the job here is not airport-specific parsing: it is generic detection and
// conversion covering every standard subscription format.
package subscribe

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Format is the subscription content format.
type Format int

const (
	FormatUnknown     Format = iota // unrecognizable (empty, an HTML error page, ...)
	FormatClash                     // Clash YAML (proxies: list)
	FormatURI                       // plaintext URI list (one scheme://... per line)
	FormatBase64URI                 // base64-encoded URI list (mihomo decodes natively)
	FormatBase64Clash               // base64-encoded Clash YAML (needs decoding)
	FormatSingBox                   // sing-box JSON (outbounds:)
	FormatSurge                     // Surge config (#!MANAGED-CONFIG / [Proxy])
)

// uriSchemeRe matches the common proxy URI schemes (to decide whether a line is a node URI).
var uriSchemeRe = regexp.MustCompile(`^(vless|vmess|trojan|ss|ssr|hysteria2?|hy2|tuic|snell|wireguard|http|https|socks5)://`)

// Detect detects the subscription content format.
func Detect(b []byte) Format {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return FormatUnknown
	}
	// Surge managed-config header
	if strings.HasPrefix(s, "#!MANAGED-CONFIG") {
		return FormatSurge
	}
	// sing-box JSON
	if strings.HasPrefix(s, "{") {
		var j map[string]any
		if json.Unmarshal([]byte(s), &j) == nil {
			if _, ok := j["outbounds"]; ok {
				return FormatSingBox
			}
		}
	}
	// Clash YAML (contains the proxies key)
	var doc map[string]any
	if yaml.Unmarshal([]byte(s), &doc) == nil {
		if _, ok := doc["proxies"]; ok {
			return FormatClash
		}
	}
	// bare Surge config without the managed header ([Proxy] section)
	if strings.Contains(s, "[Proxy]") {
		return FormatSurge
	}
	// plaintext URI list
	if isURILine(firstNonEmptyLine(s)) {
		return FormatURI
	}
	// base64: after decoding it may be a URI list or Clash YAML
	if dec, err := decodeBase64Line(s); err == nil {
		d := strings.TrimSpace(dec)
		if d == "" {
			return FormatUnknown
		}
		if isURILine(firstNonEmptyLine(d)) {
			return FormatBase64URI
		}
		var doc2 map[string]any
		if yaml.Unmarshal([]byte(d), &doc2) == nil {
			if _, ok := doc2["proxies"]; ok {
				return FormatBase64Clash
			}
		}
	}
	return FormatUnknown
}

// Normalize normalizes subscription content into Clash YAML mihomo can parse.
// It returns the normalized bytes, whether a conversion happened (the provider must
// then switch to type: file), and an error.
// Clash YAML / URI lists (plaintext or base64) are parsed natively by mihomo and
// passed through as-is (converted=false).
func Normalize(b []byte) ([]byte, bool, error) {
	switch Detect(b) {
	case FormatBase64Clash:
		dec, err := decodeBase64Line(strings.TrimSpace(string(b)))
		if err != nil {
			return nil, false, fmt.Errorf("failed to decode base64 Clash: %w", err)
		}
		return []byte(dec), true, nil
	case FormatSingBox:
		out, err := singboxToClash(b)
		return out, true, err
	case FormatSurge:
		out, err := surgeToClash(b)
		return out, true, err
	case FormatUnknown:
		return nil, false, fmt.Errorf("subscription is not in a recognizable format (supported: Clash YAML / URI list / sing-box JSON / Surge; airports often return a web page for an invalid token)")
	default:
		return b, false, nil
	}
}

// NodeNames extracts the node names from a subscription (for derived-group pruning:
// keep only the region/type groups actually hit). Covers every standard format;
// URI lists use the #fragment name.
func NodeNames(b []byte) ([]string, error) {
	switch Detect(b) {
	case FormatClash:
		return clashNodeNames(b)
	case FormatURI:
		return uriNodeNames(b)
	case FormatBase64URI, FormatBase64Clash:
		dec, err := decodeBase64Line(strings.TrimSpace(string(b)))
		if err != nil {
			return nil, err
		}
		if Detect([]byte(dec)) == FormatClash {
			return clashNodeNames([]byte(dec))
		}
		return uriNodeNames([]byte(dec))
	case FormatSingBox:
		return singboxNodeNames(b)
	case FormatSurge:
		return surgeNodeNames(b)
	default:
		return nil, fmt.Errorf("unrecognized subscription format (not Clash YAML / URI list / sing-box / Surge)")
	}
}

// ---- per-format node-name extraction ----

func clashNodeNames(b []byte) ([]string, error) {
	var doc struct {
		Proxies []struct {
			Name string `yaml:"name"`
		} `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("failed to parse Clash YAML: %w", err)
	}
	out := make([]string, 0, len(doc.Proxies))
	for _, p := range doc.Proxies {
		if p.Name != "" {
			out = append(out, p.Name)
		}
	}
	return out, nil
}

func uriNodeNames(b []byte) ([]string, error) {
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if n := uriFragmentName(line); n != "" {
			out = append(out, n)
		}
	}
	return out, nil
}

func singboxNodeNames(b []byte) ([]string, error) {
	var doc struct {
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("failed to parse sing-box JSON: %w", err)
	}
	out := make([]string, 0, len(doc.Outbounds))
	for _, ob := range doc.Outbounds {
		if ob.Tag != "" {
			out = append(out, ob.Tag)
		}
	}
	return out, nil
}

// surgeProxyLines returns the non-comment lines inside a Surge config's [Proxy]
// section (shared by node-name extraction and node conversion).
func surgeProxyLines(b []byte) []string {
	var out []string
	inProxy := false
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			inProxy = strings.EqualFold(strings.Trim(t, "[]"), "Proxy")
			continue
		}
		if !inProxy || t == "" || strings.HasPrefix(t, ";") || strings.HasPrefix(t, "#") {
			continue
		}
		out = append(out, t)
	}
	return out
}

func surgeNodeNames(b []byte) ([]string, error) {
	var out []string
	for _, line := range surgeProxyLines(b) {
		if i := strings.Index(line, "="); i > 0 {
			out = append(out, strings.TrimSpace(line[:i]))
		}
	}
	return out, nil
}

// ---- conversion: sing-box JSON -> Clash YAML ----

func singboxToClash(b []byte) ([]byte, error) {
	var doc struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("failed to parse sing-box JSON: %w", err)
	}
	proxies := make([]map[string]any, 0, len(doc.Outbounds))
	for _, ob := range doc.Outbounds {
		if p, ok := singboxOutbound(ob); ok {
			proxies = append(proxies, p)
		}
	}
	if len(proxies) == 0 {
		return nil, fmt.Errorf("no convertible outbounds in sing-box JSON (only vless/vmess/trojan/shadowsocks/hysteria2/tuic supported)")
	}
	return renderClashProxies(proxies)
}

// singboxOutbound maps a single sing-box outbound to a Clash proxy (unsupported types
// return ok=false and are skipped). Note the TLS field names differ per protocol:
// vless/vmess use tls+servername while trojan/hysteria2/tuic use sni; hysteria2/tuic
// have no udp field (hysteria2 is UDP by nature, tuic uses udp-relay-mode).
func singboxOutbound(ob map[string]any) (map[string]any, bool) {
	tag, _ := ob["tag"].(string)
	sbType, _ := ob["type"].(string)
	server, _ := ob["server"].(string)
	port := intVal(ob["server_port"])
	if server == "" || port == 0 {
		return nil, false // a non-node outbound such as direct/dns-out
	}
	p := map[string]any{"name": tag, "server": server, "port": port}

	var tlsEnabled bool
	var sni string
	var insecure bool
	if tls, ok := ob["tls"].(map[string]any); ok {
		tlsEnabled, _ = tls["enabled"].(bool)
		sni, _ = tls["server_name"].(string)
		insecure, _ = tls["insecure"].(bool)
	}

	switch sbType {
	case "vless":
		p["type"] = "vless"
		p["udp"] = true
		if u, _ := ob["uuid"].(string); u != "" {
			p["uuid"] = u
		}
		if f, _ := ob["flow"].(string); f != "" {
			p["flow"] = f
		}
		if tlsEnabled {
			p["tls"] = true
			if sni != "" {
				p["servername"] = sni
			}
		}
		if insecure {
			p["skip-cert-verify"] = true
		}
	case "vmess":
		p["type"] = "vmess"
		p["udp"] = true
		if u, _ := ob["uuid"].(string); u != "" {
			p["uuid"] = u
		}
		// mihomo's vmess requires explicit alterId and cipher (defaults must be written out too, or it refuses to load)
		p["alterId"] = intVal(ob["alter_id"])
		cipher := "auto"
		if c, _ := ob["security"].(string); c != "" && c != "auto" {
			cipher = c
		}
		p["cipher"] = cipher
		if tlsEnabled {
			p["tls"] = true
			if sni != "" {
				p["servername"] = sni
			}
		}
		if insecure {
			p["skip-cert-verify"] = true
		}
	case "trojan":
		p["type"] = "trojan"
		p["udp"] = true
		if pw, _ := ob["password"].(string); pw != "" {
			p["password"] = pw
		}
		if sni != "" {
			p["sni"] = sni
		}
		if insecure {
			p["skip-cert-verify"] = true
		}
	case "shadowsocks":
		p["type"] = "ss"
		p["udp"] = true
		if m, _ := ob["method"].(string); m != "" {
			p["cipher"] = m
		}
		if pw, _ := ob["password"].(string); pw != "" {
			p["password"] = pw
		}
		if pf, _ := ob["plugin"].(string); pf != "" {
			p["plugin"] = pf
		}
	case "hysteria2":
		p["type"] = "hysteria2"
		if pw, _ := ob["password"].(string); pw != "" {
			p["password"] = pw
		}
		if sni != "" {
			p["sni"] = sni
		}
		if insecure {
			p["skip-cert-verify"] = true
		}
	case "tuic":
		p["type"] = "tuic"
		if u, _ := ob["uuid"].(string); u != "" {
			p["uuid"] = u
		}
		if pw, _ := ob["password"].(string); pw != "" {
			p["password"] = pw
		}
		if sni != "" {
			p["sni"] = sni
		}
		if insecure {
			p["skip-cert-verify"] = true
		}
	default:
		return nil, false
	}

	// transport exists only for TCP-type protocols (ws/http/grpc)
	if tr, ok := ob["transport"].(map[string]any); ok {
		applySingboxTransport(p, tr)
	}
	return p, true
}

// applySingboxTransport maps a sing-box transport to Clash's network + the matching opts.
func applySingboxTransport(p map[string]any, tr map[string]any) {
	t, _ := tr["type"].(string)
	switch t {
	case "ws":
		p["network"] = "ws"
		opts := map[string]any{}
		if path, _ := tr["path"].(string); path != "" {
			opts["path"] = path
		}
		if h, ok := tr["headers"].(map[string]any); ok && len(h) > 0 {
			hd := make(map[string]string, len(h))
			for k, v := range h {
				hd[k] = fmt.Sprint(v)
			}
			opts["headers"] = hd
		}
		if len(opts) > 0 {
			p["ws-opts"] = opts
		}
	case "http":
		p["network"] = "http"
		opts := map[string]any{}
		if path, _ := tr["path"].(string); path != "" {
			opts["path"] = []string{path}
		}
		if len(opts) > 0 {
			p["http-opts"] = opts
		}
	case "grpc":
		p["network"] = "grpc"
		if svc, _ := tr["service_name"].(string); svc != "" {
			p["grpc-opts"] = map[string]any{"grpc-service-name": svc}
		}
	}
}

// ---- conversion: Surge -> Clash YAML ----

func surgeToClash(b []byte) ([]byte, error) {
	var proxies []map[string]any
	for _, line := range surgeProxyLines(b) {
		if p, ok := surgeProxy(line); ok {
			proxies = append(proxies, p)
		}
	}
	if len(proxies) == 0 {
		return nil, fmt.Errorf("no convertible nodes in Surge config [Proxy] section (only ss/trojan/vmess/vless/hysteria2 supported)")
	}
	return renderClashProxies(proxies)
}

// surgeProxy parses one Surge node definition line (common protocols: SS/SSR/trojan/vmess/vless/hysteria2).
func surgeProxy(line string) (map[string]any, bool) {
	i := strings.Index(line, "=")
	if i <= 0 {
		return nil, false
	}
	name := strings.TrimSpace(line[:i])
	parts := strings.Split(strings.TrimSpace(line[i+1:]), ",")
	if len(parts) < 3 {
		return nil, false
	}
	proto := strings.ToLower(strings.TrimSpace(parts[0]))
	server := strings.TrimSpace(parts[1])
	port := atoi(parts[2])
	if server == "" || port == 0 {
		return nil, false
	}
	params := map[string]string{}
	for _, kv := range parts[3:] {
		if j := strings.Index(kv, "="); j > 0 {
			params[strings.TrimSpace(kv[:j])] = strings.TrimSpace(kv[j+1:])
		}
	}

	p := map[string]any{"name": name, "server": server, "port": port}
	switch proto {
	case "ss", "shadowsocks":
		p["type"] = "ss"
		p["udp"] = true
		if m := params["encrypt-method"]; m != "" {
			p["cipher"] = m
		}
		if pw := params["password"]; pw != "" {
			p["password"] = pw
		}
	case "trojan":
		p["type"] = "trojan"
		p["udp"] = true
		if pw := params["password"]; pw != "" {
			p["password"] = pw
		}
		if sni := params["sni"]; sni != "" {
			p["sni"] = sni
		}
		if params["skip-cert-verify"] == "true" {
			p["skip-cert-verify"] = true
		}
	case "vmess":
		p["type"] = "vmess"
		p["udp"] = true
		if u := params["username"]; u != "" {
			p["uuid"] = u
		}
		// mihomo's vmess requires explicit alterId and cipher (Surge has no alterId; fixed at 0)
		p["alterId"] = 0
		cipher := "auto"
		if m := params["encrypt-method"]; m != "" {
			cipher = m
		}
		p["cipher"] = cipher
		applySurgeTLS(p, params)
		applySurgeWS(p, params)
	case "vless":
		p["type"] = "vless"
		p["udp"] = true
		if u := params["username"]; u != "" {
			p["uuid"] = u
		}
		applySurgeTLS(p, params)
		applySurgeWS(p, params)
	case "hysteria2", "hy2":
		p["type"] = "hysteria2"
		if pw := params["password"]; pw != "" {
			p["password"] = pw
		}
		if sni := params["sni"]; sni != "" {
			p["sni"] = sni
		}
		if params["skip-cert-verify"] == "true" {
			p["skip-cert-verify"] = true
		}
	default:
		return nil, false
	}
	return p, true
}

func applySurgeTLS(p map[string]any, params map[string]string) {
	if params["tls"] == "true" {
		p["tls"] = true
		if sni := params["sni"]; sni != "" {
			p["servername"] = sni
		}
		if params["skip-cert-verify"] == "true" {
			p["skip-cert-verify"] = true
		}
	}
}

func applySurgeWS(p map[string]any, params map[string]string) {
	if params["ws"] != "true" {
		return
	}
	p["network"] = "ws"
	opts := map[string]any{}
	if pth := params["ws-path"]; pth != "" {
		opts["path"] = pth
	}
	if h := params["ws-headers"]; h != "" {
		opts["headers"] = map[string]string{"Host": h}
	}
	if len(opts) > 0 {
		p["ws-opts"] = opts
	}
}

// ---- shared helpers ----

// nodeCount counts the nodes in a subscription (after format detection; lets Validate
// decide "has nodes"). Node names are not required: URI lists count lines, the rest
// count entries, so a URI list without #name is not misjudged as 0.
func nodeCount(b []byte) int {
	return nodeCountDetected(b, Detect(b))
}

// nodeCountDetected counts with the format already known, so Validate and nodeCount
// do not each repeat a Detect (double parsing).
func nodeCountDetected(b []byte, f Format) int {
	switch f {
	case FormatClash:
		var doc struct {
			Proxies []map[string]any `yaml:"proxies"`
		}
		if yaml.Unmarshal(b, &doc) == nil {
			return len(doc.Proxies)
		}
	case FormatURI:
		return countURILines(string(b))
	case FormatBase64URI, FormatBase64Clash:
		if dec, err := decodeBase64Line(strings.TrimSpace(string(b))); err == nil {
			return nodeCount([]byte(dec)) // the decoded payload is new content; re-detect
		}
	case FormatSingBox:
		var doc struct {
			Outbounds []map[string]any `json:"outbounds"`
		}
		if json.Unmarshal(b, &doc) == nil {
			return len(doc.Outbounds)
		}
	case FormatSurge:
		n := 0
		for _, line := range surgeProxyLines(b) {
			if strings.Contains(line, "=") {
				n++
			}
		}
		return n
	}
	return 0
}

func countURILines(s string) int {
	n := 0
	for _, l := range strings.Split(s, "\n") {
		if isURILine(l) {
			n++
		}
	}
	return n
}

// renderClashProxies renders a list of proxy maps into Clash YAML (the proxies: section).
func renderClashProxies(proxies []map[string]any) ([]byte, error) {
	out, err := yaml.Marshal(map[string]any{"proxies": proxies})
	if err != nil {
		return nil, fmt.Errorf("failed to render Clash YAML: %w", err)
	}
	return out, nil
}

// uriFragmentName extracts the URI's #fragment (the node name, percent-decoded).
func uriFragmentName(uri string) string {
	i := strings.IndexByte(uri, '#')
	if i < 0 || i+1 >= len(uri) {
		return ""
	}
	frag := uri[i+1:]
	if n, err := url.PathUnescape(frag); err == nil {
		return n
	}
	return frag
}

func isURILine(s string) bool {
	return uriSchemeRe.MatchString(strings.TrimSpace(s))
}

func firstNonEmptyLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			return t
		}
	}
	return ""
}

// decodeBase64Line decodes subscription base64 (whitespace stripped; standard/URL
// encodings and missing padding all tolerated).
func decodeBase64Line(s string) (string, error) {
	clean := strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', ' ', '\t':
			return -1
		}
		return r
	}, s)
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if dec, err := enc.DecodeString(clean); err == nil {
			return string(dec), nil
		}
	}
	return "", fmt.Errorf("base64 decode failed")
}

func intVal(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
	case string:
		if i, err := strconv.Atoi(n); err == nil {
			return i
		}
	}
	return 0
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}
