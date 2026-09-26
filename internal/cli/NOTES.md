# NOTES

## cobra PowerShell 补全与控制台编码（2026-09-26）

cobra 生成的 PowerShell 补全脚本（`GenPowerShellCompletionWithDesc`）通过
`Invoke-Expression` 调用 `relay __complete`，PowerShell 按 `[Console]::OutputEncoding`
解码原生进程输出。relay 的命令描述是中文（UTF-8），在 GBK 等系统代码页下，
描述的双字节序列会吞掉紧随的换行符，导致末尾指令行（`:4`）并入上一行，
completer 在 `[int]$Directive = $Out[-1].TrimStart(':')` 处抛异常，整体退化为
文件名补全。

上游差异处理：relay 不直接输出 cobra 模板，而是接管 `completion` 命令
（`CompletionOptions` 默认命令被自定义 `completion` 覆盖），在生成
PowerShell 脚本后注入「调用前切 UTF-8、调用后恢复」的编码切换
（`patchPowerShellCompletion`）。锚点行由 `completion_test.go` 固定，
cobra 升级改动模板时测试会失败提醒同步。

bash/zsh/fish 补全按字节流处理，不受影响，直接透传 cobra 生成器。
