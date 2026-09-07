// Package tests is the Go e2e suite: it drives the compiled panoxy single binary (embedded
// kernel) through env-var path overrides, a fake systemctl and the in-process kernel,
// covering the full transaction chains of deploy / sub import / sub del / mode.
//
// Safety constraint: the dev machine never boots a tun instance (auto-route would rewrite
// the host routing table) — e2e configs always strip the tun section; real tun/tproxy
// gateway boot is verified on the gateway machine.
package tests

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/deadship2003/panoxy/internal/asset"
	"github.com/deadship2003/panoxy/internal/constants"
)

var (
	bin    string // the compiled panoxy (single binary, embedded kernel)
	goTool string
)

func TestMain(m *testing.M) {
	var err error
	goTool, err = exec.LookPath("go")
	if err != nil {
		fmt.Println("SKIP: no go toolchain")
		os.Exit(0)
	}
	// geo source resolution: GEO_SRC > /opt/<ProgName> > /opt/panixy (legacy installs) >
	// ~/panoxy-e2e > offline-package assets (keeps e2e working after /opt was cleaned)
	if os.Getenv("GEO_SRC") == "" {
		for _, c := range []string{
			filepath.Join("/opt", constants.ProgName),
			"/opt/panixy", // legacy leftover name from old deployments
			homeDir() + "/panoxy-e2e",
			constants.ProgName + "-V0.0.1-local-amd64/assets/geo",
		} {
			if _, err := os.Stat(filepath.Join(c, "GeoSite.dat")); err == nil {
				os.Setenv("GEO_SRC", c)
				break
			}
		}
	}
	dir, err := os.MkdirTemp("", constants.ProgName+"-e2e-bin-")
	if err != nil {
		os.Exit(1)
	}
	bin = filepath.Join(dir, constants.ProgName)
	out, err := exec.Command(goTool, "build", "-o", bin, "../cmd/panoxy").CombinedOutput()
	if err != nil {
		fmt.Printf("SKIP: building panoxy failed (deps not fetched?): %s\n%s", err, out)
		os.Exit(0)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// env is one sandbox: path overrides + fake systemctl/ip/sysctl/nft + random ports.
type env struct {
	t       *testing.T
	dir     string
	root    string
	conf    string
	apiPort int
	mixPort int
	dnsPort int
}

func homeDir() string { h, _ := os.UserHomeDir(); return h }

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	e := &env{
		t: t, dir: dir,
		root:    filepath.Join(dir, "root"),
		conf:    filepath.Join(dir, "clash.yaml"),
		apiPort: freePort(t), mixPort: freePort(t), dnsPort: freePort(t),
	}
	os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	// Fake systemctl: start/stop/restart drive the sandbox kernel (pids tracked in PIDF),
	// is-active judges by pid liveness; enable/disable are registration-only no-ops.
	shim := filepath.Join(dir, "bin", "systemctl")
	pidf := filepath.Join(dir, "pid")
	pfx := constants.EnvPrefix()
	os.WriteFile(shim, []byte(fmt.Sprintf(`#!/bin/sh
PIDF=%s
PROG=%s
start_mh() {
  # The shim plays systemd, so it sets what systemd sets: INVOCATION_ID marks the kernel
  # as deliberately spawned (the run command's single-instance guard skips its probe).
  INVOCATION_ID=e2e nohup "$%s_CLI" run >> "$%s_ROOT/run.log" 2>&1 9>&- &
  echo $! >> "$PIDF"
}
kill_mh() { while read p; do kill "$p" 2>/dev/null; done < "$PIDF" 2>/dev/null; : > "$PIDF"; }
alive_mh() { a=0; while read p; do kill -0 "$p" 2>/dev/null && a=1; done < "$PIDF" 2>/dev/null; [ "$a" = 1 ]; }
case "$1" in
  start)   [ "$2" = "$PROG.service" ] && start_mh ;;
  stop)    [ "$2" = "$PROG.service" ] && kill_mh ;;
  restart) [ "$2" = "$PROG.service" ] && { kill_mh; sleep 1; start_mh; } ;;
  enable|disable) : ;;
  is-active) alive_mh && echo active || { echo inactive; exit 3; } ;;
  is-enabled) echo disabled; exit 1 ;;
  show) if [ "$2" = "$PROG.service" ]; then
          if alive_mh; then echo "ActiveState=active"; echo "MainPID=$(tail -n 1 "$PIDF" 2>/dev/null)";
          else echo "ActiveState=inactive"; fi
          echo "Result=success"; echo "NRestarts=0"
        fi ;;
esac
exit 0
`, pidf, constants.ProgName, pfx, pfx)), 0o755)
	for _, name := range []string{"ip", "sysctl", "nft"} {
		os.WriteFile(filepath.Join(dir, "bin", name), []byte("#!/bin/sh\nexit 0\n"), 0o755)
	}
	// Reap sandbox kernel processes when the test ends, so nothing leaks.
	t.Cleanup(func() {
		if b, err := os.ReadFile(pidf); err == nil {
			for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
				if l != "" {
					syscall.Kill(atoi(l), syscall.SIGKILL)
				}
			}
		}
	})
	return e
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func (e *env) envOf() []string {
	pfx := constants.EnvPrefix()
	prog := constants.ProgName
	return append(os.Environ(),
		"PATH="+filepath.Join(e.dir, "bin")+":"+os.Getenv("PATH"),
		pfx+"_ROOT="+e.root,
		pfx+"_CONF="+e.conf,
		pfx+"_UNIT_DIR="+filepath.Join(e.dir, "units"),
		pfx+"_CLI="+filepath.Join(e.dir, "cli", prog),
		pfx+"_MAN="+filepath.Join(e.dir, "man", prog+".1.gz"),
		pfx+"_STATE="+filepath.Join(e.dir, "state.yaml"),
		pfx+"_SYSCTL="+filepath.Join(e.dir, "99.conf"),
		pfx+"_LOCK="+filepath.Join(e.dir, "lock"),
		fmt.Sprintf(pfx+"_API_PORT=%d", e.apiPort),
		fmt.Sprintf(pfx+"_PROXY_PORT=%d", e.mixPort),
		pfx+"_SECRET=e2esecret",
		pfx+"_ALLOW_NONROOT=1",
		pfx+"_SKIP_TPROXY_PROBE=1",
	)
}

func (e *env) cmd(args ...string) *exec.Cmd {
	cmd := exec.Command(bin, args...)
	cmd.Env = e.envOf()
	return cmd
}

// shim invokes the fake systemctl directly (to boot/stop the sandbox kernel from the test).
func (e *env) shim(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command(filepath.Join(e.dir, "bin", "systemctl"), args...)
	cmd.Env = e.envOf()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shim %v failed: %s", args, out)
	}
}

func (e *env) run(t *testing.T, args ...string) string {
	t.Helper()
	out, err := e.cmd(args...).CombinedOutput()
	if err != nil {
		t.Fatalf("panoxy %v failed:\n%s", args, out)
	}
	return string(out)
}

func (e *env) runFail(t *testing.T, args ...string) string {
	t.Helper()
	out, err := e.cmd(args...).CombinedOutput()
	if err == nil {
		t.Fatalf("panoxy %v unexpectedly succeeded:\n%s", args, out)
	}
	return string(out)
}

// exitCode runs a command and returns its process exit code.
func (e *env) exitCode(t *testing.T, args ...string) int {
	t.Helper()
	cmd := e.cmd(args...)
	_ = cmd.Run()
	return cmd.ProcessState.ExitCode()
}

// apiURL hits the sandbox kernel API directly.
func (e *env) apiURL(path string) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", e.apiPort, path)
}

// waitAPI waits for the sandbox kernel API to become ready.
func (e *env) waitAPI(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(e.apiURL("/version"))
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	log, _ := os.ReadFile(filepath.Join(e.root, "run.log"))
	t.Fatalf("sandbox kernel API not ready\n--- run.log ---\n%s", log)
}

// noTunConf renders the template with the tun section stripped (the dev machine never
// boots tun), random ports and a fixed secret.
func noTunConf(t *testing.T, api, mix, dns int, tproxy bool) string {
	t.Helper()
	d := asset.DefaultConfigData()
	d.ApiPort, d.MixedPort, d.DnsPort = api, mix, dns
	d.TProxy = tproxy
	d.Secret = "e2esecret"
	out, err := asset.RenderConfig(d)
	if err != nil {
		t.Fatal(err)
	}
	// Randomize the template's hard-coded http/socks ports (a real gateway installed on
	// this machine would collide with the fixed 9966/6699).
	out = strings.Replace(out, "port: 9966", fmt.Sprintf("port: %d", freePort(t)), 1)
	out = strings.Replace(out, "socks-port: 6699", fmt.Sprintf("socks-port: %d", freePort(t)), 1)
	// Strip the tun section (from "tun:" to the next top-level key).
	var b strings.Builder
	skip := false
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "tun:") {
			skip = true
			continue
		}
		if skip {
			if l != "" && l[0] != ' ' && l[0] != '#' {
				skip = false
			} else {
				continue
			}
		}
		b.WriteString(l + "\n")
	}
	return b.String()
}

// fakeSubServer emulates an airport: any path returns a Clash YAML with n nodes.
func fakeSubServer(t *testing.T, nodes int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		b.WriteString("proxies:\n")
		for i := 0; i < nodes; i++ {
			fmt.Fprintf(&b, "  - name: 'e2e-%d'\n    type: socks5\n    server: 127.0.0.1\n    port: 1080\n", i)
		}
		w.Write([]byte(b.String()))
	}))
	t.Cleanup(srv.Close)
	return srv
}
