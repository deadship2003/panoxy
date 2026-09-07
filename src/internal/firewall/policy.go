package firewall

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/deadship2003/panoxy/internal/constants"
	"github.com/deadship2003/panoxy/internal/logx"
)

// ---- TPROXY policy routing (fwmark -> table -> local route; one set per v4/v6) ----
// The rules live in the main namespace (no netns); teardown/CleanAll clean idempotently.

// TproxyPolicyAdd loads the policy routing (called on TPROXY-mode Apply; idempotently
// cleans the old rules first to prevent accumulation).
func TproxyPolicyAdd() error {
	return runIps(tproxyPolicyCmds(true, constants.MarkTproxy, constants.TproxyTable), "load TPROXY policy routing")
}

// TproxyPolicyDel cleans the policy routing (idempotent).
func TproxyPolicyDel() error {
	return runIps(tproxyPolicyCmds(false, constants.MarkTproxy, constants.TproxyTable), "clean TPROXY policy routing")
}

func tproxyPolicyCmds(add bool, mark, table int) [][]string {
	t := fmt.Sprint(table)
	m := fmt.Sprint(mark)
	act := "del"
	if add {
		act = "add"
	}
	flush := "flush"
	return [][]string{
		{"ip", "rule", act, "fwmark", m, "lookup", t},
		{"ip", "route", flush, "table", t},
		{"ip", "-6", "rule", act, "fwmark", m, "lookup", t},
		{"ip", "-6", "route", flush, "table", t},
		{"ip", "route", "add", "local", "0.0.0.0/0", "dev", "lo", "table", t},
		{"ip", "-6", "route", "add", "local", "::/0", "dev", "lo", "table", t},
	}
}

func runIps(cmds [][]string, what string) error {
	for _, c := range cmds {
		out, err := exec.Command(c[0], c[1:]...).CombinedOutput()
		logx.DebugCmd(c[0], c[1:], string(out), err)
		// Idempotency tolerance: "does not exist" while cleaning and "already exists"
		// while loading both count as success. Note that ip reports "No such file or
		// directory" (RTNETLINK ENOENT) for a missing rule/table — guaranteed on a
		// fresh machine; skipping this tolerance made fw apply fail and the service
		// get declared dead (observed in practice).
		if err != nil && !tolerantError(string(out)) {
			return fmt.Errorf("%s failed: %s", what, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// tolerantError decides whether an error output is an ignorable idempotency case.
func tolerantError(out string) bool {
	for _, s := range []string{
		"File exists",               // already exists while adding
		"No such process",           // some del implementations
		"No such file or directory", // missing rule/table (RTNETLINK ENOENT)
		"does not exist",            // newer iproute2: FIB rule does not exist
		"Cannot find device",
	} {
		if strings.Contains(out, s) {
			return true
		}
	}
	return false
}
