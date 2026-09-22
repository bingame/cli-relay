package handoff

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func fixtureDocument() *Document {
	d := &Document{SchemaVersion: 1, SourceCLI: "claude", SourceProvider: "fake-provider", SourceSessionID: "fake-session", GeneratedBy: "live-agent", GeneratedAt: "2026-09-17T08:00:00Z", Git: GitMetadata{WorkDir: "/fake/project", Branch: "main", HeadCommit: "0123456", Dirty: true}, Sections: map[string]string{}}
	for _, title := range SectionTitles {
		d.Sections[title] = "无"
	}
	d.Sections["目标"] = "完成 Relay 的跨 CLI 会话交接。"
	d.Sections[ConstraintsSection] = "不要用 mattn/go-sqlite3。\n文档和 Commit 使用中文。"
	return d
}

func TestDocumentRoundtripAndConstraints(t *testing.T) {
	d := fixtureDocument()
	d.Sections["文件与代码状态"] = "修改内部解析器。\n\n```markdown\n## 这不是交接章节\n```"
	data, err := d.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(parsed, d) {
		t.Fatalf("文档往返丢失数据: %#v", parsed)
	}
	prompt := parsed.ContinuePrompt()
	if strings.Count(prompt, d.Sections[ConstraintsSection]) != 2 {
		t.Fatal("续接未单独强调原文硬约束")
	}
	var schema map[string]any
	if err := json.Unmarshal(Schema(), &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	required := properties["sections"].(map[string]any)["required"].([]any)
	if len(required) != 8 {
		t.Fatal("schema 未声明全部固定节")
	}
}

func TestDocumentRejectsInvalidMetadataAndTemplates(t *testing.T) {
	for _, mutate := range []func(*Document){func(d *Document) { d.SchemaVersion = 2 }, func(d *Document) { d.GeneratedAt = "yesterday" }, func(d *Document) { d.SourceCLI = "" }, func(d *Document) { d.GeneratedBy = "made-up" }, func(d *Document) { delete(d.Sections, "目标") }, func(d *Document) { d.Sections["目标"] = "- ...\n- 待填写" }, func(d *Document) { d.Sections["目标"] = "ANTHROPIC_AUTH_TOKEN=fake-sensitive" }} {
		d := fixtureDocument()
		mutate(d)
		if d.Validate() == nil {
			t.Fatal("应拒绝不完整/危险交接文档")
		}
	}
	d := fixtureDocument()
	data, err := d.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{strings.Replace(string(data), "schema_version: 1", "schema_version: 1\nschema_version: 1", 1), strings.Replace(string(data), "source_cli: claude\n", "", 1), string(data) + "\n## 目标\n重复目标\n", string(data) + "\n```\n未闭合", strings.Replace(string(data), "source_cli: claude", "unexpected: true\nsource_cli: claude", 1)} {
		if _, err := Parse([]byte(text)); err == nil {
			t.Fatal("应拒绝无效 Markdown/YAML")
		}
	}
}

func TestExtractDocumentFromFinalOutput(t *testing.T) {
	d := fixtureDocument()
	data, err := d.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{string(data), "这是交接：\n```markdown\n" + string(data) + "```\n结束。"} {
		got, err := ExtractDocument(text)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, d) {
			t.Fatal("提取文档包含外层包装或丢失内容")
		}
	}
	if _, err := ExtractDocument("任务还未完成。"); err == nil {
		t.Fatal("普通回复不能当作交接文档")
	}
}

func TestSanitizePreservesOrdinaryConstraints(t *testing.T) {
	text := "不要把 api_key 写入 argv。文档和 Commit 使用中文。\nTOKEN_COUNT=32\nmax_tokens: 42"
	if Sanitize(text) != text {
		t.Fatal("脱敏修改了普通约束文本")
	}
	for _, input := range []string{"Authorization: Bearer fake-sensitive", `{"api_key":"fake-sensitive"}`, "ANTHROPIC_AUTH_TOKEN=fake-sensitive", "https://person:fake-sensitive@example.invalid/", "--api-key fake-sensitive", "data:image/png;base64,ZmFrZS1pbWFnZQ=="} {
		masked := Sanitize(input)
		if strings.Contains(masked, "fake-sensitive") || strings.Contains(masked, "ZmFrZS1pbWFnZQ==") {
			t.Errorf("脱敏未生效: %s", masked)
		}
		if Sanitize(masked) != masked {
			t.Fatal("重复脱敏不幂等")
		}
	}
}
