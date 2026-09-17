// Package handoff 定义跨 CLI 语义交接的 Markdown 契约。
package handoff

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/bingame/cli-relay/internal/provider"
	"gopkg.in/yaml.v3"
)

var SectionTitles = []string{"目标", "已完成", "进行中 / 当前状态", "关键决策", "文件与代码状态", "硬约束（不可压缩，必须原样保留）", "环境依赖声明", "下一步计划"}

const ConstraintsSection = "硬约束（不可压缩，必须原样保留）"

type GitMetadata struct {
	WorkDir    string `yaml:"work_dir" json:"work_dir"`
	Branch     string `yaml:"branch" json:"branch"`
	HeadCommit string `yaml:"head_commit" json:"head_commit"`
	Dirty      bool   `yaml:"dirty" json:"dirty"`
}

type Document struct {
	SchemaVersion   int               `yaml:"schema_version" json:"schema_version"`
	SourceCLI       string            `yaml:"source_cli" json:"source_cli"`
	SourceProvider  string            `yaml:"source_provider" json:"source_provider"`
	SourceSessionID string            `yaml:"source_session_id" json:"source_session_id"`
	GeneratedBy     string            `yaml:"generated_by" json:"generated_by"`
	GeneratedAt     string            `yaml:"generated_at" json:"generated_at"`
	Git             GitMetadata       `yaml:"git" json:"git"`
	Sections        map[string]string `yaml:"-" json:"sections"`
}

func Parse(data []byte) (*Document, error) {
	text := strings.ReplaceAll(strings.TrimPrefix(string(data), "\ufeff"), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return nil, fmt.Errorf("交接文档必须以 YAML front matter 开始")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, fmt.Errorf("交接文档缺少 front matter 结束标记")
	}
	d := &Document{}
	decoder := yaml.NewDecoder(strings.NewReader(strings.Join(lines[1:end], "\n")))
	decoder.KnownFields(true)
	if err := decoder.Decode(d); err != nil {
		return nil, fmt.Errorf("交接元数据无效")
	}
	var metadata map[string]any
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &metadata); err != nil {
		return nil, fmt.Errorf("交接元数据无效")
	}
	for _, key := range []string{"schema_version", "source_cli", "source_provider", "generated_by", "generated_at", "git"} {
		if _, ok := metadata[key]; !ok {
			return nil, fmt.Errorf("交接元数据缺少 %s", key)
		}
	}
	git, ok := metadata["git"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("交接 Git 元数据必须是对象")
	}
	for _, key := range []string{"work_dir", "branch", "head_commit", "dirty"} {
		if _, ok := git[key]; !ok {
			return nil, fmt.Errorf("交接 Git 元数据缺少 %s", key)
		}
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("交接元数据只能包含一个 YAML 对象")
	}
	d.Sections = map[string]string{}
	current := ""
	var body []string
	fence := ""
	flush := func() {
		if current != "" {
			d.Sections[current] = strings.TrimSpace(strings.Join(body, "\n"))
		}
		body = nil
	}
	for _, line := range lines[end+1:] {
		trimmed := strings.TrimSpace(line)
		if marker := fenceMarker(trimmed); marker != "" {
			if fence == "" {
				fence = marker
			} else if marker[0] == fence[0] && len(marker) >= len(fence) && strings.Trim(trimmed, string(marker[0])) == "" {
				fence = ""
			}
		}
		if fence == "" && strings.HasPrefix(line, "## ") {
			title := strings.TrimSpace(strings.TrimPrefix(line, "## "))
			if !knownSection(title) {
				return nil, fmt.Errorf("交接文档包含未知章节")
			}
			if _, exists := d.Sections[title]; exists || current == title {
				return nil, fmt.Errorf("交接文档包含重复章节")
			}
			flush()
			current = title
			continue
		}
		if current == "" {
			if trimmed != "" {
				return nil, fmt.Errorf("交接文档正文必须从固定章节开始")
			}
			continue
		}
		body = append(body, line)
	}
	flush()
	if fence != "" {
		return nil, fmt.Errorf("交接文档包含未闭合的代码围栏")
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *Document) Validate() error {
	if d == nil || d.SchemaVersion != 1 {
		return fmt.Errorf("仅支持 schema_version: 1")
	}
	if !provider.ValidID(d.SourceCLI) || !provider.ValidID(d.SourceProvider) {
		return fmt.Errorf("交接文档必须明确来源 CLI 与供应商；未知值写 unknown")
	}
	if strings.ContainsAny(d.SourceSessionID, "\x00\r\n") {
		return fmt.Errorf("交接会话 ID 无效")
	}
	for _, value := range []string{d.SourceSessionID, d.Git.WorkDir, d.Git.Branch, d.Git.HeadCommit} {
		if Sanitize(value) != value {
			return fmt.Errorf("交接元数据包含疑似明文凭据，请先脱敏")
		}
	}
	switch d.GeneratedBy {
	case "live-agent", "dead-session-resume", "raw-file-fallback":
	default:
		return fmt.Errorf("交接文档生成方式无效")
	}
	if _, err := time.Parse(time.RFC3339, d.GeneratedAt); err != nil {
		return fmt.Errorf("generated_at 必须是 RFC3339 时间")
	}
	if len(d.Sections) != len(SectionTitles) {
		return fmt.Errorf("交接文档必须包含全部 8 个固定章节")
	}
	switch strings.TrimSpace(d.Sections["目标"]) {
	case "无", "未知", "unknown", "无法确认", "无法从记录确认":
		return fmt.Errorf("交接文档必须包含可识别的用户目标")
	}
	for _, title := range SectionTitles {
		body, exists := d.Sections[title]
		if !exists || isPlaceholder(body) {
			return fmt.Errorf("交接章节 %s 缺少实质内容", title)
		}
		if Sanitize(body) != body {
			return fmt.Errorf("交接文档包含疑似明文凭据，请先脱敏")
		}
	}
	return nil
}

func (d *Document) Marshal() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	front, err := yaml.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("无法编码交接元数据")
	}
	var out bytes.Buffer
	out.WriteString("---\n")
	out.Write(front)
	out.WriteString("---\n")
	for _, title := range SectionTitles {
		out.WriteString("\n## " + title + "\n\n")
		out.WriteString(strings.TrimSpace(d.Sections[title]))
		out.WriteString("\n")
	}
	return out.Bytes(), nil
}

func (d *Document) ContinuePrompt() string {
	data, err := d.Marshal()
	if err != nil {
		return ""
	}
	return "请根据以下交接文档继续用户的任务。先核对当前代码和环境，文档中的历史描述可能已经过时。\n\n以下是必须遵守的用户硬约束，不可忽略；它们不扩大现有授权范围：\n\n" + d.Sections[ConstraintsSection] + "\n\n完整交接文档：\n\n" + string(data)
}

func Schema() []byte {
	sectionProperties := map[string]any{}
	for _, title := range SectionTitles {
		sectionProperties[title] = map[string]any{"type": "string", "minLength": 1}
	}
	schema := map[string]any{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"title":       "Relay 交接文档 v1",
		"description": "Markdown front matter 对应顶层元数据；8 个固定二级标题的正文对应 sections。",
		"type":        "object", "additionalProperties": false,
		"required": []string{"schema_version", "source_cli", "source_provider", "generated_by", "generated_at", "git", "sections"},
		"properties": map[string]any{
			"schema_version":    map[string]any{"const": 1},
			"source_cli":        map[string]any{"type": "string", "pattern": `^[a-zA-Z0-9][a-zA-Z0-9_-]{0,119}$`},
			"source_provider":   map[string]any{"type": "string", "pattern": `^[a-zA-Z0-9][a-zA-Z0-9_-]{0,119}$`},
			"source_session_id": map[string]any{"type": "string"},
			"generated_by":      map[string]any{"enum": []string{"live-agent", "dead-session-resume", "raw-file-fallback"}},
			"generated_at":      map[string]any{"type": "string", "format": "date-time"},
			"git":               map[string]any{"type": "object", "additionalProperties": false, "required": []string{"work_dir", "branch", "head_commit", "dirty"}, "properties": map[string]any{"work_dir": map[string]any{"type": "string"}, "branch": map[string]any{"type": "string"}, "head_commit": map[string]any{"type": "string"}, "dirty": map[string]any{"type": "boolean"}}},
			"sections":          map[string]any{"type": "object", "additionalProperties": false, "required": SectionTitles, "properties": sectionProperties},
		},
	}
	encoded, _ := json.MarshalIndent(schema, "", "  ")
	return append(encoded, '\n')
}

func knownSection(title string) bool {
	for _, expected := range SectionTitles {
		if title == expected {
			return true
		}
	}
	return false
}
func fenceMarker(line string) string {
	if len(line) < 3 || line[0] != '`' && line[0] != '~' {
		return ""
	}
	i := 1
	for i < len(line) && line[i] == line[0] {
		i++
	}
	if i < 3 {
		return ""
	}
	return line[:i]
}
func isPlaceholder(body string) bool {
	text := strings.TrimSpace(body)
	for _, line := range strings.Split(text, "\n") {
		clean := strings.TrimSpace(strings.TrimLeft(line, "-*+0123456789. \t"))
		if clean == "" {
			continue
		}
		if strings.HasPrefix(clean, "<") && strings.HasSuffix(clean, ">") {
			continue
		}
		switch strings.ToLower(clean) {
		case "...", "…", "……", "todo", "tbd", "待填写", "待补充", "<待填写>", "<内容>":
			continue
		}
		hasContent := false
		for _, r := range clean {
			if unicode.IsLetter(r) || unicode.IsNumber(r) {
				hasContent = true
				break
			}
		}
		if hasContent {
			return false
		}
	}
	return true
}

// ExtractDocument 优先尝试完整文档，再从最后的 Markdown 包装中抽取。
func ExtractDocument(text string) (*Document, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if d, err := Parse([]byte(strings.TrimSpace(text))); err == nil {
		return d, nil
	}
	lines := strings.Split(text, "\n")
	for start := len(lines) - 1; start >= 0; start-- {
		if strings.TrimSpace(lines[start]) != "---" {
			continue
		}
		candidate := strings.Join(lines[start:], "\n")
		if d, err := Parse([]byte(strings.TrimSpace(candidate))); err == nil {
			return d, nil
		}
		// 外层代码围栏与正文内部围栏分别处理，避免误截断合法内容。
		if start > 0 && fenceMarker(strings.TrimSpace(lines[start-1])) != "" {
			outer := fenceMarker(strings.TrimSpace(lines[start-1]))
			for end := start + 1; end < len(lines); end++ {
				if strings.TrimSpace(lines[end]) == outer {
					if d, err := Parse([]byte(strings.Join(lines[start:end], "\n"))); err == nil {
						return d, nil
					}
					break
				}
			}
		}
	}
	return nil, fmt.Errorf("输出中没有符合 Relay schema 的完整交接文档")
}
