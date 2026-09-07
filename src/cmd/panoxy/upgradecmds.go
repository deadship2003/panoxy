package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/deadship2003/panoxy/internal/constants"
	"github.com/deadship2003/panoxy/internal/logx"
	"github.com/deadship2003/panoxy/internal/mihomoapi"
	"github.com/deadship2003/panoxy/internal/paths"
	"github.com/deadship2003/panoxy/internal/upgrade"
)

// runUpgrade upgrades the metacubexd web UI; --check is a dry-run, --ui-version pins a version.
// The mihomo kernel is fused into the CLI, so there is no separate kernel/CLI to upgrade here —
// a new CLI version is installed by compiling a new binary and running `sudo <prog> redeploy`
// (or a plain copy to the CLI path). .last-upgrade is updated only on success.
func runUpgrade(cmd *cobra.Command, args []string) error {
	return withRootLock(func(p paths.Paths) error { return runUpgradeBody(p, cmd, args) })
}

func runUpgradeBody(p paths.Paths, cmd *cobra.Command, args []string) error {
	check, _ := cmd.Flags().GetBool("check")
	uiVer, _ := cmd.Flags().GetString("ui-version")
	forceUI, _ := cmd.Flags().GetBool("ui") // --ui: manual (re)upgrade even when already latest

	var err error
	api := mihomoapi.NewFromConf(p.Conf)
	proxy := api.Proxy()

	curUI := ""
	if b, err := os.ReadFile(p.UiStamp); err == nil {
		curUI = strings.TrimSpace(string(b))
	}

	var latestUI string
	want := uiVer
	if want == "" {
		if latestUI, err = upgrade.Latest("MetaCubeX/metacubexd", proxy); err != nil {
			if !check {
				logx.Warn("UI version query failed, skipping this time: %v", err)
			}
		} else {
			want = latestUI
		}
	}
	if check {
		fmt.Printf("UI:    current %s latest %s → %s\n", orQ(curUI), orQ(want), action(curUI, want))
		return nil
	}
	if want != "" && (want != curUI || forceUI) {
		if err := uiUpgrade(p, proxy, want); err != nil {
			return err
		}
	} else {
		logx.Info("UI is already latest %s", curUI)
	}
	os.WriteFile(p.LastUp, []byte(time.Now().Format("2006-01-02 15:04:05")+"\n"), 0o644)
	return nil
}

func orQ(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

func action(cur, want string) string {
	if cur == want {
		return "up to date"
	}
	if want == "" {
		return "query failed"
	}
	return "upgradable"
}

// uiUpgrade: download compressed-dist.tgz → swap dir → probe /ui/ → restore the old dir on failure.
func uiUpgrade(p paths.Paths, proxy, want string) error {
	logx.Info("UI upgrade: → %s", want)
	tmp, _ := os.MkdirTemp("", constants.ProgName+"-ui-")
	defer os.RemoveAll(tmp)
	tgz := filepath.Join(tmp, "dist.tgz")
	url := "https://github.com/MetaCubeX/metacubexd/releases/download/" + want + "/compressed-dist.tgz"
	if err := upgrade.Download(url, proxy, tgz); err != nil {
		return fmt.Errorf("UI download failed: %w", err)
	}
	if err := runExtractTgz(tgz, filepath.Join(tmp, "x")); err != nil {
		return fmt.Errorf("UI unpack failed: %w", err)
	}
	if !exists(filepath.Join(tmp, "x", "index.html")) {
		return fmt.Errorf("UI package is abnormal (no index.html)")
	}
	os.RemoveAll(p.UiDir + ".old")
	if exists(p.UiDir) {
		os.Rename(p.UiDir, p.UiDir+".old")
	}
	copyDir(filepath.Join(tmp, "x"), p.UiDir)
	os.WriteFile(p.UiStamp, []byte(want+"\n"), 0o644)
	if code := probeUI(p.Conf); code != "200" {
		os.RemoveAll(p.UiDir)
		os.Rename(p.UiDir+".old", p.UiDir)
		os.WriteFile(p.UiStamp, []byte("unknown\n"), 0o644)
		return fmt.Errorf("UI probe abnormal (http=%s), restored old version", code)
	}
	os.RemoveAll(p.UiDir + ".old")
	logx.Info("UI upgrade succeeded → %s", want)
	return nil
}
