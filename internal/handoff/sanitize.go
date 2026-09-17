package handoff

import (
	"regexp"
	"strings"
)

const redacted = "[已脱敏]"

var credentialAssignments = []*regexp.Regexp{
	regexp.MustCompile(`(?i)((?:authorization|proxy-authorization)["']?\s*[:=]\s*["']?(?:bearer\s+|basic\s+)?)([^\s"',;}\r\n]+)`),
	regexp.MustCompile(`(?i)((?:[a-z][a-z0-9_]*(?:api_key|token|secret|password|access_key|private_key)|api[_-]?key|access[_-]?token|refresh[_-]?token|token|secret|password|client[_-]?secret)["']?\s*[:=]\s*["']?)([^\s"',;}\r\n]+)`),
	regexp.MustCompile(`(?i)((?:--api-key|--token|--password|--secret)\s+)([^\s"',;}\r\n]+)`),
}
var recognizableCredential = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[baprs]-[A-Za-z0-9-]{20,})\b`)
var urlCredential = regexp.MustCompile(`(?i)(https?://)[^\s/@:]+:[^\s/@]+@`)
var imageData = regexp.MustCompile(`(?i)data:image/[a-z0-9.+-]+;base64,[a-z0-9+/=\r\n]+`)

// Sanitize 仅清理明显凭据形式；已知的实际密钥还应由调用方在边界逐值脱敏。
func Sanitize(text string) string {
	for _, pattern := range credentialAssignments {
		text = pattern.ReplaceAllStringFunc(text, func(match string) string {
			parts := pattern.FindStringSubmatch(match)
			if len(parts) < 3 || parts[2] == redacted {
				return match
			}
			return parts[1] + redacted
		})
	}
	text = recognizableCredential.ReplaceAllString(text, redacted)
	text = urlCredential.ReplaceAllString(text, "${1}"+redacted+"@")
	return imageData.ReplaceAllString(text, "[图片数据已省略]")
}

func secretField(key string) bool {
	key = strings.ToUpper(strings.ReplaceAll(key, "-", "_"))
	if key == "TOKEN" || key == "SECRET" || key == "PASSWORD" {
		return true
	}
	for _, marker := range []string{"API_KEY", "APIKEY", "AUTHORIZATION", "_TOKEN", "SECRET", "PASSWORD", "PRIVATE_KEY", "ACCESS_KEY", "CREDENTIAL"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}
