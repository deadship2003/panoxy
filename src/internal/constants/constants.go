// Package constants defines the panoxy global constants: directory layout, firewall
// identities, ports.
// Layout principle: /opt/<ProgName> is the self-contained data home; /etc/<ProgName>.yaml
// is the admin-hand-edited system-level config (the single source of truth).
package constants

import "strings"

// ProgName is the program name, injectable at build time via
//
//	-ldflags "-X github.com/deadship2003/panoxy/internal/constants.ProgName=myproxy"
//
// Once injected, every derived artifact follows it: paths, unit names, firewall table and
// chains, env prefix, state file, backup suffixes (see EnvPrefix). The default is "panoxy"
// (the Makefile PROG variable / build.sh --prog flag default to the same name).
var ProgName = "panoxy"

// EnvPrefix returns the environment-variable prefix: PANOXY_ -> <PROG>_ (uppercased, - to _).
func EnvPrefix() string { return strings.ToUpper(strings.ReplaceAll(ProgName, "-", "_")) }

const (
	Version = "0.0.1"

	// Defaults below; tests/sandboxes can override them via env vars (see internal/paths).
	DefUnitDir = "/etc/systemd/system"

	// Firewall: a dedicated table, never reusing the system nat/filter tables; startup
	// runs CleanAll unconditionally, which is what makes restart self-healing.
	NftFamily = "inet"

	MarkSelf    = 6666 // kernel routing-mark: tags the kernel's own outbound traffic so the firewall can exempt it and prevent a DNS loop (do not change; coupled with the config template)
	MarkTproxy  = 1    // TPROXY-mode traffic mark
	TproxyTable = 100  // TPROXY policy-routing table number
	TproxyPort  = 7893 // kernel tproxy-port

	DnsListenPort = 1053 // kernel DNS listen port (the firewall redirect target)
	MixedPortDef  = 33833
	ApiPortDef    = 9999
	DefSecret     = "deadship" // default panel/API secret (init/deploy --secret default; same source as the API client fallback)
)

// Embedded-kernel upstream baseline: the mihomo Alpha branch commit locked at subtree
// import time (i.e. what src/third_party/mihomo contains). The upstream command uses
// this to detect new upstream commits; after a subtree sync, update this constant and
// third_party/mihomo/.git-subtree-source together.
const (
	UpstreamRepo         = "https://github.com/MetaCubeX/mihomo"
	UpstreamBranch       = "Alpha"
	UpstreamMihomoCommit = "65287f0"
)

// Defaults below derive from ProgName (they follow a compile-time ProgName injection).
var (
	DefRootDir    = "/opt/" + ProgName
	DefConfPath   = "/etc/" + ProgName + ".yaml"
	DefCliDest    = "/usr/local/bin/" + ProgName
	DefManGz      = "/usr/local/share/man/man1/" + ProgName + ".1.gz"
	DefSysctlFile = "/etc/sysctl.d/99-" + ProgName + ".conf"
	DefLockFile   = "/run/" + ProgName + ".lock"
	NftTable      = ProgName
)

// BackupSuffix / PremergeSuffix are the backup file suffixes, derived from the program
// name (shared by the config transaction backup and the merge pre-merge backup).
func BackupSuffix() string   { return "." + ProgName + "-bak" }
func PremergeSuffix() string { return "." + ProgName + "-premerge" }
