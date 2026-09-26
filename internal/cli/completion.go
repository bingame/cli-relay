package cli

import (
	"bytes"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// cobra 生成的 PowerShell 补全脚本通过 Invoke-Expression 调用 relay __complete，
// 而 PowerShell 按 [Console]::OutputEncoding 解码原生进程输出。中文描述在 GBK 等
// 代码页下会把紧随其后的换行符吞进双字节序列，导致指令行（":4"）并入上一行，
// completer 解析指令失败后整体中断，PowerShell 退化为文件名补全。
// 因此生成后需在调用前后把输出编码切到 UTF-8，调用结束再恢复。
const psInvokeLine = "Invoke-Expression -OutVariable out \"$RequestComp\" 2>&1 | Out-Null"

const psInvokePatched = `$__relayOutputEncoding = [Console]::OutputEncoding
    try { [Console]::OutputEncoding = [System.Text.Encoding]::UTF8 } catch { }
    Invoke-Expression -OutVariable out "$RequestComp" 2>&1 | Out-Null
    try { [Console]::OutputEncoding = $__relayOutputEncoding } catch { }`

// patchPowerShellCompletion 注入输出编码切换；模板变化导致锚点缺失时返回原脚本，
// 由单元测试固定 cobra 版本下的锚点，避免静默失效。
func patchPowerShellCompletion(script string) string {
	if !strings.Contains(script, psInvokeLine) {
		return script
	}
	return strings.Replace(script, psInvokeLine, psInvokePatched, 1)
}

func (a *App) completionCommand() *cobra.Command {
	group := &cobra.Command{
		Use:   "completion",
		Short: "生成 shell 补全脚本",
		Long:  "为 bash、zsh、fish 或 PowerShell 生成 relay 的补全脚本，写入标准输出。",
	}
	shell := func(use, short string, generate func(*cobra.Command, *bytes.Buffer)) *cobra.Command {
		var noDesc bool
		sub := &cobra.Command{
			Use: use, Short: short, Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				var buf bytes.Buffer
				generate(cmd, &buf)
				script := buf.String()
				if use == "powershell" && !noDesc {
					script = patchPowerShellCompletion(script)
				}
				_, err := os.Stdout.WriteString(script)
				return err
			},
		}
		sub.Flags().BoolVar(&noDesc, "no-descriptions", false, "禁用补全描述")
		return sub
	}
	group.AddCommand(
		shell("bash", "生成 bash 补全脚本", func(cmd *cobra.Command, buf *bytes.Buffer) {
			_ = cmd.Root().GenBashCompletionV2(buf, true)
		}),
		shell("zsh", "生成 zsh 补全脚本", func(cmd *cobra.Command, buf *bytes.Buffer) {
			_ = cmd.Root().GenZshCompletion(buf)
		}),
		shell("fish", "生成 fish 补全脚本", func(cmd *cobra.Command, buf *bytes.Buffer) {
			_ = cmd.Root().GenFishCompletion(buf, true)
		}),
		shell("powershell", "生成 PowerShell 补全脚本", func(cmd *cobra.Command, buf *bytes.Buffer) {
			_ = cmd.Root().GenPowerShellCompletionWithDesc(buf)
		}),
	)
	return group
}
