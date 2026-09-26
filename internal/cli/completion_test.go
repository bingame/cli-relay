package cli

import (
	"bytes"
	"strings"
	"testing"
)

// cobra 模板变化会使 patchPowerShellCompletion 失去锚点而静默失效，此处固定。
func TestPatchPowerShellCompletionAnchor(t *testing.T) {
	var buf bytes.Buffer
	if err := NewRoot().GenPowerShellCompletionWithDesc(&buf); err != nil {
		t.Fatalf("生成 PowerShell 补全模板失败: %v", err)
	}
	if !strings.Contains(buf.String(), psInvokeLine) {
		t.Fatal("PowerShell 补全模板缺少 Invoke-Expression 锚点，patchPowerShellCompletion 需要同步更新")
	}
}

func TestPatchPowerShellCompletion(t *testing.T) {
	patched := patchPowerShellCompletion("prefix\n" + psInvokeLine + "\nsuffix")
	if !strings.Contains(patched, "[Console]::OutputEncoding = [System.Text.Encoding]::UTF8") {
		t.Fatalf("补丁未注入输出编码切换: %s", patched)
	}
	if strings.Count(patched, psInvokeLine) != 1 {
		t.Fatalf("补丁后仍应保留一次 Invoke-Expression 调用: %s", patched)
	}
	if got := patchPowerShellCompletion("no anchor"); got != "no anchor" {
		t.Fatalf("锚点缺失时应返回原脚本: %s", got)
	}
}
