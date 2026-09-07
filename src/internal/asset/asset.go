// Package asset embeds resources: the systemd unit templates and the full mihomo config
// template (tun/tproxy dual-mode variants).
package asset

import (
	"bytes"
	"embed"
	"fmt"
	"text/template"

	"github.com/deadship2003/panoxy/internal/constants"
)

//go:embed service.tpl upgrade-service.tpl upgrade-timer.tpl config.tpl
var files embed.FS

// UnitData carries the fields needed to render the main service unit.
type UnitData struct {
	Mode                   string // tun / tproxy (Description only)
	Prog, EnvPrefix        string // program name / env prefix (follows the compile-time ProgName injection)
	Conf, Root, UiDir, Cli string
}

func render(name string, data any) (string, error) {
	raw, err := files.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("embedded asset missing: %s: %w", name, err)
	}
	t, err := template.New(name).Parse(string(raw))
	if err != nil {
		return "", fmt.Errorf("failed to parse template %s: %w", name, err)
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return "", fmt.Errorf("failed to render template %s: %w", name, err)
	}
	return b.String(), nil
}

// RenderService renders <Prog>.service (i.e. panoxy.service; no resolvectl logic at all;
// fw apply unconditionally CleanAll's first, so kill -9 leftovers self-heal on restart).
func RenderService(d UnitData) (string, error) { return render("service.tpl", d) }

// RenderUpgradeService / RenderUpgradeTimer render the daily auto-upgrade units.
func RenderUpgradeService(cli, root string) (string, error) {
	return render("upgrade-service.tpl", map[string]string{
		"Cli": cli, "Root": root,
		"Prog": constants.ProgName, "EnvPrefix": constants.EnvPrefix(),
	})
}
func RenderUpgradeTimer() (string, error) {
	return render("upgrade-timer.tpl", map[string]string{
		"Prog": constants.ProgName, "EnvPrefix": constants.EnvPrefix(),
	})
}

// ConfigData carries the fields needed to render the mihomo config.
type ConfigData struct {
	Prog        string // program name (rendered into the config header comment; follows the compile-time ProgName injection)
	MixedPort   int
	ApiPort     int
	Secret      string
	TProxy      bool // true = the TPROXY variant (no tun section, adds tproxy-port)
	TproxyPort  int
	DnsPort     int
	RoutingMark int
}

// DefaultConfigData holds the usual defaults.
func DefaultConfigData() ConfigData {
	return ConfigData{
		Prog:        constants.ProgName,
		Secret:      constants.DefSecret,
		MixedPort:   constants.MixedPortDef,
		ApiPort:     constants.ApiPortDef,
		TProxy:      false,
		TproxyPort:  constants.TproxyPort,
		DnsPort:     constants.DnsListenPort,
		RoutingMark: constants.MarkSelf,
	}
}

// RenderConfig renders the full mihomo config (all default groups/rules included,
// inheriting the bash-version v0.1.4 assets).
func RenderConfig(d ConfigData) (string, error) { return render("config.tpl", d) }

// TunParams and TunRouteExclude are the single source of truth for the TUN-mode config
// block: the config.tpl rendering and config.SetMode's incremental rebuild both take
// their values from here, preventing the two hardcodings from drifting (change them
// here; the template follows).
var (
	TunParams = [][2]string{
		{"enable", "true"},
		{"stack", "system"},
		{"auto-route", "true"},
		{"auto-detect-interface", "true"},
		{"strict-route", "true"},
		{"mtu", "1500"},
	}
	TunRouteExclude = []string{"127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}
)
