package provider

import (
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
	Extra       map[string]any `json:"extra,omitempty"`
	Source      string         `json:"source"`
	CreatedAt   string         `json:"created_at,omitempty"`
	UpdatedAt   string         `json:"updated_at,omitempty"`
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
	if p.BaseURL != "" {
		u, e := url.Parse(p.BaseURL)
		if e != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("base_url 必须是无凭据、查询参数和片段的 HTTP(S) URL")
		}
	}
	return nil
}
