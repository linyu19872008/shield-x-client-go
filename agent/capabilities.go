package agent

import (
	"encoding/json"
	"fmt"
)

// Capabilities 是 gateway 能力声明，随拉策略请求发送。
//
// 未声明时服务端按最小能力集处理（仅 observe、不支持自定义模板）。
type Capabilities struct {
	Actions             []ActionType `json:"actions"`
	MatchFields         []string     `json:"match_fields"`
	BlockTemplates      bool         `json:"block_templates"`
	TemplateSpecVersion int          `json:"template_spec_version"`
	MaxRules            int          `json:"max_rules"`
	SupportsRateLimit   bool         `json:"supports_rate_limit"`
}

// Validate 校验能力声明。
func (c Capabilities) Validate() error {
	if len(c.Actions) == 0 {
		return fmt.Errorf("%w: capabilities.actions 不能为空", ErrInvalidConfig)
	}
	seen := make(map[ActionType]bool, len(c.Actions))
	for _, a := range c.Actions {
		if !a.Valid() || a == ActionNone {
			return fmt.Errorf("%w: capabilities.actions 含非法动作 %q", ErrInvalidConfig, a)
		}
		if seen[a] {
			return fmt.Errorf("%w: capabilities.actions 重复动作 %q", ErrInvalidConfig, a)
		}
		seen[a] = true
	}

	if len(c.MatchFields) == 0 {
		return fmt.Errorf("%w: capabilities.match_fields 不能为空", ErrInvalidConfig)
	}
	for _, f := range c.MatchFields {
		if f == "" {
			return fmt.Errorf("%w: capabilities.match_fields 含空字符串", ErrInvalidConfig)
		}
	}

	if c.MaxRules <= 0 {
		return fmt.Errorf("%w: capabilities.max_rules 必须为正数", ErrInvalidConfig)
	}
	if c.TemplateSpecVersion < 0 {
		return fmt.Errorf("%w: capabilities.template_spec_version 不能为负", ErrInvalidConfig)
	}
	if c.BlockTemplates && c.TemplateSpecVersion != 1 {
		return fmt.Errorf("%w: 声明 block_templates 时 template_spec_version 必须为 1", ErrInvalidConfig)
	}
	return nil
}

func (c Capabilities) headerJSON() (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
