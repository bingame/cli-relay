package ccswitch

import (
	"errors"
	"strings"
	"unicode/utf8"
)

var errUnsafeSQL = errors.New("SQL 导出包含不允许的语句或函数")

// sqlToken 区分字面量、引用标识符和普通词；安全检查不会误扫 INSERT 的 JSON 内容。
type sqlToken struct {
	text string
	kind byte // w: 单词；i: 引用标识符；s: 字符串；p: 标点。
}

// guardSQL 不是 INSERT 解析器。这里只检查词法边界，数据仍由 SQLite 完整执行。
// modernc/sqlite 当前没有公开 authorizer API，故采用默认拒绝的语句白名单。
func guardSQL(script string) error {
	if strings.IndexByte(script, 0) >= 0 {
		return errors.New("SQL 导出含有无效字符")
	}
	var statement []sqlToken
	flush := func() error {
		if len(statement) == 0 {
			return nil
		}
		err := guardStatement(statement)
		statement = statement[:0]
		return err
	}
	for pos := 0; pos < len(script); {
		c := script[pos]
		if c == 0 {
			return errors.New("SQL 导出含有无效字符")
		}
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' {
			pos++
			continue
		}
		if c == '-' && pos+1 < len(script) && script[pos+1] == '-' {
			if end := strings.IndexByte(script[pos:], '\n'); end >= 0 {
				pos += end + 1
			} else {
				pos = len(script)
			}
			continue
		}
		if c == '/' && pos+1 < len(script) && script[pos+1] == '*' {
			end := strings.Index(script[pos+2:], "*/")
			if end < 0 {
				return errors.New("SQL 导出的注释未结束，文件可能已截断")
			}
			pos += end + 4
			continue
		}
		if c == ';' {
			if err := flush(); err != nil {
				return err
			}
			pos++
			continue
		}
		if c == '\'' || c == '"' || c == '`' || c == '[' {
			endQuote := c
			kind := byte('i')
			if c == '\'' {
				kind = 's'
			} else if c == '[' {
				endQuote = ']'
			}
			pos++
			var value strings.Builder
			closed := false
			for pos < len(script) {
				if script[pos] == 0 {
					return errors.New("SQL 导出含有无效字符")
				}
				if script[pos] != endQuote {
					value.WriteByte(script[pos])
					pos++
					continue
				}
				pos++
				if endQuote != ']' && pos < len(script) && script[pos] == endQuote {
					value.WriteByte(endQuote)
					pos++
					continue
				}
				closed = true
				break
			}
			if !closed {
				return errors.New("SQL 导出的引用未结束，文件可能已截断")
			}
			statement = append(statement, sqlToken{text: value.String(), kind: kind})
			continue
		}
		if isSQLWord(c) {
			start := pos
			for pos < len(script) && isSQLWord(script[pos]) {
				pos++
			}
			statement = append(statement, sqlToken{text: script[start:pos], kind: 'w'})
			continue
		}
		statement = append(statement, sqlToken{text: string(c), kind: 'p'})
		pos++
	}
	if !utf8.ValidString(script) {
		return errors.New("SQL 导出不是有效的 UTF-8 文本")
	}
	return flush()
}

func isSQLWord(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '$' || c >= 128
}

func guardStatement(tokens []sqlToken) error {
	if tokens[0].kind != 'w' {
		return errUnsafeSQL
	}
	for i, token := range tokens {
		word := strings.ToUpper(token.text)
		if token.kind == 'w' {
			switch word {
			case "ATTACH", "DETACH", "VACUUM", "VIRTUAL", "TRIGGER":
				return errUnsafeSQL
			}
		}
		// 引用函数名也必须检查；SQLite 允许 "load_extension"(...)。
		if i+1 < len(tokens) && tokens[i+1].text == "(" {
			switch word {
			case "LOAD_EXTENSION", "READFILE", "WRITEFILE", "EDIT", "FSDIR":
				return errUnsafeSQL
			}
		}
	}
	switch strings.ToUpper(tokens[0].text) {
	case "CREATE":
		i := 1
		if i < len(tokens) && (strings.EqualFold(tokens[i].text, "TEMP") || strings.EqualFold(tokens[i].text, "TEMPORARY")) {
			i++
		}
		if i < len(tokens) && strings.EqualFold(tokens[i].text, "UNIQUE") {
			i++
		}
		if i >= len(tokens) || tokens[i].kind != 'w' {
			return errUnsafeSQL
		}
		switch strings.ToUpper(tokens[i].text) {
		case "TABLE", "INDEX", "VIEW":
			return nil
		}
	case "INSERT", "DELETE", "BEGIN", "COMMIT", "END", "ROLLBACK", "SAVEPOINT", "RELEASE":
		return nil
	case "PRAGMA":
		// 仅接受 cc-switch dump 使用的两个 PRAGMA；不允许 schema 前缀。
		if len(tokens) < 2 || tokens[1].kind == 'p' {
			return errUnsafeSQL
		}
		name := strings.ToLower(tokens[1].text)
		if name != "foreign_keys" && name != "user_version" {
			return errUnsafeSQL
		}
		if len(tokens) == 2 {
			return nil
		}
		if len(tokens) == 4 && tokens[2].text == "=" && pragmaValue(tokens[3]) {
			return nil
		}
		if len(tokens) == 5 && tokens[2].text == "(" && tokens[4].text == ")" && pragmaValue(tokens[3]) {
			return nil
		}
	}
	return errUnsafeSQL
}

func pragmaValue(token sqlToken) bool {
	if strings.EqualFold(token.text, "ON") || strings.EqualFold(token.text, "OFF") {
		return true
	}
	if token.text == "" {
		return false
	}
	for _, c := range token.text {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
