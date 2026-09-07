package main

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/deadship2003/panoxy/internal/constants"
	"github.com/deadship2003/panoxy/internal/logx"
)

// runUpstream probes the mihomo Alpha upstream's latest commit and compares it with
// the embedded baseline — a hint only, it never auto-merges or touches source.
// Upstream syncing (subtree merge -> re-apply the panoxy trims -> regression) is done
// manually by the user + AI; this command only detects.
func runUpstream(cmd *cobra.Command, args []string) error {
	remote := constants.UpstreamRepo + ".git"
	ref := "refs/heads/" + constants.UpstreamBranch

	out, err := exec.Command("git", "ls-remote", remote, ref).Output()
	if err != nil {
		return fmt.Errorf("failed to probe upstream (needs git + access to %s): %w", constants.UpstreamRepo, err)
	}
	latest := firstField(string(out))
	if latest == "" {
		return fmt.Errorf("could not parse the upstream %s branch commit (output %q)", constants.UpstreamBranch, strings.TrimSpace(string(out)))
	}

	base := constants.UpstreamMihomoCommit
	fmt.Printf("embedded kernel: mihomo %s @ %s\n", constants.UpstreamBranch, shortSha(base))
	fmt.Printf("upstream latest: mihomo %s @ %s\n", constants.UpstreamBranch, shortSha(latest))

	if latest == base {
		logx.Info("up to date: the embedded kernel == the upstream %s branch HEAD", constants.UpstreamBranch)
		return nil
	}

	logx.Warn("upstream has moved: %s @ %s -> %s; syncing is recommended (a manual + AI subtree merge, this command never touches source)",
		constants.UpstreamBranch, shortSha(base), shortSha(latest))
	return nil
}

// shortSha returns the first 7 characters of a commit for readable display.
func shortSha(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// firstField returns the first whitespace-separated field (ls-remote prints "<sha>\t<refname>").
func firstField(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

func cmdUpstream() *cobra.Command {
	c := &cobra.Command{
		Use:   "upstream",
		Short: "check mihomo Alpha upstream for newer commits (hint only, never auto-merges)",
		Long: `Detect the latest commit on the mihomo Alpha branch and compare it against the embedded
kernel baseline. If upstream has moved, it prints a hint — it never auto-merges or modifies source.

The embedded kernel baseline is compiled in (internal/constants.UpstreamMihomoCommit); syncing
upstream is a manual + AI step (git subtree pull → re-apply panoxy trims → regression).`,
		Example: `  panoxy upstream   # prints "upstream has moved: Alpha @ 65287f0 -> <new>; syncing is recommended" when upstream moved`,
		RunE:    runUpstream,
	}
	return c
}
