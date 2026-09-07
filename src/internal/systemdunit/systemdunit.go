// Package systemdunit renders/installs/manages systemd units and detects bash-era legacy leftovers.
//
// Lifecycle primitives follow LIF-001 strict separation: Start/StopSvc/Restart touch only the
// current instance; Enable/Disable change only the auto-start registration (plus arming the
// daily upgrade timer, which is registration-adjacent and never starts/stops the service).
package systemdunit

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/deadship2003/panoxy/internal/asset"
	"github.com/deadship2003/panoxy/internal/constants"
	"github.com/deadship2003/panoxy/internal/execx"
	"github.com/deadship2003/panoxy/internal/paths"

	"gopkg.in/yaml.v3"
)

// Unit names derive from the program name (they follow a compile-time ProgName injection).
var (
	unitMain    = constants.ProgName + ".service"
	unitUpgrade = constants.ProgName + "-upgrade.service"
	unitTimer   = constants.ProgName + "-upgrade.timer"
)

// UnitNames lists every unit this program owns (uninstall/cleanup iterate it).
func UnitNames() []string { return []string{unitMain, unitUpgrade, unitTimer} }

// Render generates the three unit bodies (the service unit depends on the current mode).
func Render(p paths.Paths, mode string) (map[string]string, error) {
	svc, err := asset.RenderService(asset.UnitData{
		Mode:      mode,
		Prog:      constants.ProgName,
		EnvPrefix: constants.EnvPrefix(),
		Conf:      p.Conf,
		Root:      p.Root,
		UiDir:     p.UiDir,
		Cli:       p.Cli,
	})
	if err != nil {
		return nil, err
	}
	us, err := asset.RenderUpgradeService(p.Cli, p.Root)
	if err != nil {
		return nil, err
	}
	ut, err := asset.RenderUpgradeTimer()
	if err != nil {
		return nil, err
	}
	return map[string]string{
		unitMain:    svc,
		unitUpgrade: us,
		unitTimer:   ut,
	}, nil
}

// Write writes the units and runs daemon-reload. Idempotent; re-installing over an
// existing installation overwrites and resets the unit configuration (LIF-001 rule 2:
// no automatic start/enable happens here).
func Write(p paths.Paths, mode string) error {
	units, err := Render(p, mode)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.UnitDir, 0o755); err != nil {
		return fmt.Errorf("failed to create the unit directory: %w", err)
	}
	for name, content := range units {
		dst := filepath.Join(p.UnitDir, name)
		if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
			return fmt.Errorf("failed to write %s: %w", dst, err)
		}
	}
	_, _ = execx.Run("systemctl", "daemon-reload")
	return nil
}

// Remove deletes the units and runs daemon-reload (idempotent).
func Remove(p paths.Paths) {
	for _, name := range []string{unitMain, unitUpgrade, unitTimer} {
		os.Remove(filepath.Join(p.UnitDir, name))
	}
	_, _ = execx.Run("systemctl", "daemon-reload")
}

// ---- transient control (current instance only; never touches registration) ----

// Start starts the service for the current boot only.
func Start() error {
	_, err := execx.RunOK("start service", "systemctl", "start", unitMain)
	return err
}

// StopSvc stops the running instance; auto-start registration is left untouched.
func StopSvc() error {
	_, err := execx.RunOK("stop service", "systemctl", "stop", unitMain)
	return err
}

// Restart restarts the service (the unit's ExecStop/ExecStartPost re-load the firewall,
// self-healing kill -9 leftovers). Enablement is not changed.
func Restart() error {
	_, err := execx.RunOK("restart service", "systemctl", "restart", unitMain)
	return err
}

// ---- persistent auto-start registration (never starts/stops the service) ----

// Enable registers auto-start on boot for the service and the upgrade timer, and arms
// the timer (a timer only fires once started; arming it does not touch the service).
func Enable() error {
	if _, err := execx.RunOK("enable service", "systemctl", "enable", unitMain, unitTimer); err != nil {
		return err
	}
	_, err := execx.RunOK("arm upgrade timer", "systemctl", "start", unitTimer)
	return err
}

// Disable removes the auto-start registration and disarms the upgrade timer; a running
// service keeps running (stop it explicitly with StopSvc).
func Disable() error {
	if _, err := execx.RunOK("disarm upgrade timer", "systemctl", "stop", unitTimer); err != nil {
		return err
	}
	_, err := execx.RunOK("disable service", "systemctl", "disable", unitMain, unitTimer)
	return err
}

// Enabled reports whether the service is registered for auto-start.
func Enabled() bool {
	out, _ := execx.Run("systemctl", "is-enabled", unitMain)
	return strings.TrimSpace(out) == "enabled"
}

// ---- status introspection (source of truth = the init system) ----

// IsActive reports whether the service is in the active state (derived from Active; single source of truth).
func IsActive() bool { return Active() == "active" }

// Installed reports whether the main unit file has been written (init/deploy ran before);
// start/stop/restart use it to fail with a friendly message instead of systemctl's raw
// "unit not found".
func Installed(p paths.Paths) bool {
	_, err := os.Stat(filepath.Join(p.UnitDir, unitMain))
	return err == nil
}

// Active returns the service state string (active/inactive/failed...).
func Active() string {
	out, _ := execx.Run("systemctl", "is-active", unitMain)
	return strings.TrimSpace(out)
}

// StatusInfo carries the LIF-001 fixed status fields, sourced from the init system.
type StatusInfo struct {
	Installed     bool
	Enabled       bool
	Running       bool
	PID           int
	UptimeSeconds int64
	LastExitCode  int
	LastError     string
	RestartCount  int
}

// Status collects the service status snapshot (system scope, systemd).
func Status(p paths.Paths) StatusInfo {
	info := StatusInfo{Installed: Installed(p)}
	if !info.Installed {
		return info
	}
	info.Enabled = Enabled()
	props := ShowProps("LoadState", "ActiveState", "MainPID", "ActiveEnterTimestamp",
		"ExecMainStatus", "Result", "NRestarts")
	info.Running = props["ActiveState"] == "active"
	info.PID, _ = strconv.Atoi(props["MainPID"])
	info.UptimeSeconds = ParseUptime(props["ActiveEnterTimestamp"])
	info.LastExitCode, _ = strconv.Atoi(props["ExecMainStatus"])
	info.RestartCount, _ = strconv.Atoi(props["NRestarts"])
	if r := props["Result"]; r != "" && r != "success" {
		info.LastError = r
	}
	return info
}

// ShowProps queries `systemctl show` for the given properties and returns them as a map.
// Missing/empty properties (e.g. under a test shim) simply stay absent.
func ShowProps(props ...string) map[string]string {
	args := []string{"show", unitMain}
	for _, p := range props {
		args = append(args, "-p", p)
	}
	out, _ := execx.Run("systemctl", args...)
	return ParseShow(out)
}

// ParseShow parses "Key=Value" lines of `systemctl show` output.
func ParseShow(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if i := strings.IndexByte(line, '='); i > 0 {
			m[line[:i]] = line[i+1:]
		}
	}
	return m
}

// ParseUptime converts a systemd timestamp ("Sun 2026-09-07 17:44:00 CST") to elapsed
// seconds. The weekday prefix and zone suffix are locale-dependent, so only the
// local-time core is parsed — good enough for a display field; unparseable input yields 0.
func ParseUptime(ts string) int64 {
	f := strings.Fields(ts)
	if len(f) < 3 {
		return 0
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", f[1]+" "+f[2], time.Local)
	if err != nil {
		return 0
	}
	return int64(time.Since(t).Seconds())
}

// UnitEnv extracts the Environment= assignments from the installed main unit — the
// environment-alignment source for the LIF-002 foreground debug run. A missing unit
// yields nil (the caller then runs with the current environment). The unit sets no
// WorkingDirectory (ExecStart paths are absolute), so env is all that needs restoring.
func UnitEnv(p paths.Paths) map[string]string {
	b, err := os.ReadFile(filepath.Join(p.UnitDir, unitMain))
	if err != nil {
		return nil
	}
	m := map[string]string{}
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if !strings.HasPrefix(l, "Environment=") {
			continue
		}
		v := strings.Trim(strings.TrimPrefix(l, "Environment="), `"`)
		if i := strings.IndexByte(v, '='); i > 0 {
			m[v[:i]] = v[i+1:]
		}
	}
	return m
}

// DetectLegacy detects bash-version deployment leftovers: an old unit containing
// resolvectl, or a config with tun dns-hijack. A non-empty return describes the residue
// found (deploy aborts with manual cleanup guidance accordingly).
func DetectLegacy(p paths.Paths) string {
	if b, err := os.ReadFile(filepath.Join(p.UnitDir, unitMain)); err == nil {
		if strings.Contains(string(b), "resolvectl") {
			return "systemd unit contains resolvectl (bash legacy deployment)"
		}
	}
	if b, err := os.ReadFile(p.Conf); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			t := strings.TrimSpace(l)
			if strings.HasPrefix(t, "#") {
				continue // wording in comments does not count (the new template's comments mention historical fields)
			}
			if strings.HasPrefix(t, "dns-hijack:") {
				return "/etc/clash.yaml contains tun dns-hijack (bash legacy config)"
			}
		}
	}
	return ""
}

// PortCheck is a pre-start preflight: parse the actually deployed config's listen ports
// and check each for occupancy. A stale instance holding a port is the most common root
// cause of "service start failed" (the new kernel cannot bind).
func PortCheck(confPath string) error {
	var c struct {
		MixedPort          int    `yaml:"mixed-port"`
		SocksPort          int    `yaml:"socks-port"`
		TproxyPort         int    `yaml:"tproxy-port"`
		ExternalController string `yaml:"external-controller"`
		DNS                struct {
			Listen string `yaml:"listen"`
		} `yaml:"dns"`
	}
	if b, err := os.ReadFile(confPath); err == nil {
		yaml.Unmarshal(b, &c) // on parse failure skip the preflight (never blocks the main flow)
	}
	why := map[int]string{
		c.MixedPort:                    "mixed-port",
		c.SocksPort:                    "socks-port",
		c.TproxyPort:                   "tproxy-port",
		portTail(c.ExternalController): "external-controller (web UI/API)",
		portTail(c.DNS.Listen):         "DNS listen",
	}
	var list []string
	for p, w := range why {
		if p <= 0 {
			continue
		}
		out, _ := execx.Run("sh", "-c",
			fmt.Sprintf("ss -tlnup 2>/dev/null | grep -qE ':%d\\b' && echo busy", p))
		if strings.TrimSpace(out) != "" {
			list = append(list, fmt.Sprintf("%d(%s)", p, w))
		}
	}
	if len(list) > 0 {
		hint := ""
		if pout, _ := execx.Run("sh", "-c", fmt.Sprintf("pgrep -af '%s run' | head -3", constants.ProgName)); strings.TrimSpace(pout) != "" {
			hint = "\nrunning " + constants.ProgName + " detected:\n" + pout + "→ old deployment not cleaned up: first sudo " + constants.ProgName + " uninstall (old version) / stop the old instance, then deploy\n"
		}
		return fmt.Errorf("port already in use: %s%s", strings.Join(list, ", "), hint)
	}
	return nil
}

func portTail(addr string) int {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		n := 0
		for _, ch := range addr[i+1:] {
			if ch < '0' || ch > '9' {
				return 0
			}
			n = n*10 + int(ch-'0')
		}
		return n
	}
	return 0
}
