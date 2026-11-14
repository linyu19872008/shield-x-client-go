package agent

import (
	"encoding/json"
	"fmt"
	"time"
)

// PolicySnapshot 是一次拉取返回的全量策略快照。
//
// 契约要求整体替换，禁止增量合并：调用方拿到新 revision 后应整体替换本地快照，
// 即使 Policies 为空也必须替换（表示清空）。
type PolicySnapshot struct {
	Revision    int64     `json:"revision"`
	GeneratedAt time.Time `json:"generated_at"`
	PolicyCount int       `json:"policy_count"`
	FilteredOut int       `json:"filtered_out"`

	// Policies 保持服务端顺序。SDK 永远不排序——契约 §4.5 第 5 条。
	Policies []Policy `json:"policies"`
}

// Policy 是一条策略。
type Policy struct {
	ID       string `json:"id"`
	Version  int    `json:"version"`
	Name     string `json:"name"`
	Priority int    `json:"priority"`
	Match    Match  `json:"match"`
	Action   Action `json:"action"`
}

// Match 是匹配条件组，支持 all / any / not 嵌套。
type Match struct {
	All []Condition `json:"all,omitempty"`
	Any []Condition `json:"any,omitempty"`
	Not *Match      `json:"not,omitempty"`
}

// Condition 是单个匹配条件。
type Condition struct {
	Field string `json:"field"`
	Name  string `json:"name,omitempty"` // header / query / cookie 需要
	Op    string `json:"op"`
	Value Value  `json:"value"`
}

// Value 保存条件值的原始 JSON：契约 v1 只有 string 或 string[] 两种形态。
//
// 保留原始字节可以避免丢失未知形态，同时提供强类型读取方法。
type Value []byte

// StringValue 构造字符串条件值。
func StringValue(s string) Value {
	b, _ := json.Marshal(s)
	return Value(b)
}

// StringsValue 构造字符串数组条件值。
func StringsValue(ss []string) Value {
	b, _ := json.Marshal(ss)
	return Value(b)
}

// Raw 返回原始 JSON。
func (v Value) Raw() json.RawMessage { return json.RawMessage(v) }

// String 将值解析为字符串。
func (v Value) String() (string, error) {
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return "", fmt.Errorf("条件值不是字符串: %w", err)
	}
	return s, nil
}

// Strings 将值解析为字符串数组。
func (v Value) Strings() ([]string, error) {
	var ss []string
	if err := json.Unmarshal(v, &ss); err != nil {
		return nil, fmt.Errorf("条件值不是字符串数组: %w", err)
	}
	return ss, nil
}

// MarshalJSON 实现 json.Marshaler。
func (v Value) MarshalJSON() ([]byte, error) {
	if len(v) == 0 {
		return []byte("null"), nil
	}
	return []byte(v), nil
}

// UnmarshalJSON 实现 json.Unmarshaler。
func (v *Value) UnmarshalJSON(b []byte) error {
	*v = append((*v)[:0], b...)
	return nil
}

// ActionType 是动作类型。
type ActionType string

const (
	ActionObserve     ActionType = "observe"
	ActionBlock       ActionType = "block"
	ActionRedirect    ActionType = "redirect"
	ActionJSChallenge ActionType = "javascript_challenge"

	// ActionNone 仅用于事件（未命中任何策略），能力声明中不允许出现。
	ActionNone ActionType = "none"
)

// Action 描述命中策略后执行什么。
type Action struct {
	Type   ActionType    `json:"type"`
	Params *ActionParams `json:"params,omitempty"`
}

// ActionParams 是动作参数。契约 v1 的参数集是封闭的。
type ActionParams struct {
	TemplateID    string `json:"template_id,omitempty"`
	StatusCode    int    `json:"status_code,omitempty"`
	RedirectURL   string `json:"redirect_url,omitempty"`
	ChallengeTTLS int    `json:"challenge_ttl_s,omitempty"`
}

// TemplateSnapshot 是一次模板拉取返回的全量快照。
type TemplateSnapshot struct {
	Revision  int64      `json:"revision"`
	Templates []Template `json:"templates"`
}

// Template 是一条拦截模板。
type Template struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Version     int               `json:"version"`
	Checksum    string            `json:"checksum"` // sha256:<hex>
	ContentType string            `json:"content_type"`
	StatusCode  int               `json:"status_code"`
	Headers     map[string]string `json:"headers"`
	Content     string            `json:"content"`
	Vars        map[string]string `json:"vars"`
}

// ActionResult 是动作执行结果。
type ActionResult string

const (
	ResultExecuted   ActionResult = "executed"
	ResultChallenged ActionResult = "challenged"
	ResultPassed     ActionResult = "passed"
	ResultFailed     ActionResult = "failed"
	ResultSkipped    ActionResult = "skipped"
)

// DecisionSource 是决策来源，必须与 MatchedPolicyID 配合表达三态语义。
type DecisionSource string

const (
	DecisionPolicy       DecisionSource = "policy"
	DecisionNone         DecisionSource = "none"
	DecisionGatewayError DecisionSource = "gateway_error"
)

// Event 是单个请求事件。
//
// 指针字段用于区分「未提供」与「提供了零值」：
// matched_policy_id 未命中必须是 null，而不是空串。
type Event struct {
	EventID   string    `json:"event_id"`
	TS        time.Time `json:"ts"`
	GatewayID string    `json:"gateway_id,omitempty"` // 混批时逐条指定

	ClientIP    string   `json:"client_ip"`
	RealIPChain []string `json:"real_ip_chain,omitempty"`
	Host        string   `json:"host"`
	Path        string   `json:"path"`
	Query       string   `json:"query,omitempty"`
	Method      string   `json:"method"`
	Scheme      string   `json:"scheme,omitempty"`
	UA          string   `json:"ua,omitempty"`
	Referer     string   `json:"referer,omitempty"`

	Status         int   `json:"status"`
	UpstreamStatus *int  `json:"upstream_status"`
	DurationMS     int   `json:"duration_ms"`
	Bytes          int64 `json:"bytes,omitempty"`

	MatchedPolicyID       *string `json:"matched_policy_id"`
	MatchedPolicyPriority *int    `json:"matched_policy_priority,omitempty"`
	ConfigRevision        int64   `json:"config_revision"`

	Action         ActionType     `json:"action"`
	ActionResult   ActionResult   `json:"action_result"`
	DecisionSource DecisionSource `json:"decision_source"`

	DecisionLatencyUS    int               `json:"decision_latency_us,omitempty"`
	BlockTemplateID      *string           `json:"block_template_id,omitempty"`
	BlockTemplateVersion *int              `json:"block_template_version,omitempty"`
	HeadersDigest        map[string]string `json:"headers_digest,omitempty"`
	TraceID              string            `json:"trace_id,omitempty"`
	ClockSkewMS          *int              `json:"clock_skew_ms,omitempty"`
}

// EventBatch 是一次上报的业务字段。
//
// agent_version / gateway_id 由 Client 从 Config 自动填充，调用方不需要关心。
type EventBatch struct {
	ConfigRevision int64
	PolicyCount    int
	ClientStats    *ClientStats
	Events         []Event
}

// ClientStats 让服务端感知上报侧是否在丢数据。
type ClientStats struct {
	QueueDepth   int   `json:"queue_depth"`
	DroppedTotal int64 `json:"dropped_total"`
	SpoolSize    int   `json:"spool_size"`
}

// PoliciesResult 是 FetchPolicies 的结果。304 时 NotModified=true 且 Snapshot=nil。
type PoliciesResult struct {
	Revision    int64
	NotModified bool
	Snapshot    *PolicySnapshot
}

// TemplatesResult 是 FetchTemplates 的结果。
type TemplatesResult struct {
	Revision    int64
	NotModified bool
	Snapshot    *TemplateSnapshot
}

// ReportResult 是上报接口的响应。
type ReportResult struct {
	Accepted   int
	Rejected   int
	ServerTime time.Time
}
