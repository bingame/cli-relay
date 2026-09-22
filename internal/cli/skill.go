package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func (a *App) skillCommand() *cobra.Command {
	group := &cobra.Command{Use: "skill", Short: "管理随 Relay 分发的交接 Skill"}
	var targets []string
	install := &cobra.Command{
		Use: "install", Short: "为已安装的 Claude Code / Codex 安装交接 Skill", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			selected := targets
			if !cmd.Flags().Changed("cli") {
				for _, target := range []string{"claude", "codex"} {
					if targetInstalled(target) {
						selected = append(selected, target)
					}
				}
			}
			// 先校验全部输入，避免拼写错误造成部分安装。
			for _, target := range selected {
				if target != "claude" && target != "codex" {
					return fmt.Errorf("不支持的 Skill 目标: %q（可选 claude,codex）", target)
				}
				if _, err := a.adapter(target); err != nil {
					return err
				}
			}
			if len(selected) == 0 {
				cmd.Println("未探测到 Claude Code 或 Codex；安装目标 CLI 后运行 relay skill install，也可显式指定 --cli claude,codex。")
				return nil
			}
			seen := map[string]bool{}
			var failures []error
			for _, target := range selected {
				if seen[target] {
					continue
				}
				seen[target] = true
				ad, _ := a.adapter(target)
				dir := filepath.Join(nativeHome(target), "skills", "relay-handoff")
				if err := ad.InstallSkill(dir); err != nil {
					failures = append(failures, fmt.Errorf("%s: %w", target, err))
					continue
				}
				cmd.Printf("已安装 %s: %s\n", target, filepath.Join(dir, "SKILL.md"))
			}
			return errors.Join(failures...)
		},
	}
	install.Flags().StringSliceVar(&targets, "cli", nil, "指定目标（逗号分隔），默认仅安装本机已探测到的 CLI")
	group.AddCommand(install)
	return group
}

func targetInstalled(target string) bool {
	binary := "codex"
	if target == "claude" {
		binary = "claude"
	}
	if override := os.Getenv("RELAY_" + strings.ToUpper(strings.ReplaceAll(target, "-", "_")) + "_BIN"); override != "" {
		binary = override
	}
	// 只探测，不执行 CLI；Windows 上也识别 npm 的 .cmd 入口。
	_, err := exec.LookPath(binary)
	return err == nil
}
