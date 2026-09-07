// Package core wraps the mihomo kernel (Alpha branch) in-process, equivalent to the
// external mihomo binary.
//
// Since M2 the lifecycle is in-process: the systemd unit's ExecStart runs Run directly
// instead of launching an external binary. Run corresponds strictly to the startup
// section + signal loop of upstream main.go; Validate corresponds to the -t check.
// Adding or removing logic on our own is forbidden (details in [[mihomo-alpha-embedding]]).
package core

import (
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"go.uber.org/automaxprocs/maxprocs"

	"github.com/metacubex/mihomo/component/updater"
	"github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/hub"
	"github.com/metacubex/mihomo/hub/executor"
	"github.com/metacubex/mihomo/log"
)

// Run starts the kernel in-process and blocks, equivalent to mihomo main()'s startup
// section + signal loop:
// PreferGo -> maxprocs -> SetHomeDir -> SetConfig -> config.Init -> hub.Parse(nil) ->
// geo auto-update -> signal loop (SIGHUP re-reads the file / SIGINT/SIGTERM ->
// executor.Shutdown). An empty configPath falls back to homeDir/config.yaml (equivalent
// to the -f default). opts passes through overrides such as external-ui.
func Run(homeDir, configPath string, opts ...hub.Option) error {
	net.DefaultResolver.PreferGo = true
	_, _ = maxprocs.Set(maxprocs.Logger(func(string, ...any) {}))

	if homeDir != "" {
		C.SetHomeDir(homeDir)
	}
	if configPath == "" {
		configPath = filepath.Join(C.Path.HomeDir(), C.Path.Config())
	}
	C.SetConfig(configPath)
	if err := config.Init(C.Path.HomeDir()); err != nil {
		return err
	}
	if err := hub.Parse(nil, opts...); err != nil {
		return err
	}
	if updater.GeoAutoUpdate() {
		updater.RegisterGeoUpdater()
	}

	defer executor.Shutdown()
	termSign := make(chan os.Signal, 1)
	hupSign := make(chan os.Signal, 1)
	signal.Notify(termSign, syscall.SIGINT, syscall.SIGTERM)
	signal.Notify(hupSign, syscall.SIGHUP)
	for {
		select {
		case <-termSign:
			return nil
		case <-hupSign:
			if err := hub.Parse(nil, opts...); err != nil {
				log.Errorln("Parse config error: %s", err.Error())
			}
		}
	}
}

// Validate is equivalent to mihomo -t: parse-and-check the config only, starting no
// listeners. homeDir resolves relative resources such as geodata (GeoSite.dat/GeoIP.dat);
// empty means the default home.
func Validate(homeDir string, configBytes []byte) error {
	if homeDir != "" {
		C.SetHomeDir(homeDir)
	}
	_, err := executor.ParseWithBytes(configBytes)
	return err
}

// Version returns the embedded kernel's version (upstream Alpha branch C.Version; after
// the fusion the kernel is panoxy itself).
func Version() string { return C.Version }
