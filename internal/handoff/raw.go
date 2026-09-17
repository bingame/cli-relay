package handoff

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bingame/cli-relay/internal/safeio"
)

// RawExtract 不依赖内部会话文件的完整 schema，仅识别稳定的文本主干。
// 所有已识别用户文本完整保留（明显凭据除外）；超限返回错误，不静默截断。
func RawExtract(path string, maxBytes int64) (string, error) {
	if maxBytes <= 0 {
		return "", fmt.Errorf("会话读取上限必须大于 0")
	}
	data, err := safeio.ReadRegular(path, maxBytes)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		var event map[string]any
		if json.Unmarshal(bytes.TrimSpace(line), &event) != nil {
			continue
		}
		for _, record := range extractEvent(event) {
			if strings.TrimSpace(record.Text) == "" {
				continue
			}
			out.WriteString(EncodeRecord(record.Role, record.Text))
		}
		if int64(out.Len()) > maxBytes {
			return "", fmt.Errorf("提取后的会话文本超过大小上限，请明确提高上限后重试")
		}
	}
	if strings.TrimSpace(out.String()) == "" {
		return "", fmt.Errorf("会话文件中没有可识别的完整用户、助手或工具文本")
	}
	return strings.TrimSpace(out.String()), nil
}

type rawRecord struct {
	Role string
	Text string
}

// EncodeRecord 将来源保留在 JSON 外层字段，文本中的伪造标签不会变成新事件。
func EncodeRecord(role, text string) string {
	if role != "user" && role != "assistant" && role != "tool" {
		role = "tool"
	}
	data, _ := json.Marshal(struct {
		Role string `json:"role"`
		Text string `json:"text"`
	}{role, Sanitize(text)})
	return string(data) + "\n"
}

func extractEvent(event map[string]any) []rawRecord {
	typeName, _ := event["type"].(string)
	if typeName == "response_item" {
		if payload, ok := event["payload"].(map[string]any); ok {
			return extractEvent(payload)
		}
	}
	if typeName == "event_msg" {
		payload, _ := event["payload"].(map[string]any)
		kind, _ := payload["type"].(string)
		switch kind {
		case "user_message":
			text, _ := payload["message"].(string)
			return []rawRecord{{"user", text}}
		case "agent_message":
			text, _ := payload["message"].(string)
			return []rawRecord{{"assistant", text}}
		}
		return nil
	}
	if typeName == "function_call" || typeName == "custom_tool_call" {
		return []rawRecord{{"tool", toolSummary(event)}}
	}
	if typeName == "function_call_output" || typeName == "custom_tool_call_output" {
		return []rawRecord{{"tool", stringifyClean(event["output"])}}
	}
	message := event
	if nested, ok := event["message"].(map[string]any); ok {
		message = nested
	}
	role, _ := message["role"].(string)
	if role == "" && (typeName == "user" || typeName == "assistant") {
		role = typeName
	}
	if role != "user" && role != "assistant" && role != "tool" {
		return nil
	}
	if channel, _ := message["channel"].(string); channel == "analysis" || channel == "commentary" && typeName == "reasoning" {
		return nil
	}
	label := role
	content := message["content"]
	if text, ok := content.(string); ok {
		return []rawRecord{{label, text}}
	}
	items, _ := content.([]any)
	records := []rawRecord{}
	for _, item := range items {
		part, ok := item.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := part["type"].(string)
		switch kind {
		case "text", "input_text", "output_text":
			text, _ := part["text"].(string)
			records = append(records, rawRecord{label, text})
		case "tool_use":
			records = append(records, rawRecord{"tool", toolSummary(part)})
		case "tool_result":
			records = append(records, rawRecord{"tool", contentText(part["content"])})
		}
	}
	return records
}

func contentText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	if items, ok := value.([]any); ok {
		var parts []string
		for _, item := range items {
			if part, ok := item.(map[string]any); ok {
				kind, _ := part["type"].(string)
				if kind == "text" || kind == "output_text" {
					if text, ok := part["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func toolSummary(event map[string]any) string {
	summary := map[string]any{}
	for _, key := range []string{"name", "id", "call_id", "input", "arguments"} {
		if value, ok := event[key]; ok {
			summary[key] = value
		}
	}
	return stringifyClean(summary)
}

func stringifyClean(value any) string {
	if text, ok := value.(string); ok {
		var parsed any
		if json.Unmarshal([]byte(text), &parsed) == nil {
			return stringifyClean(parsed)
		}
		return Sanitize(text)
	}
	clean := cleanValue(value)
	if clean == nil {
		return ""
	}
	data, err := json.Marshal(clean)
	if err != nil {
		return ""
	}
	return Sanitize(string(data))
}

func cleanValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		kind, _ := value["type"].(string)
		if kind == "image" || kind == "input_image" || kind == "thinking" || kind == "redacted_thinking" || kind == "reasoning" {
			return nil
		}
		result := map[string]any{}
		for key, child := range value {
			if secretField(key) {
				result[key] = redacted
				continue
			}
			lower := strings.ToLower(key)
			if lower == "thinking" || lower == "signature" || lower == "encrypted_content" || lower == "base64" || lower == "image_url" {
				continue
			}
			if cleaned := cleanValue(child); cleaned != nil {
				result[key] = cleaned
			}
		}
		return result
	case []any:
		result := []any{}
		for _, item := range value {
			if clean := cleanValue(item); clean != nil {
				result = append(result, clean)
			}
		}
		return result
	case string:
		trimmed := strings.TrimSpace(value)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			var parsed any
			if json.Unmarshal([]byte(value), &parsed) == nil {
				return cleanValue(parsed)
			}
		}
		return Sanitize(value)
	default:
		return value
	}
}
