package agent

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		BaseURL:      "http://127.0.0.1:8080/shieldx",
		Token:        "sx_token",
		GatewayID:    "gw-test-01",
		AgentVersion: "gateway/1.4.2",
		Capabilities: &Capabilities{
			Actions:             []ActionType{ActionObserve, ActionBlock},
			MatchFields:         []string{"ip", "host"},
			BlockTemplates:      true,
			TemplateSpecVersion: 1,
			MaxRules:            5000,
		},
	}
}

func TestConfigValidateOK(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("合法配置不应报错: %v", err)
	}
	// 不声明能力也合法。
	cfg := validConfig()
	cfg.Capabilities = nil
	if err := cfg.Validate(); err != nil {
		t.Fatalf("无能力声明不应报错: %v", err)
	}
}

func TestConfigValidateErrors(t *testing.T) {
	longID := strings.Repeat("a", 65)
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"BaseURL 为空", func(c *Config) { c.BaseURL = "" }},
		{"BaseURL 相对地址", func(c *Config) { c.BaseURL = "/shieldx" }},
		{"BaseURL 非 http", func(c *Config) { c.BaseURL = "ftp://example.com/shieldx" }},
		{"BaseURL 带 query", func(c *Config) { c.BaseURL = "http://example.com/shieldx?a=1" }},
		{"Token 为空", func(c *Config) { c.Token = "" }},
		{"GatewayID 过长", func(c *Config) { c.GatewayID = longID }},
		{"GatewayID 非法字符", func(c *Config) { c.GatewayID = "gw test" }},
		{"AgentVersion 为空", func(c *Config) { c.AgentVersion = "" }},
		{"HTTPClient 与 Transport 同时设置", func(c *Config) {
			c.HTTPClient = &http.Client{}
			c.Transport = http.DefaultTransport
		}},
		{"超时为负", func(c *Config) { c.FetchTimeout = -time.Second }},
		{"能力 actions 为空", func(c *Config) {
			c.Capabilities = &Capabilities{MatchFields: []string{"ip"}, MaxRules: 1}
		}},
		{"能力含 none", func(c *Config) {
			c.Capabilities = &Capabilities{
				Actions: []ActionType{ActionNone}, MatchFields: []string{"ip"}, MaxRules: 1,
			}
		}},
		{"能力 max_rules 为 0", func(c *Config) {
			c.Capabilities = &Capabilities{
				Actions: []ActionType{ActionObserve}, MatchFields: []string{"ip"},
			}
		}},
		{"声明模板但版本为 0", func(c *Config) {
			c.Capabilities = &Capabilities{
				Actions: []ActionType{ActionObserve}, MatchFields: []string{"ip"},
				BlockTemplates: true, MaxRules: 1,
			}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("期望校验失败")
			}
		})
	}
}

func TestValueStringAndStrings(t *testing.T) {
	var list Condition
	if err := json.Unmarshal([]byte(`{"field":"ip","op":"in_cidr","value":["203.0.113.0/24"]}`), &list); err != nil {
		t.Fatalf("解析数组条件失败: %v", err)
	}
	ss, err := list.Value.Strings()
	if err != nil || len(ss) != 1 || ss[0] != "203.0.113.0/24" {
		t.Fatalf("Strings() = %v, %v", ss, err)
	}

	var single Condition
	if err := json.Unmarshal([]byte(`{"field":"ua","op":"contains","value":"sqlmap"}`), &single); err != nil {
		t.Fatalf("解析字符串条件失败: %v", err)
	}
	s, err := single.Value.String()
	if err != nil || s != "sqlmap" {
		t.Fatalf("String() = %q, %v", s, err)
	}

	var bad Condition
	if err := json.Unmarshal([]byte(`{"field":"x","op":"eq","value":true}`), &bad); err != nil {
		t.Fatalf("解析布尔条件失败: %v", err)
	}
	if _, err := bad.Value.String(); err == nil {
		t.Fatal("非字符串值应报错")
	}

	b, err := json.Marshal(Condition{Field: "x", Op: "eq"})
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if !strings.Contains(string(b), `"value":null`) {
		t.Fatalf("零值 Value 应序列化为 null: %s", b)
	}
}

func TestEventValidate(t *testing.T) {
	pid := "pol_01"
	id := "e-1"

	cases := []struct {
		name   string
		mutate func(*Event)
		ok     bool
	}{
		{"合法命中事件", func(e *Event) {}, true},
		{"合法未命中事件", func(e *Event) {
			e.MatchedPolicyID = nil
			e.DecisionSource = DecisionNone
			e.Action = ActionNone
			e.ActionResult = ResultPassed
		}, true},
		{"合法防护失效事件", func(e *Event) {
			e.MatchedPolicyID = nil
			e.DecisionSource = DecisionGatewayError
			e.Action = ActionNone
			e.ActionResult = ResultFailed
		}, true},
		{"event_id 为空", func(e *Event) { e.EventID = "" }, false},
		{"event_id 过长", func(e *Event) { e.EventID = strings.Repeat("x", 65) }, false},
		{"ts 为零值", func(e *Event) { e.TS = time.Time{} }, false},
		{"client_ip 非法", func(e *Event) { e.ClientIP = "not-an-ip" }, false},
		{"host 为空", func(e *Event) { e.Host = "" }, false},
		{"action 非法", func(e *Event) { e.Action = "drop" }, false},
		{"action_result 非法", func(e *Event) { e.ActionResult = "ok" }, false},
		{"decision_source 非法", func(e *Event) { e.DecisionSource = "engine" }, false},
		{"policy 但无策略 ID", func(e *Event) { e.MatchedPolicyID = nil }, false},
		{"非 policy 却带策略 ID", func(e *Event) {
			e.DecisionSource = DecisionNone
		}, false},
		{"policy 但策略 ID 为空串", func(e *Event) {
			empty := ""
			e.MatchedPolicyID = &empty
		}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := validEvent(id)
			tc.mutate(&ev)
			err := ev.Validate()
			if tc.ok && err != nil {
				t.Fatalf("期望通过，实际: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("期望失败")
			}
		})
	}

	// 命中事件的指针校验。
	if ev := validEvent(id); ev.MatchedPolicyID == nil || *ev.MatchedPolicyID != pid {
		t.Fatal("validEvent 构造有误")
	}
}

func TestTemplateVerifyChecksumErrors(t *testing.T) {
	tpl := Template{Checksum: "md5:abc", Content: "x"}
	if err := tpl.VerifyChecksum(); err == nil {
		t.Fatal("不支持的算法应报错")
	}
	tpl = Template{Checksum: "sha256:zz", Content: "x"}
	if err := tpl.VerifyChecksum(); err == nil {
		t.Fatal("非法 hex 应报错")
	}
	tpl = Template{
		Checksum: "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		Content:  "not-empty",
	}
	if err := tpl.VerifyChecksum(); err == nil {
		t.Fatal("内容不匹配应报错")
	}
}

func TestActionTypeValid(t *testing.T) {
	for _, a := range []ActionType{ActionObserve, ActionBlock, ActionRedirect, ActionJSChallenge, ActionNone} {
		if !a.Valid() {
			t.Fatalf("%q 应为合法动作", a)
		}
	}
	if ActionType("drop").Valid() {
		t.Fatal("drop 不应合法")
	}
}
