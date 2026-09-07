// Package execx unifies external command execution: CombinedOutput + verbatim echo at
// the trace level (zero obfuscation).
package execx

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/deadship2003/panoxy/internal/logx"
)

// Run executes a command and returns its combined output; on failure the error carries
// the output (lesson learned: the kernel logs to stdout, so watching only stderr means
// "silent failure").
func Run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	logx.DebugCmd(name, args, string(out), err)
	return string(out), err
}

// RunOK requires success and returns a contextual error on failure.
func RunOK(what, name string, args ...string) (string, error) {
	out, err := Run(name, args...)
	if err != nil {
		return out, fmt.Errorf("%s failed: %s", what, strings.TrimSpace(out))
	}
	return out, nil
}

// RunShell executes a shell line (for idempotent command chains containing ||; only ever
// fed constant-built commands, so there is no injection surface).
func RunShell(line string) (string, error) {
	out, err := exec.Command("sh", "-c", line).CombinedOutput()
	logx.DebugCmd("sh", []string{"-c", line}, string(out), err)
	return string(out), err
}
