package main

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/deadship2003/panoxy/internal/constants"
	"github.com/deadship2003/panoxy/internal/logx"
)

// runUpstream 探测 mihomo Alpha 上游最新 commit,与内嵌基线对比,仅提示、绝不自动合并/改源码。
// 上游同步(子树合并 → 重新应用 panoxy 裁剪 → 回归)由用户 + AI 人工完成,本命令只做探测。
func runUpstream(cmd *cobra.Command, args []string) error {
	remote := constants.UpstreamRepo + ".git"
	ref := "refs/heads/" + constants.UpstreamBranch

	out, err := exec.Command("git", "ls-remote", remote, ref).Output()
	if err != nil {
		return fmt.Errorf("探测上游失败(需 git + 可访问 %s):%w", constants.UpstreamRepo, err)
	}
	latest := firstField(string(out))
	if latest == "" {
		return fmt.Errorf("未能解析上游 %s 分支 commit(输出 %q)", constants.UpstreamBranch, strings.TrimSpace(string(out)))
	}

	base := constants.UpstreamMihomoCommit
	fmt.Printf("内嵌内核:mihomo %s @ %s\n", constants.UpstreamBranch, shortSha(base))
	fmt.Printf("上游最新:mihomo %s @ %s\n", constants.UpstreamBranch, shortSha(latest))

	if latest == base {
		logx.Info("已是最新:内嵌内核 == 上游 %s 分支 HEAD", constants.UpstreamBranch)
		return nil
	}

	logx.Warn("发现上游更新:%s @ %s → %s,建议同步(需人工 + AI 完成 subtree 合并,本命令不自动改源码)",
		constants.UpstreamBranch, shortSha(base), shortSha(latest))
	return nil
}

// shortSha 取 commit 前 7 位显示(可读)。
func shortSha(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// firstField 取空白分隔的第一个字段(ls-remote 输出 "<sha>\t<refname>")。
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
		Example: `  panoxy upstream   # prints "发现上游更新: Alpha @ 65287f0 → <new>, 建议同步" when upstream moved`,
		RunE:    runUpstream,
	}
	return c
}
