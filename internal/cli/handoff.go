package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/bingame/cli-relay/internal/adapter"
	"github.com/bingame/cli-relay/internal/handoff"
	"github.com/bingame/cli-relay/internal/process"
	"github.com/bingame/cli-relay/internal/safeio"
	"github.com/bingame/cli-relay/internal/secrets"
	"github.com/spf13/cobra"
)

func (a *App) handoffCommand() *cobra.Command {
	parent := &cobra.Command{Use: "handoff", Short: "生成、校验和消费语义交接文档"}
	var validate string
	schema := &cobra.Command{Use: "schema", Short: "输出 JSON Schema，或校验 Markdown 文档", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if validate != "" {
			b, e := safeio.ReadRegular(validate, 8<<20)
			if e != nil {
				return e
			}
			_, e = handoff.Parse(b)
			return e
		}
		_, e := cmd.OutOrStdout().Write(handoff.Schema())
		return e
	}}
	schema.Flags().StringVar(&validate, "validate", "", "校验指定 Handoff Markdown 文件")
	var docPath, target, id string
	var headless bool
	cont := &cobra.Command{Use: "continue", Short: "把交接文档和硬约束注入新的会话", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		b, e := safeio.ReadRegular(docPath, 8<<20)
		if e != nil {
			return e
		}
		d, e := handoff.Parse(b)
		if e != nil {
			return e
		}
		t := target
		if t == "" {
			t = d.SourceCLI
		}
		p := id
		if p == "" {
			current, e := a.current()
			if e != nil {
				return e
			}
			p = current[t]
			if p == "" && t == d.SourceCLI && d.SourceProvider != "unknown" {
				p = d.SourceProvider
			}
		}
		mode := adapter.Interactive
		if headless {
			mode = adapter.Headless
		}
		o, e := a.prepare(cmd, t, p, mode, nil)
		if e != nil {
			return e
		}
		prompt := d.ContinuePrompt()
		// 长交接文本通过 stdin 进入新会话，避免 Windows argv 长度限制。
		if headless {
			if t == "codex" {
				o.Inputs.Args = append(o.Inputs.Args, "-")
			}
			o.Stdin = bytes.NewBufferString(prompt)
			r, e := process.Spawn(cmd.Context(), o)
			if e != nil {
				return e
			}
			if r.ExitCode != 0 {
				return exitError{r.ExitCode}
			}
			return nil
		}
		// 交互 stdin 必须继续连接终端，使用原生 prompt 参数；超长文档建议 --exec。
		if len(prompt) > 24000 {
			return fmt.Errorf("交接文档过长，交互 argv 无法可靠传递；请使用 --exec 从 stdin 注入")
		}
		o.Inputs.Args = append(o.Inputs.Args, prompt)
		return process.Replace(o)
	}}
	cont.Flags().StringVar(&docPath, "doc", "", "Handoff 文档路径")
	cont.Flags().StringVar(&target, "cli", "", "目标 CLI，默认 source_cli")
	cont.Flags().StringVar(&id, "provider", "", "目标供应商，优先目标 CLI 的 current")
	cont.Flags().BoolVar(&headless, "exec", false, "通过受监管无头模式继续（支持长文档）")
	_ = cont.MarkFlagRequired("doc")
	parent.AddCommand(schema, cont, a.handoffExportCommand())
	return parent
}

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, fmt.Errorf("模型输出超出交接大小限制")
	}
	return b.Buffer.Write(p)
}
func (a *App) generate(cmd *cobra.Command, target, id, dir string, args []string, prompt string, timeout time.Duration) (*handoff.Document, error) {
	o, e := a.prepare(cmd, target, id, adapter.Headless, args)
	if e != nil {
		return nil, e
	}
	if dir != "" {
		o.Dir = dir
		o.SessionDir = dir
	}
	o.Stdin = bytes.NewBufferString(secrets.Redact(prompt, o.SecretValues))
	if target == "codex" {
		o.Inputs.Args = append(o.Inputs.Args, "-")
	}
	out := &limitedBuffer{limit: 16 << 20}
	o.Stdout = out
	o.Stderr = io.Discard
	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()
	r, e := process.Spawn(ctx, o)
	if e != nil {
		return nil, e
	}
	if r.ExitCode != 0 {
		return nil, fmt.Errorf("总结进程退出码 %d", r.ExitCode)
	}
	text := r.FinalText
	if text == "" {
		text = out.String()
	}
	return handoff.ExtractDocument(secrets.Redact(text, o.SecretValues))
}
func (a *App) handoffExportCommand() *cobra.Command {
	var target, id, output, input, rawFile, summaryProvider, summaryCLI string
	var live, dead bool
	var timeout time.Duration
	cmd := &cobra.Command{Use: "export", Short: "活会话导出指引，或恢复会话并生成交接", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if live && dead {
			return fmt.Errorf("--live 与 --dead 不能同时使用")
		}
		if _, e := a.adapter(target); e != nil {
			return e
		}
		write := func(b []byte) error {
			if output != "" {
				return safeio.WriteFile(output, b, 0600)
			}
			_, e := cmd.OutOrStdout().Write(b)
			return e
		}
		if input != "" {
			var b []byte
			var e error
			if input == "-" {
				b, e = io.ReadAll(io.LimitReader(cmd.InOrStdin(), (8<<20)+1))
				if len(b) > 8<<20 {
					return fmt.Errorf("文档过大")
				}
			} else {
				b, e = safeio.ReadRegular(input, 8<<20)
			}
			if e != nil {
				return e
			}
			d, e := handoff.ExtractDocument(string(b))
			if e != nil {
				return e
			}
			d.GeneratedBy = "live-agent"
			d.SourceCLI = target
			d.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
			b, e = d.Marshal()
			if e != nil {
				return e
			}
			return write(b)
		}
		if live || (!dead && id == "") {
			if output != "" {
				return fmt.Errorf("活会话请先在 agent 中执行 /relay-handoff，再用 --input <文档> -o <路径> 接收；提示词不会伪装成交接文档")
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "请将下列请求交给当前活 agent；外部 Relay 进程无法读取它的内存上下文。生成后用 handoff export --input 接收。")
			return write([]byte(handoff.LivePrompt() + "\n"))
		}
		if id == "" && rawFile == "" {
			return fmt.Errorf("--dead 需要 --session 或 --raw-file")
		}
		workdir, _ := os.Getwd()
		sourceProvider := ""
		if id != "" {
			if meta, e := process.LoadSession(a.Home, target, id); e == nil {
				sourceProvider = meta.Provider
				workdir = meta.WorkDir
			}
		}
		if sourceProvider == "" {
			current, e := a.current()
			if e != nil {
				return e
			}
			sourceProvider = current[target]
		}
		var doc *handoff.Document
		var resumeErr error
		// Codex 优先读取官方结构化历史；接口不可用时再尝试原生 resume。
		if target == "codex" && id != "" {
			if o, e := a.prepare(cmd, target, sourceProvider, adapter.Interactive, nil); e == nil {
				ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
				raw, readErr := handoff.ReadCodexThread(ctx, o.Inputs, workdir, id)
				cancel()
				if readErr == nil {
					doc, resumeErr = a.generate(cmd, target, sourceProvider, workdir, nil, handoff.StructuredSummaryPrompt(secrets.Redact(raw, o.SecretValues)), timeout)
				}
			}
		}
		if id != "" && doc == nil {
			ad, _ := a.adapter(target)
			doc, resumeErr = a.generate(cmd, target, sourceProvider, workdir, ad.ResumeArgs(id), handoff.LivePrompt(), timeout)
		}
		generated := "dead-session-resume"
		if doc == nil {
			generated = "raw-file-fallback"
			if rawFile == "" {
				var e error
				rawFile, e = handoff.FindSessionFile(target, id, nativeHome(target))
				if e != nil {
					if resumeErr != nil {
						return fmt.Errorf("原生恢复失败（%v），且无法定位原始会话: %w", resumeErr, e)
					}
					return e
				}
			}
			raw, e := handoff.RawExtract(rawFile, 64<<20)
			if e != nil {
				return e
			}
			if sourceProvider != "" {
				values, e := a.providerSecretValues(cmd, sourceProvider)
				if e != nil {
					return fmt.Errorf("无法解锁来源凭据用于交接脱敏: %w", e)
				}
				raw = secrets.Redact(raw, values)
			}
			st := summaryCLI
			if st == "" {
				st = target
			}
			sp := summaryProvider
			if sp == "" && st == target {
				sp = sourceProvider
			}
			doc, e = a.generate(cmd, st, sp, workdir, nil, handoff.SummaryPrompt(raw), timeout)
			if e != nil {
				return fmt.Errorf("原始文件已解析，但总结模型不可用: %w；可指定 --summary-cli/--summary-provider", e)
			}
		}
		doc.SourceCLI = target
		doc.SourceProvider = sourceProvider
		if doc.SourceProvider == "" {
			doc.SourceProvider = "unknown"
		}
		doc.SourceSessionID = id
		doc.GeneratedBy = generated
		doc.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
		doc.Git = handoff.GitInfo(workdir)
		if generated == "raw-file-fallback" {
			doc.Sections["环境依赖声明"] += "\n\n本文档由降级路径生成，可能存在信息丢失。"
		}
		b, e := doc.Marshal()
		if e != nil {
			return e
		}
		return write(b)
	}}
	cmd.Flags().StringVar(&target, "cli", "", "来源 CLI")
	cmd.Flags().StringVar(&id, "session", "", "来源会话 ID")
	cmd.Flags().BoolVar(&live, "live", false, "输出给活 agent 的交接请求")
	cmd.Flags().BoolVar(&dead, "dead", false, "先无头恢复，失败后解析原始会话并总结")
	cmd.Flags().StringVarP(&output, "output", "o", "", "输出文档路径")
	cmd.Flags().StringVar(&input, "input", "", "接收活 agent 生成的文档，- 表示 stdin")
	cmd.Flags().StringVar(&rawFile, "raw-file", "", "指定最后降级路径的原始 JSONL")
	cmd.Flags().StringVar(&summaryProvider, "summary-provider", "", "降级总结使用的供应商")
	cmd.Flags().StringVar(&summaryCLI, "summary-cli", "", "降级总结使用的 CLI")
	cmd.Flags().DurationVar(&timeout, "timeout", 2*time.Minute, "每次恢复/总结超时")
	_ = cmd.MarkFlagRequired("cli")
	return cmd
}
