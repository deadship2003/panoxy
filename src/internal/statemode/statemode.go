// Package statemode reads/writes panoxy's own state file (/opt/panoxy/panoxy.yaml):
// settings managed programmatically by the CLI, such as proxy-mode. Users never hand-edit
// it; the default is tun.
package statemode

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type State struct {
	ProxyMode string `yaml:"proxy-mode"` // tun | tproxy
}

// legacyStateName is the state file name used before the program-default rename
// (commit e5fb3fb: "Panoxy" -> "panoxy"). Hosts deployed in that era still carry
// the capital-P file in /opt/panoxy while the canonical name is panoxy.yaml.
const legacyStateName = "Panoxy.yaml"

// migrateLegacy adopts a leftover pre-rename state file as the canonical one,
// best-effort: on any failure Read() simply falls back to its default path.
func migrateLegacy(path string) {
	if _, err := os.Stat(path); err == nil {
		return // canonical state already exists
	}
	legacy := filepath.Join(filepath.Dir(path), legacyStateName)
	if _, err := os.Stat(legacy); err == nil {
		_ = os.Rename(legacy, path)
	}
}

// Read reads the state; a missing/corrupt file always yields the default (tun) and no
// error (the read path never blocks the flow).
func Read(path string) string {
	return normalize(readState(path).ProxyMode)
}

// readState returns the full state structure.
func readState(path string) State {
	migrateLegacy(path)
	var st State
	b, err := os.ReadFile(path)
	if err != nil {
		return State{ProxyMode: "tun"}
	}
	if err := yaml.Unmarshal(b, &st); err != nil {
		return State{ProxyMode: "tun"}
	}
	if st.ProxyMode == "" {
		st.ProxyMode = "tun"
	}
	return st
}

// Write writes the state atomically.
func Write(path string, st State) error {
	st.ProxyMode = normalize(st.ProxyMode)
	b, err := yaml.Marshal(&st)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func normalize(m string) string {
	if m == "tproxy" {
		return "tproxy"
	}
	return "tun"
}
