// Package ccswitch 在独立的内存 SQLite 中读取 cc-switch 导出，不修改源文件或全局配置。
package ccswitch

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"relay/internal/provider"
	"relay/internal/safeio"

	"github.com/pelletier/go-toml/v2"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const MaxDumpBytes int64 = 64 << 20
const exportHeader = "-- CC Switch SQLite 导出"

type Entry struct {
	Provider   provider.Provider
	Secrets    map[string]string
	Current    bool
	OriginalID string
	Warnings   []string // 仅字段路径和处理说明，不包含原始配置值。
}

type Result struct {
	Providers []Entry
	FileHash  string
	Snapshot  []byte // 含原始 MCP/prompt 数据，只能加密存储。
	Columns   []string
}

// Parse 只解析，不渲染、不导入本地数据库、不切换当前供应商。
// Secrets 与 Snapshot 都是敏感内存数据，调用方必须在持久化之前加密。
func Parse(ctx context.Context, path string) (*Result, error) {
	data, err := safeio.ReadRegular(path, MaxDumpBytes)
	if err != nil {
		return nil, errors.New("无法读取 cc-switch SQL 导出，请检查路径、文件类型和大小限制")
	}
	digest := sha256.Sum256(data)
	script := strings.TrimPrefix(string(data), "\ufeff")
	if !strings.HasPrefix(script, exportHeader) || len(script) > len(exportHeader) && script[len(exportHeader)] != '\r' && script[len(exportHeader)] != '\n' {
		return nil, errors.New("文件缺少 cc-switch SQL 导出标记")
	}
	if err := guardSQL(script); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// 独立 Driver 不继承其他包注册的函数、扩展或虚拟表。
	db := sql.OpenDB(memoryConnector{driver: &sqlite.Driver{}})
	defer db.Close()
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, errors.New("无法创建 SQL 导入内存数据库")
	}
	defer conn.Close()
	for id, limit := range map[int]int{
		sqlite3.SQLITE_LIMIT_LENGTH:          int(MaxDumpBytes),
		sqlite3.SQLITE_LIMIT_SQL_LENGTH:      int(MaxDumpBytes),
		sqlite3.SQLITE_LIMIT_ATTACHED:        0,
		sqlite3.SQLITE_LIMIT_COLUMN:          512,
		sqlite3.SQLITE_LIMIT_EXPR_DEPTH:      100,
		sqlite3.SQLITE_LIMIT_COMPOUND_SELECT: 20,
	} {
		if _, err := sqlite.Limit(conn, id, limit); err != nil {
			return nil, errors.New("无法设置 SQL 导入安全限制")
		}
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA temp_store=MEMORY; PRAGMA trusted_schema=OFF; PRAGMA journal_mode=MEMORY; PRAGMA max_page_count=32768;"); err != nil {
		return nil, errors.New("无法初始化 SQL 导入内存数据库")
	}
	if _, err := conn.ExecContext(ctx, script); err != nil {
		// SQLite 错误经常含有 SQL 片段，不能直接包装后返回。
		return nil, errors.New("cc-switch SQL 执行失败，文件可能损坏或包含不支持的 SQL")
	}
	// 新 BEGIN 成功当且仅当脚本已回到 autocommit；未结束的事务不能算完整备份。
	if _, err := conn.ExecContext(ctx, "BEGIN;"); err != nil {
		return nil, errors.New("SQL 导出的事务未结束，文件可能已截断")
	}
	if _, err := conn.ExecContext(ctx, "ROLLBACK;"); err != nil {
		return nil, errors.New("无法检查 SQL 导出事务状态")
	}
	result := &Result{FileHash: hex.EncodeToString(digest[:]), Providers: []Entry{}}
	rows, err := tableRows(ctx, conn, "providers", true)
	if err != nil {
		return nil, err
	}
	result.Columns = rows.Columns
	for _, required := range []string{"id", "app_type", "name", "settings_config", "meta", "is_current"} {
		found := false
		for _, col := range rows.Columns {
			found = found || col == required
		}
		if !found {
			return nil, fmt.Errorf("providers 缺少必需列 %s；实际列：%s", required, safeColumns(rows.Columns))
		}
	}
	for i, row := range rows.Rows {
		entry, err := parseEntry(row)
		if err != nil {
			return nil, fmt.Errorf("第 %d 条供应商记录无效：%s", i+1, err)
		}
		result.Providers = append(result.Providers, entry)
	}
	snapshot := map[string]rawTable{}
	for _, table := range []string{"mcp_servers", "prompts"} {
		value, err := tableRows(ctx, conn, table, false)
		if err != nil {
			return nil, err
		}
		snapshot[table] = value
	}
	result.Snapshot, err = json.Marshal(snapshot)
	if err != nil {
		return nil, errors.New("无法编码 cc-switch 原始快照")
	}
	return result, nil
}

type memoryConnector struct{ driver *sqlite.Driver }

func (c memoryConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.driver.Open("file::memory:?mode=memory&cache=private&_defensive=1")
}
func (c memoryConnector) Driver() driver.Driver { return c.driver }

type rawTable struct {
	Columns []string         `json:"columns"`
	Rows    []map[string]any `json:"rows"`
}

func tableRows(ctx context.Context, conn *sql.Conn, table string, required bool) (rawTable, error) {
	result := rawTable{Columns: []string{}, Rows: []map[string]any{}}
	var exists int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE type='table' AND name=?", table).Scan(&exists); err != nil {
		return result, errors.New("无法读取 SQL 导出结构")
	}
	if exists == 0 {
		if required {
			return result, errors.New("SQL 导出中缺少 providers 表")
		}
		return result, nil
	}
	// table 仅来自本文件内的常量，绝不使用导出数据拼接标识符。
	rows, err := conn.QueryContext(ctx, `SELECT * FROM "`+table+`"`)
	if err != nil {
		return result, errors.New("无法读取 SQL 导出数据")
	}
	defer rows.Close()
	result.Columns, err = rows.Columns()
	if err != nil {
		return result, errors.New("无法读取 SQL 导出列名")
	}
	for rows.Next() {
		if len(result.Rows) >= 100000 {
			return result, errors.New("SQL 导出单表行数超过 100000 条限制")
		}
		values := make([]any, len(result.Columns))
		pointers := make([]any, len(values))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return result, errors.New("无法读取 SQL 导出记录")
		}
		row := make(map[string]any, len(values))
		for i, name := range result.Columns {
			row[name] = values[i]
		}
		result.Rows = append(result.Rows, row)
	}
	if rows.Err() != nil {
		return result, errors.New("SQL 导出读取未完成")
	}
	return result, nil
}

func parseEntry(row map[string]any) (Entry, error) {
	id, idOK := textValue(row["id"])
	app, appOK := textValue(row["app_type"])
	name, nameOK := textValue(row["name"])
	raw, rawOK := textValue(row["settings_config"])
	if !idOK || id == "" || !appOK || !provider.ValidID(app) || !nameOK || strings.TrimSpace(name) == "" || !rawOK {
		return Entry{}, errors.New("id、app_type、name 或 settings_config 字段无效")
	}
	settings := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &settings); err != nil || settings == nil {
		return Entry{}, errors.New("settings_config 不是 JSON 对象")
	}
	current, err := currentValue(row["is_current"])
	if err != nil {
		return Entry{}, err
	}
	entry := Entry{
		Provider: provider.Provider{ID: safeSlug(id), DisplayName: name, Targets: []string{app}, Source: "cc-switch-import", Extra: map[string]any{}},
		Secrets:  map[string]string{"source_settings": raw}, Current: current, OriginalID: id,
	}
	if rawMeta, ok := textValue(row["meta"]); ok {
		entry.Secrets["source_meta"] = rawMeta
	}
	switch app {
	case "claude":
		entry.Provider.Targets = []string{"claude-code"}
		env, _ := settings["env"].(map[string]any)
		entry.Provider.BaseURL = firstString(env["ANTHROPIC_BASE_URL"], settings["base_url"])
		entry.Provider.Model = firstString(env["ANTHROPIC_MODEL"], settings["model"])
		entry.Secrets["api_key"] = firstString(env["ANTHROPIC_AUTH_TOKEN"], env["ANTHROPIC_API_KEY"], settings["api_key"])
		entry.Provider.Extra["claude_settings"] = sanitize(settings, "", entry.Secrets)
		// 两种鉴权不能同时注入；保留原生 API_KEY 语义，次要凭据仅留在加密源配置中。
		if entry.Secrets["api_key"] != "" {
			entry.Secrets["api_key_env"] = "ANTHROPIC_AUTH_TOKEN"
			if firstString(env["ANTHROPIC_AUTH_TOKEN"]) == "" && firstString(env["ANTHROPIC_API_KEY"]) != "" {
				entry.Secrets["api_key_env"] = "ANTHROPIC_API_KEY"
			}
		}
		for _, name := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"} {
			if value := entry.Secrets["env:"+name]; value != "" && value != entry.Secrets["api_key"] {
				entry.Secrets["field:env."+name] = value
			}
		}
		delete(entry.Secrets, "env:ANTHROPIC_AUTH_TOKEN")
		delete(entry.Secrets, "env:ANTHROPIC_API_KEY")
	case "codex":
		auth, _ := settings["auth"].(map[string]any)
		entry.Secrets["api_key"] = firstString(auth["OPENAI_API_KEY"], settings["api_key"])
		config := map[string]any{}
		if rawConfig, ok := settings["config"]; ok {
			value, ok := rawConfig.(string)
			if !ok || toml.Unmarshal([]byte(value), &config) != nil {
				return Entry{}, errors.New("Codex config 不是有效 TOML")
			}
		}
		selected := firstString(config["model_provider"])
		providers, _ := config["model_providers"].(map[string]any)
		selectedConfig, _ := providers[selected].(map[string]any)
		entry.Provider.BaseURL = firstString(selectedConfig["base_url"], settings["base_url"])
		entry.Provider.Model = firstString(config["model"], settings["model"])
		env, _ := settings["env"].(map[string]any)
		if entry.Secrets["api_key"] == "" {
			entry.Secrets["api_key"] = firstString(selectedConfig["experimental_bearer_token"], selectedConfig["api_key"], env[firstString(selectedConfig["env_key"])])
		}
		// JSON env 中可能另有凭据；原始 auth/config 只通过 source_settings 加密保存。
		if env != nil {
			sanitize(map[string]any{"env": env}, "", entry.Secrets)
		}
		moveCodexHeaders(config, entry.Provider.ID, env, entry.Secrets)
		entry.Provider.Extra["codex_config"] = sanitize(config, "", entry.Secrets)
	default:
		// 不支持的 target 保留全量加密源数据，不生成可误用的原生配置。
		entry.Secrets["api_key"] = firstString(settings["api_key"])
		sanitize(settings, "", entry.Secrets)
	}
	if entry.Secrets["api_key"] == "" {
		delete(entry.Secrets, "api_key")
	}
	if err := removeCredentialCopies(&entry); err != nil {
		return Entry{}, err
	}
	if err := entry.Provider.Validate(); err != nil {
		return Entry{}, errors.New("供应商 ID、目标或 base_url 无效；URL 不能含凭据或查询参数")
	}
	return entry, nil
}

// 已知凭据可能再次出现在 hooks.command 等普通文本中；禁止其进入公开元数据。
func removeCredentialCopies(entry *Entry) error {
	var credentials []string
	for key, value := range entry.Secrets {
		if value == "" {
			continue
		}
		if key == "api_key" || len(value) >= 8 && (strings.HasPrefix(key, "env:") || strings.HasPrefix(key, "field:")) {
			credentials = append(credentials, value)
		}
	}
	contains := func(value string) bool {
		for _, secret := range credentials {
			if strings.Contains(value, secret) {
				return true
			}
		}
		return false
	}
	public := []string{entry.Provider.ID, entry.OriginalID, entry.Provider.DisplayName, entry.Provider.BaseURL, entry.Provider.Model}
	public = append(public, entry.Provider.Targets...)
	for _, value := range public {
		if contains(value) {
			return errors.New("供应商公开字段包含已识别凭据，拒绝导入")
		}
	}
	var clean func(any, string) (any, bool)
	clean = func(value any, path string) (any, bool) {
		switch value := value.(type) {
		case string:
			if contains(value) {
				entry.Warnings = append(entry.Warnings, "已移除含凭据的配置字段："+path)
				return nil, false
			}
		case map[string]any:
			keys := make([]string, 0, len(value))
			for key := range value {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			filtered := make(map[string]any, len(value))
			for _, key := range keys {
				part := key
				if contains(part) {
					part = "[含凭据的字段名]"
				}
				childPath := path + "." + part
				if contains(key) {
					entry.Warnings = append(entry.Warnings, "已移除含凭据的配置字段："+childPath)
					continue
				}
				if child, keep := clean(value[key], childPath); keep {
					filtered[key] = child
				} else if key == "command" {
					// 不留下只有 type、没有 command 的半个 hook。
					return nil, false
				}
			}
			return filtered, true
		case []any:
			filtered := make([]any, 0, len(value))
			for i, child := range value {
				if child, keep := clean(child, fmt.Sprintf("%s[%d]", path, i)); keep {
					filtered = append(filtered, child)
				}
			}
			return filtered, true
		}
		return value, true
	}
	value, _ := clean(entry.Provider.Extra, "extra")
	entry.Provider.Extra, _ = value.(map[string]any)
	return nil
}

func textValue(value any) (string, bool) {
	switch value := value.(type) {
	case string:
		return value, true
	case []byte:
		return string(value), true
	}
	return "", false
}

func firstString(values ...any) string {
	for _, value := range values {
		if value, ok := value.(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func currentValue(value any) (bool, error) {
	switch value := value.(type) {
	case int64:
		if value == 0 || value == 1 {
			return value == 1, nil
		}
	case string:
		if value == "0" || value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "false") {
			parsed, _ := strconv.ParseBool(value)
			return parsed, nil
		}
	}
	return false, errors.New("is_current 不是有效布尔值")
}

func safeSlug(id string) string {
	if provider.ValidID(id) {
		return id
	}
	var slug strings.Builder
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || (r == '_' || r == '-') && slug.Len() > 0 {
			slug.WriteRune(r)
		} else if slug.Len() > 0 && !strings.HasSuffix(slug.String(), "-") {
			slug.WriteByte('-')
		}
		if slug.Len() >= 96 {
			break
		}
	}
	value := strings.TrimRight(slug.String(), "-_")
	if value == "" {
		value = "provider"
	}
	hash := sha256.Sum256([]byte(id))
	return value + "-" + hex.EncodeToString(hash[:4])
}

func safeColumns(columns []string) string {
	names := make([]string, len(columns))
	for i, column := range columns {
		if provider.ValidID(column) {
			names[i] = column
		} else {
			names[i] = "[非标准列名]"
		}
	}
	return strings.Join(names, ", ")
}

func sanitize(value any, path string, secrets map[string]string) any {
	switch value := value.(type) {
	case map[string]any:
		clean := make(map[string]any, len(value))
		for key, item := range value {
			itemPath := key
			if path != "" {
				itemPath = path + "." + key
			}
			if key == "env_http_headers" {
				// 键是 HTTP header 名称，值是环境变量名；Authorization 在此不是密钥。
				clean[key] = item
				continue
			}
			if tokenBudget(key, item) {
				clean[key] = item
				continue
			}
			if sensitiveName(key) {
				if text, ok := item.(string); ok && text != "" {
					if path == "env" {
						secrets["env:"+key] = text
					} else {
						secrets["field:"+itemPath] = text
					}
				}
				continue
			}
			clean[key] = sanitize(item, itemPath, secrets)
		}
		return clean
	case []any:
		clean := make([]any, len(value))
		for i, item := range value {
			clean[i] = sanitize(item, path, secrets)
		}
		return clean
	default:
		return value
	}
}

// Codex 支持 env_http_headers；把内联 header 换成环境引用，保持自定义网关兼容。
func moveCodexHeaders(config map[string]any, id string, env map[string]any, secrets map[string]string) {
	providers, _ := config["model_providers"].(map[string]any)
	for name, raw := range providers {
		native, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		refs, _ := native["env_http_headers"].(map[string]any)
		if refs == nil {
			refs = map[string]any{}
		}
		for _, ref := range refs {
			if variable, ok := ref.(string); ok {
				if value, ok := env[variable].(string); ok && value != "" {
					secrets["env:"+variable] = value
				}
			}
		}
		if headers, ok := native["http_headers"].(map[string]any); ok {
			for header, value := range headers {
				text, ok := value.(string)
				if !ok {
					continue
				}
				if _, exists := refs[header]; exists {
					continue // 保持原生环境引用的优先级。
				}
				digest := sha256.Sum256([]byte(id + "\x00" + name + "\x00" + header))
				variable := "RELAY_IMPORT_" + strings.ToUpper(hex.EncodeToString(digest[:8])) + "_HEADER"
				refs[header] = variable
				secrets["env:"+variable] = text
			}
		}
		delete(native, "http_headers")
		if len(refs) > 0 {
			native["env_http_headers"] = refs
		}
	}
}

func sensitiveName(name string) bool {
	name = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(name, "_", ""), "-", ""))
	if name == "envkey" || name == "apikeyenv" || name == "envhttpheaders" {
		return false
	}
	return strings.Contains(name, "apikey") || strings.Contains(name, "token") || strings.Contains(name, "secret") || strings.Contains(name, "password") || strings.Contains(name, "credential") || strings.Contains(name, "authorization") || strings.Contains(name, "customheaders") || strings.Contains(name, "cookie") || name == "auth" || name == "httpheaders" || strings.HasSuffix(name, "key")
}

func tokenBudget(key string, value any) bool {
	switch key {
	case "model_auto_compact_token_limit", "max_tokens", "max_output_tokens", "max_input_tokens":
		switch value.(type) {
		case int64, float64, int:
			return true
		}
	}
	return false
}
