// Package statemode reads/writes panoxy's own state file (/opt/panoxy/panoxy.yaml):
// settings managed programmatically by the CLI, such as proxy-mode. Users never hand-edit
// it; the default is tun.
package statemode

import (
	"os"

	"gopkg.in/yaml.v3"
)

type State struct {
	ProxyMode string `yaml:"proxy-mode"` // tun | tproxy
}

// Read reads the state; a missing/corrupt file always yields the default (tun) and no
// error (the read path never blocks the flow).
func Read(path string) string {
	return normalize(readState(path).ProxyMode)
}

// readState returns the full state structure.
func readState(path string) State {
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
