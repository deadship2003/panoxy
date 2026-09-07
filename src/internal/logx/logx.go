// Package logx implements leveled logging: INFO by default (one line per step, keeping
// the bash-version rhythm); --verbose shows per-step detail; --trace is the full
// passthrough (external commands echoed verbatim, mihomo API request/response, file
// writes). All logs go to stderr; stdout stays reserved for machine output like --json.
//
// Lesson in the background: the bash version once lost an entire script's stderr to an
// exec redirection and kernel logs on stdout were eaten by >/dev/null, costing a whole
// debugging round — in the Go version, external-call I/O is unobfuscated at the trace
// level.
package logx

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/deadship2003/panoxy/internal/constants"
)

const (
	LevelInfo = iota
	LevelVerbose
	LevelDebug
)

var level = LevelInfo

// SetLevel is injected from the cobra persistent flags.
func SetLevel(l int) { level = l }

func stamp(lv string, msg string) string {
	return fmt.Sprintf("[%s] %s %-7s %s", constants.ProgName, time.Now().Format("2006-01-02 15:04:05"), lv, msg)
}

func Info(format string, a ...any) {
	fmt.Fprintln(os.Stderr, stamp("INFO", fmt.Sprintf(format, a...)))
}

// Step is the --verbose level: transaction steps (e.g. "[3/7] write config (backup -> .panoxy-bak, i.e. the derived BackupSuffix)").
func Step(format string, a ...any) {
	if level >= LevelVerbose {
		fmt.Fprintln(os.Stderr, stamp("STEP", fmt.Sprintf(format, a...)))
	}
}

// Debug is the full level: input/output of external commands and APIs echoed verbatim.
func Debug(format string, a ...any) {
	if level >= LevelDebug {
		fmt.Fprintln(os.Stderr, stamp("DEBUG", fmt.Sprintf(format, a...)))
	}
}

// DebugCmd echoes an external command's full command line and output (multiline output
// is preserved verbatim).
func DebugCmd(cmd string, args []string, out string, err error) {
	if level < LevelDebug {
		return
	}
	c := strings.TrimSpace(cmd + " " + strings.Join(args, " "))
	o := strings.TrimRight(out, "\n")
	line := "$ " + c
	if o != "" {
		line += "\n" + indent(o)
	}
	if err != nil {
		line += "\n  (exit: " + err.Error() + ")"
	}
	fmt.Fprintln(os.Stderr, stamp("CMD", line))
}

func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}

// Warn/Error go to stderr and always display.
func Warn(format string, a ...any) {
	fmt.Fprintln(os.Stderr, stamp("WARN", fmt.Sprintf(format, a...)))
}

func Error(format string, a ...any) {
	fmt.Fprintln(os.Stderr, stamp("ERROR", fmt.Sprintf(format, a...)))
}
