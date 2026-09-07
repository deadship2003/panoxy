// Package paths resolves runtime paths: defaults + environment-variable overrides
// (<PROG>_ROOT etc.), so sandboxes and tests can reuse the bash-version experience.
package paths

import (
	"os"
	"path/filepath"

	"github.com/deadship2003/panoxy/internal/constants"
)

type Paths struct {
	Root        string // /opt/<prog>
	UiDir       string
	UiStamp     string
	State       string // /opt/<prog>/<prog>.yaml: the program's own state (proxy-mode etc.)
	Conf        string // /etc/<prog>.yaml: the mihomo config (single source of truth)
	DefaultConf string // /opt/<prog>/config.default.yaml: pristine default-template copy (merge-conf rebuild baseline)
	UnitDir     string
	Cli         string
	ManGz       string
	Sysctl      string
	Lock        string
	LastUp      string
	Proxies     string // subscription cache dir
	RuleProv    string // rule-provider cache dir
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Get returns the path set for the current environment (re-resolved on every call, so
// env overrides take effect immediately).
func Get() Paths {
	pfx := constants.EnvPrefix()
	root := env(pfx+"_ROOT", constants.DefRootDir)
	return Paths{
		Root:        root,
		UiDir:       filepath.Join(root, "ui", "official"),
		UiStamp:     filepath.Join(root, "ui", ".official.version"),
		State:       env(pfx+"_STATE", filepath.Join(root, constants.ProgName+".yaml")),
		Conf:        env(pfx+"_CONF", constants.DefConfPath),
		DefaultConf: filepath.Join(root, "config.default.yaml"),
		UnitDir:     env(pfx+"_UNIT_DIR", constants.DefUnitDir),
		Cli:         env(pfx+"_CLI", constants.DefCliDest),
		ManGz:       env(pfx+"_MAN", constants.DefManGz),
		Sysctl:      env(pfx+"_SYSCTL", constants.DefSysctlFile),
		Lock:        env(pfx+"_LOCK", constants.DefLockFile),
		LastUp:      filepath.Join(root, ".last-upgrade"),
		Proxies:     filepath.Join(root, "proxies"),
		RuleProv:    filepath.Join(root, "rule_provider"),
	}
}
