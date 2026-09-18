package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

type Provider struct {
	ID          string         `json:"id"`
	DisplayName string         `json:"display_name"`
	Targets     []string       `json:"targets"`
	BaseURL     string         `json:"base_url,omitempty"`
	Model       string         `json:"model,omitempty"`
	SecretMode  string         `json:"secret_mode,omitempty"`
	Status      string         `json:"status,omitempty"`
	ContentHash string         `json:"content_hash,omitempty"`
	Extra       map[string]any `json:"extra,omitempty"`
	Source      string         `json:"source"`
	CreatedAt   string         `json:"created_at,omitempty"`
	UpdatedAt   string         `json:"updated_at,omitempty"`
}

type Model struct {
	ProviderID    string `json:"provider_id"`
	ModelID       string `json:"model_id"`
	DisplayName   string `json:"display_name,omitempty"`
	ContextWindow int64  `json:"context_window,omitempty"`
	IsDefault     bool   `json:"is_default,omitempty"`
	SortOrder     int    `json:"sort_order,omitempty"`
}

func (p Provider) EffectiveSecretMode() string {
	if p.SecretMode == "" {
		return "callback"
	}
	return p.SecretMode
}

func (p Provider) EffectiveStatus() string {
	if p.Status == "" {
		return "active"
	}
	return p.Status
}

func ContentHash(p Provider, models []Model) string {
	p.ContentHash = ""
	p.CreatedAt = ""
	p.UpdatedAt = ""
	data, _ := json.Marshal(struct {
		Provider Provider `json:"provider"`
		Models   []Model  `json:"models"`
	}{p, models})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func Slugify(value string) string {
	value = strings.TrimSpace(value)
	var slug strings.Builder
	lastDash := false
	for _, r := range value {
		valid := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-'
		if valid {
			if slug.Len() == 0 && (r == '_' || r == '-') {
				continue
			}
			slug.WriteRune(r)
			lastDash = r == '-'
		} else if slug.Len() > 0 && !lastDash {
			slug.WriteByte('-')
			lastDash = true
		}
		if slug.Len() >= 96 {
			break
		}
	}
	result := strings.TrimRight(slug.String(), "-_")
	if result == "" {
		result = "provider"
	}
	if result == "provider" || !ValidID(result) {
		sum := sha256.Sum256([]byte(value))
		result += "-" + hex.EncodeToString(sum[:4])
	}
	return strings.ToLower(result)
}

var validID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,119}$`)

func ValidID(id string) bool                   { return validID.MatchString(id) }
func (p Provider) Supports(target string) bool { return slices.Contains(p.Targets, target) }
func (p Provider) Validate() error {
	if !ValidID(p.ID) {
		return fmt.Errorf("供应商 ID 只能包含字母、数字、短横线和下划线，长度 1–120")
	}
	if strings.TrimSpace(p.DisplayName) == "" || len(p.Targets) == 0 {
		return fmt.Errorf("供应商名称和目标 CLI 不能为空")
	}
	for _, target := range p.Targets {
		if !ValidID(target) {
			return fmt.Errorf("无效的目标 CLI")
		}
	}
	if mode := p.EffectiveSecretMode(); mode != "callback" && mode != "env_key" && mode != "env_inline" {
		return fmt.Errorf("无效的密钥模式")
	}
	if status := p.EffectiveStatus(); status != "active" && status != "disabled" {
		return fmt.Errorf("无效的供应商状态")
	}
	if p.BaseURL != "" {
		u, e := url.Parse(p.BaseURL)
		if e != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("base_url 必须是无凭据、查询参数和片段的 HTTP(S) URL")
		}
	}
	return nil
}
