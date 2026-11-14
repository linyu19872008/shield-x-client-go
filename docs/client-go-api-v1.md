# shield-x-client-go Public API v1.0

> **状态**：v1.0 草案（D1 / D2 / D11 待批准后冻结）
> **日期**：2026-11-14
> **契约版本**：`agent-v1`
> **适用对象**：接入 shield-x 的 gateway 开发方
> **契约源**：shield-x 仓库 `docs/gateway-contract-v1.md`（文档编号 `SHIELDX-IC-001`）

---

## 0. 文档目的与范围

本文定义 `shield-x-client-go` 的**公开 API**：包布局、类型、字段、方法语义、错误模型、生命周期与兼容策略。实现细节、内部结构、测试代码不在本文范围内。

**做**：

- 三个 agent 接口的 Go 客户端：拉策略、拉模板、上报事件
- 可选的轮询器、上报器、磁盘缓冲
- 契约语义的集中实现：304、空策略清空、失败保留旧快照、批次上限、退避
- 可在 CI 中独立运行的协议探针

**不做**（明确边界）：

- 策略求值、模板渲染、JS 挑战、重定向执行、拦截页
- 策略快照落盘（契约要求 gateway 自己做；SDK 只接回 `InitialRevision`）
- 脱敏、真实 IP 解析、事件生成
- shield-x 控制台（console）管理 API
- 配置框架、CLI、metrics exporter

---

## 1. 决策摘要

| # | 决策 | 本草案取值 | 状态 |
|---|---|---|---|
| D1 | 是否内置 Poller / Reporter / Spool / Stats / Transport | **分层内置**：核心 `agent` 只做传输与模型；助手为同仓独立包 | 待批准 |
| D2 | 是否保留 CI 版 raw probe | **保留最小版**：约 10 条协议断言，禁止 import `agent` | 待批准 |
| D3 | `BaseURL` 是否包含 `/shieldx` 前缀 | **包含**，便于反向代理改前缀 | 推荐默认 |
| D4 | `AgentVersion` 必填还是默认 `unknown` | **必填** | 推荐默认 |
| D5 | 事件时间用 `time.Time` 还是 `string` | **`time.Time`** | 推荐默认 |
| D6 | 条件值用命名 `Value` 类型还是 `any` | **命名类型** | 推荐默认 |
| D7 | 动作参数用强类型还是 `map[string]any` | **强类型** | 推荐默认 |
| D8 | `reporter.State` 是否必填 | **必填** | 推荐默认 |
| D9 | Spool 默认是否开启 | **默认关闭**，显式启用 | 推荐默认 |
| D10 | Go 最低版本 | **1.22** | 推荐默认 |
| D11 | 是否先修 shield-x 的 429 `Retry-After` 响应头顺序 | **先修**（见 §12） | 待批准 |

---

## 2. 设计原则

1. **核心可单独用**：只 import `agent` 即可完成三个接口；不强制后台协程或磁盘。
2. **契约语义集中**：304 不是错误、空策略必须清空、失败保留旧快照、不排序、批次上限，只实现一次。
3. **失败模式显式**：每个错误都能判断"可重试 / 需人工 / 应丢弃"，不依赖字符串匹配。
4. **零第三方依赖**：只用标准库，降低 gateway 团队的供应链审计成本。
5. **可观测但不可知**：只暴露 `Stats()` 快照与 `OnError` 回调，不绑定日志框架或 metrics 后端。

---

## 3. 包布局

```text
shield-x-client-go/
├── go.mod                       module github.com/linyu19872008/shield-x-client-go
├── CONTRACT_VERSION             agent-v1
├── agent/                       核心：配置、客户端、模型、错误、能力、版本
├── poller/                      可选：轮询 + 304 + 原子快照 + 退避
├── reporter/                    可选：批量 + gzip/NDJSON + 重试 + 背压
├── reporter/spool/              可选：磁盘缓冲（实现 reporter.Spool）
├── examples/gateway-minimal/    最小接入示例
├── probe/                       仅 CI：raw HTTP 契约探针（build tag）
├── testdata/contract/           契约夹具
└── docs/client-go-api-v1.md     本文
```

依赖方向：

```text
agent  ←  poller
agent  ←  reporter
reporter ← reporter/spool   （spool 实现 reporter.Spool 接口）
probe  →  仅标准库（禁止 import agent）
```

`agent` **不得** import `poller` / `reporter`。

---

## 4. `agent` 核心

### 4.1 `Config`

```go
type Config struct {
    // 必填。含路径前缀，SDK 在其后拼 /policies/v1、/templates/v1、/events/v1。
    // 例：http://shield-x.internal:8080/shieldx
    BaseURL string

    // 必填。租户级 Bearer Token，形如 sx_xxx。
    Token string

    // 必填。1~64 字符，[A-Za-z0-9._-]，租户内唯一且稳定。
    GatewayID string

    // 必填。gateway 自身版本，写入 X-Shieldx-Agent-Version 与批次 agent_version。
    // 例：gateway/1.4.2。不填直接报错——身份不能靠猜。
    AgentVersion string

    // 可选。nil 表示不声明能力，服务端按最小能力集处理。
    Capabilities *Capabilities

    // 可选，二者互斥。
    HTTPClient *http.Client      // 调用方完全拥有；SDK 不设 Timeout、不 Close
    Transport  http.RoundTripper // SDK 用它构造 client，并负责 CloseIdleConnections

    // 可选。默认 3s / 10s；若 ctx 截止时间更早，以 ctx 为准。
    FetchTimeout  time.Duration
    ReportTimeout time.Duration

    // 可选。默认 shield-x-client-go/<version>。
    UserAgent string
}
```

`New` 时的校验：

| 字段 | 规则 |
|---|---|
| `BaseURL` | 非空、绝对地址、scheme 为 http/https、不含 query/fragment |
| `Token` | 非空 |
| `GatewayID` | `^[A-Za-z0-9._-]{1,64}$` |
| `AgentVersion` | 非空 |
| `Capabilities` | 非 nil 时校验动作枚举与版本号 |
| `HTTPClient` / `Transport` | 不能同时设置 |
| 超时 | 大于 0（零值取默认） |

### 4.2 `Client`

```go
func New(cfg Config) (*Client, error)

// 304 不是错误：NotModified=true 且 Snapshot=nil。
func (c *Client) FetchPolicies(ctx context.Context, sinceRevision int64) (PoliciesResult, error)
func (c *Client) FetchTemplates(ctx context.Context, sinceRevision int64) (TemplatesResult, error)

// 低层上报：一次请求，不重试、不拆批（拆批是 reporter 的职责）。
func (c *Client) ReportEvents(ctx context.Context, batch EventBatch, opts ...ReportOption) (ReportResult, error)

// 只关闭 SDK 自己创建的连接。
func (c *Client) Close() error
```

```go
type PoliciesResult struct {
    Revision    int64           // 200 取 body，304 取 ETag
    NotModified bool
    Snapshot    *PolicySnapshot // NotModified 时为 nil
}

type TemplatesResult struct {
    Revision    int64
    NotModified bool
    Snapshot    *TemplateSnapshot
}

type ReportResult struct {
    Accepted   int
    Rejected   int
    ServerTime time.Time
}
```

语义：

| 场景 | 行为 |
|---|---|
| `sinceRevision <= 0` | 不发送 `If-None-Match` |
| `sinceRevision > 0` | 发送 `If-None-Match: "rev-<n>"` |
| 200 | 解码快照，返回 `Snapshot` |
| 304 | 返回 `NotModified=true`，`Revision` 解析自 `ETag`，缺失时回退 `sinceRevision` |
| 4xx/5xx | 返回 `*APIError` |
| 解码失败 | 返回包装错误，保留原始响应片段用于排查 |

`*Client` 并发安全。

### 4.3 策略模型

```go
type PolicySnapshot struct {
    Revision    int64     `json:"revision"`
    GeneratedAt time.Time `json:"generated_at"`
    PolicyCount int       `json:"policy_count"`
    FilteredOut int       `json:"filtered_out"`
    // 保持服务端顺序。SDK 永远不排序——契约 §4.5 第 5 条。
    Policies []Policy `json:"policies"`
}

type Policy struct {
    ID       string `json:"id"`
    Version  int    `json:"version"`
    Name     string `json:"name"`
    Priority int    `json:"priority"`
    Match    Match  `json:"match"`
    Action   Action `json:"action"`
}

type Match struct {
    All []Condition `json:"all,omitempty"`
    Any []Condition `json:"any,omitempty"`
    Not *Match      `json:"not,omitempty"` // 服务端保证最大 4 层
}

type Condition struct {
    Field string `json:"field"`
    Name  string `json:"name,omitempty"` // header / query / cookie 需要
    Op    string `json:"op"`
    Value Value  `json:"value"`
}

// Value 保存原始 JSON：契约 v1 只有 string 或 string[] 两种形态。
// 既不丢未知形态，又提供强类型读取。
type Value []byte

func StringValue(s string) Value
func StringsValue(ss []string) Value
func (v Value) Raw() json.RawMessage
func (v Value) String() (string, error)
func (v Value) Strings() ([]string, error)

type ActionType string

const (
    ActionObserve     ActionType = "observe"
    ActionBlock       ActionType = "block"
    ActionRedirect    ActionType = "redirect"
    ActionJSChallenge ActionType = "javascript_challenge"
    ActionNone        ActionType = "none" // 仅事件使用，能力声明中不允许
)

func (a ActionType) Valid() bool

type Action struct {
    Type   ActionType    `json:"type"`
    Params *ActionParams `json:"params,omitempty"`
}

type ActionParams struct {
    TemplateID    string `json:"template_id,omitempty"`
    StatusCode    int    `json:"status_code,omitempty"`
    RedirectURL   string `json:"redirect_url,omitempty"`
    ChallengeTTLS int    `json:"challenge_ttl_s,omitempty"`
}
```

设计取舍：

- `Value` 用命名字节类型而不是 `any`，避免 JSON 数字被解成 `float64`，也避免调用方到处类型断言。
- `ActionParams` 用强类型而不是 `map[string]any`：契约 v1 的参数集封闭；未来新增字段对旧 SDK 是兼容的（忽略即可），新增动作类型本来就需要升级 SDK。

### 4.4 模板模型

```go
type TemplateSnapshot struct {
    Revision  int64      `json:"revision"`
    Templates []Template `json:"templates"`
}

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

// 校验 checksum；失败时调用方应回退本地缓存或内置默认页（契约 §5.4）。
func (t Template) VerifyChecksum() error
```

模板渲染**不在 SDK 内**：契约 §9 要求各网关独立实现，SDK 只提供 checksum 校验。

### 4.5 事件模型

```go
type Event struct {
    EventID   string    `json:"event_id"`            // 1~64，gateway 内唯一
    TS        time.Time `json:"ts"`                  // 请求开始时间
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
    UpstreamStatus *int  `json:"upstream_status"` // 未转发上游时为 null
    DurationMS     int   `json:"duration_ms"`
    Bytes          int64 `json:"bytes,omitempty"`

    // 未命中必须是 null，不能是空串：所以不用 omitempty。
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

type ActionResult string

const (
    ResultExecuted   ActionResult = "executed"
    ResultChallenged ActionResult = "challenged"
    ResultPassed     ActionResult = "passed"
    ResultFailed     ActionResult = "failed"
    ResultSkipped    ActionResult = "skipped"
)

type DecisionSource string

const (
    DecisionPolicy       DecisionSource = "policy"
    DecisionNone         DecisionSource = "none"
    DecisionGatewayError DecisionSource = "gateway_error"
)

// 批次元信息：agent_version / gateway_id 由 Client 从 Config 自动填充。
type EventBatch struct {
    ConfigRevision int64
    PolicyCount    int
    ClientStats    *ClientStats
    Events         []Event
}

type ClientStats struct {
    QueueDepth   int   `json:"queue_depth"`
    DroppedTotal int64 `json:"dropped_total"`
    SpoolSize    int   `json:"spool_size"`
}
```

### 4.6 `Event.Validate()`

```go
func (e Event) Validate() error

type ValidationError struct {
    EventIndex int
    Field      string
    Err        error
}
```

| 校验项 | 规则 |
|---|---|
| `event_id` | 非空且 ≤64 |
| `ts` | 非零值（服务端容忍 −24h/+1h，调用方需保证时钟同步） |
| `client_ip` | `net.ParseIP` 可解析 |
| `host` / `path` / `method` | 非空 |
| `action` / `action_result` / `decision_source` | 枚举合法 |
| 三态一致性 | `decision_source=policy` ⇔ `matched_policy_id != nil` 且非空串 |

服务端只校验"有 ID 时必须是 policy"，**没有**校验"是 policy 时必须给 ID"。SDK 两侧都校验，提前挡住统计失真。

### 4.7 错误模型

服务端错误信封：`{"error":{"code","message","request_id"}}`；响应头另有 `X-Request-Id`。

```go
type APIError struct {
    StatusCode int
    Code       string        // 如 UNAUTHORIZED、BATCH_TOO_LARGE、QUEUE_FULL
    Message    string
    RequestID  string
    RetryAfter time.Duration // 解析自 Retry-After，缺失为 0
}

func (e *APIError) Error() string
func (e *APIError) Retryable() bool // 429 / 5xx

func IsRetryable(err error) bool // 含网络错误、超时
func IsAuthError(err error) bool // 401 / 403
func RetryAfter(err error) (time.Duration, bool)
```

哨兵错误：

```go
var (
    ErrInvalidConfig = errors.New("shield-x-client-go: 配置非法")
    ErrInvalidEvent  = errors.New("shield-x-client-go: 事件字段非法")
    ErrBatchTooLarge = errors.New("shield-x-client-go: 批次超过 1000 条或 1MB")
)
```

`304` **永远不产生 error**。

### 4.8 能力与版本

```go
type Capabilities struct {
    Actions             []ActionType `json:"actions"`
    MatchFields         []string     `json:"match_fields"`
    BlockTemplates      bool         `json:"block_templates"`
    TemplateSpecVersion int          `json:"template_spec_version"`
    MaxRules            int          `json:"max_rules"`
    SupportsRateLimit   bool         `json:"supports_rate_limit"`
}

func (c Capabilities) Validate() error

const ContractVersion = "agent-v1"

func Version() string // 来自 debug.ReadBuildInfo；go run 时为 "dev"
```

### 4.9 编码与请求头

| 项 | 规则 |
|---|---|
| 策略/模板 | `GET`，`Accept: application/json` |
| 事件 | 默认 `application/x-ndjson` + `gzip`；可选 `application/json` |
| NDJSON | 第一行批次元信息，后续每行一个事件 |
| gzip | 压缩后再计算/发送；`Content-Encoding: gzip` |
| 请求头 | `Authorization`、`User-Agent`、`X-Shieldx-Agent-Version` |
| 策略请求 | 额外 `X-Shieldx-Gateway-Id`、`X-Shieldx-Capabilities` |
| 模板请求 | 额外 `X-Shieldx-Gateway-Id` |
| 批次上限 | 1000 条或 1 MiB（压缩前），超限本地报错 |
| Token | 不写日志、不进错误信息、不出现在 `String()` 输出 |

```go
type Format string

const (
    FormatJSON   Format = "json"
    FormatNDJSON Format = "ndjson"
)
```

`ReportOption`：

```go
type ReportOption func(*reportOptions)

func WithFormat(f Format) ReportOption
func WithGzip(v bool) ReportOption
func WithSkipValidation() ReportOption
```

---

## 5. `poller`

```go
type Options struct {
    Interval        time.Duration // 默认 5s，最小 1s
    Timeout         time.Duration // 默认 3s
    InitialRevision int64         // 从磁盘快照恢复时传入

    OnSnapshot    func(*agent.PolicySnapshot) // 仅 revision 变化时回调
    OnNotModified func(revision int64)        // 304 时回调（可选）
    OnError       func(error)                 // 失败回调，旧快照保持可用

    FetchTemplates bool // 默认 false
    OnTemplates    func(*agent.TemplateSnapshot)
}

func New(c *agent.Client, opts Options) (*Poller, error)

func (p *Poller) Run(ctx context.Context) error     // 阻塞；cancel 即停
func (p *Poller) Refresh(ctx context.Context) error // 手动触发一次
func (p *Poller) RefreshTemplates(ctx context.Context) error
func (p *Poller) Snapshot() *agent.PolicySnapshot   // 未成功前为 nil
func (p *Poller) Stats() Stats

type Stats struct {
    Polls         uint64
    Success       uint64
    NotModified   uint64
    Failures      uint64
    LastRevision  int64
    LastSuccess   time.Time
    LastError     error
    CurrentBackoff time.Duration
}
```

语义：

| 场景 | 行为 |
|---|---|
| 200 | 整体原子替换；`Policies` 为空也替换（清空） |
| 304 | 不回调 `OnSnapshot`，只更新 `Stats.LastSuccess` |
| 失败 | 保留旧快照，`OnError`，指数退避 1s→60s（±20%） |
| `Retry-After` | 优先于退避计算 |
| revision 未变 | 视为无变更，不重复回调 |
| 回调 | 单协程串行；要求快速返回，不阻塞轮询 |
| 模板 | 默认不轮询；`FetchTemplates=true` 时策略更新后顺带拉一次，或手动 `RefreshTemplates` |

不做快照落盘：序列化格式与 `fail_open/fail_closed` 是网关策略，SDK 只用 `InitialRevision` 接回恢复后的版本。

---

## 6. `reporter`

```go
type Options struct {
    BatchSize     int           // 默认 300，上限 1000
    MaxBatchBytes int           // 默认 1 MiB（压缩前）
    FlushInterval time.Duration // 默认 2s
    QueueSize     int           // 默认 10000 条

    Format     agent.Format   // 默认 NDJSON
    Gzip       bool           // 默认 true
    Timeout    time.Duration  // 默认 10s
    MaxBackoff time.Duration  // 默认 60s

    Spool Spool // nil = 纯内存

    // 必填：批次里的 config_revision / policy_count。
    State func() (revision int64, policyCount int)

    Validate        bool // 默认 true
    OnError         func(error)
    OnDrop          func(n int, reason string)
    ShutdownTimeout time.Duration // 默认 5s
}

func New(c *agent.Client, opts Options) (*Reporter, error)

func (r *Reporter) Run(ctx context.Context) error // 阻塞；cancel 后做最后一次 flush
func (r *Reporter) Enqueue(ev agent.Event) bool   // 永不阻塞；false = 已丢弃
func (r *Reporter) Flush(ctx context.Context) error
func (r *Reporter) Stats() Stats

type Stats struct {
    Enqueued    uint64
    Dropped     uint64
    Sent        uint64
    Accepted    uint64
    Rejected    uint64
    Retried     uint64
    Spooled     uint64
    Replayed    uint64
    QueueDepth  int
    SpoolFiles  int
    SpoolBytes  int64
    LastError   error
    LastSuccess time.Time
}
```

失败处理矩阵：

| 响应 | 行为 |
|---|---|
| 202 | 清空该批；累加 `accepted/rejected` |
| 413 | 拆半重发，不原样重试 |
| 429 | 按 `Retry-After` 退避；缺失则指数退避 |
| 400 | 丢弃该批并 `OnDrop("server_rejected")`，不重试 |
| 401/403 | 告警 + 退避重试，不丢数据 |
| 5xx / 超时 / 网络 | 有 spool 写 spool；无 spool 退回内存队列头部 |
| 队列满 | 丢最旧，`dropped_total++`（契约 §6.8） |

生命周期：`Run` 内只允许一个 goroutine；`Enqueue` 并发安全；`Run` 退出前按 `ShutdownTimeout` 做最后一次 flush。

---

## 7. `spool`

```go
// 定义在 reporter 包；实现在 reporter/spool。
type Spool interface {
    Save(ctx context.Context, payload []byte, meta Meta) error
    Replay(ctx context.Context, fn func([]byte, Meta) error) error
    Stats() SpoolStats
    Trim(ctx context.Context) error
}

type Meta struct {
    Format     agent.Format
    Gzip       bool
    Events     int
    EnqueuedAt time.Time
}

type SpoolStats struct {
    Files  int
    Bytes  int64
    Events int
}
```

磁盘实现：

```go
func NewDisk(dir string, opts ...Option) (*Disk, error)

// WithMaxBytes(200 << 20)
// WithMaxFiles(10000)
```

文件格式：

- 文件名：`v1-<unixnano>-<seq>-<events>-<format>-<gz|plain>.bin`
- 内容：已经编码好的请求体原文，补发时原样发送
- 文件名即元信息，不需要 sidecar
- 写入用临时文件 + rename 保证原子性
- 超限按最旧丢弃并计数

---

## 8. Transport 注入

不单独做 `transport` 包，`Config` 一个字段足够：

| Config 组合 | 行为 |
|---|---|
| 都不设 | SDK 建 keep-alive `http.Transport` + `http.Client`，`Close` 时释放 |
| 只设 `HTTPClient` | 原样使用；SDK 不设 Timeout、不 Close |
| 只设 `Transport` | SDK 用它构造 client，`Close` 时 `CloseIdleConnections` |
| 同时设 | `New` 返回错误 |

mock-gateway 的 `faultTransport` 已实现 `http.RoundTripper`，接入即一行：

```go
Transport: faultTransport
```

断网、延迟、丢包、状态码覆盖全部可注入，SDK 内不需要任何"测试专用"代码。

---

## 9. 契约映射

| 契约条款 | 落在哪 | 验证方式 |
|---|---|---|
| §2.2 拉取 3s / 上报 10s | `Config` 超时 | httptest 延迟 |
| §2.2 指数退避 1s→60s ±20% | `reporter` / `poller` | 可控时钟测试 |
| §2.2 Keep-Alive | 默认 Transport | 连接复用计数 |
| §3.1 `gateway_id` 格式 | `Config` 校验 | 单测 |
| §4.3 304 | `agent` | 夹具 |
| §4.4 空策略合法 | `agent` + `poller` | 夹具 + 回调测试 |
| §4.5 整体替换 / 不排序 | `poller` / 模型 | 顺序断言 |
| §4.7 错误码语义 | `APIError` | 表驱动 |
| §5.4 checksum | `Template.VerifyChecksum` | 单测 |
| §6.2 JSON / NDJSON / gzip | `agent` 编码 | 夹具 |
| §6.3 1000 条 / 1 MiB | `agent` 预检 + `reporter` 拆批 | 单测 |
| §6.5 三态语义 | `Event.Validate` | 单测 |
| §6.8 413 / 429 / 400 | `reporter` | 夹具 |
| §8 能力协商 | `Capabilities` | 头断言 |
| §11 不阻塞请求路径 | `reporter.Enqueue` 非阻塞 | 并发测试 |

---

## 10. 测试矩阵

核心夹具（`testdata/contract/`）：

```text
policies_200.json
policies_empty.json
templates_200.json
error_401.json
error_413.json
```

304 与 429 由 `httptest` handler 直接构造，不需要静态夹具。

| 层 | 用例 |
|---|---|
| `agent` | 200 / 304 / 空策略 / 401 / 403 / 429 / 413 / 500、gzip、NDJSON、解码失败、超时、本地批次超限 |
| `poller` | 304 不回调、空清空、失败保留旧快照、revision 未变不重复、退避时间、并发原子读 |
| `reporter` | 按条数/字节/时间切批、413 拆半、429 退避、400 丢弃、队列满丢最旧、spool 补发、优雅退出 |
| `probe` | 见 §10.1 |

### 10.1 raw probe（CI 专用）

位置：`probe/`，build tag `contract_probe`，只允许标准库。

```bash
SHIELDX_BASE_URL=http://127.0.0.1:8080/shieldx \
SHIELDX_TOKEN=sx_xxx \
go test -tags=contract_probe ./probe -v
```

| # | 断言 | 对应契约 |
|---|---|---|
| P1 | 无 Token → 401，信封含 `code/message/request_id` | §2.1、§4.7 |
| P2 | 带 Token + Capabilities → 200，`policy_count == len(policies)` | §4.2 |
| P3 | 带 `If-None-Match: "rev-N"` → 304，空 body，有 ETag | §4.3 |
| P4 | 空策略租户 → 200 + `policies: []`，不是错误 | §4.4 |
| P5 | JSON 批次 → 202，`accepted+rejected == 发送数`（采样租户除外） | §6.7 |
| P6 | NDJSON + gzip → 202 | §6.2 |
| P7 | 1001 条 → 413 `BATCH_TOO_LARGE` | §6.8 |
| P8 | 触发限流 → 429 且带 `Retry-After` | §6.8 |
| P9 | 两个租户 Token 拿到的策略集合不相交 | §12 E1 |
| P10 | 非法 body → 400，错误码稳定 | §4.7 |

环境不满足的用例显式 `t.Skip` 并打印原因，不静默通过。

---

## 11. 版本与兼容

| 项 | 规则 |
|---|---|
| 模块路径 | `github.com/linyu19872008/shield-x-client-go` |
| Go 版本 | `go 1.22` |
| 第三方依赖 | 零 |
| 契约版本 | `agent-v1`（`CONTRACT_VERSION` 文件 + `agent.ContractVersion` 常量） |
| 发布 | `v0.1.0` 起；`v1.0.0` 在契约冻结后 |
| 兼容矩阵 | client `v0.1.x` ↔ contract `agent-v1` ↔ shield-x `v0.9.x` |
| 变更策略 | 1.0 前允许 minor 破坏，但需在 CHANGELOG 标注；1.0 后仅新增 |

README 必须包含快速开始、兼容矩阵、probe 用法。

---

## 12. 与服务端实现的核对结论

以下为 2026-11-14 对照 shield-x 源码的核对结果，影响 SDK 行为与 probe 断言：

1. **429 的 `Retry-After` 实际没有发出。** `shield-x/internal/server/handlers_event.go` 先 `writeError`（已 `WriteHeader`）再 `Header().Set("Retry-After")`，按 Go 语义该头会被丢弃；契约 §6.8 要求 429 必须带该头。建议先修 shield-x（D11）；修之前 SDK 必须容忍缺失并退回指数退避。
2. **契约说单批 ≤1 MiB，服务端 HTTP body 上限是 8 MiB**（`maxBodyBytes = 8 << 20`），只硬限 1000 条。SDK 按契约在本地卡 1 MiB；probe 的 413 用"1001 条"触发，避免依赖字节上限。
3. **采样会让 `accepted + rejected < 发送数`**：服务端对采样丢弃的事件不计数。契约 §6.7 的"应等于"需要补采样例外；SDK/reporter 不能把差额当成丢数据告警。
4. **服务端 `ts` 使用 RFC3339Nano 解析并校验 ±24h/+1h 偏差**；SDK 不重复做时钟校验，但文档要求调用方保证 NTP。

---

## 13. 实施阶段

| 阶段 | 内容 | 规模 |
|---|---|---|
| 1 | `agent` 核心：Config / Client / 模型 / 错误 / 校验 + 夹具 + `examples/gateway-minimal` | 3–4 天 |
| 2 | `poller` / `reporter` / `spool` / Stats + 全量单测 | 4–6 天 |
| 3 | raw probe + CI + README / API 文档 + `v0.1.0` tag | 2 天（probe 0.5–1 天） |

阶段 1 验收：`go test -race ./...` 全绿、示例可运行、零第三方依赖、304/空策略/gzip/NDJSON/413/429 夹具齐全。

---

## 变更记录

| 版本 | 日期 | 变更 |
|---|---|---|
| v1.0-draft | 2026-11-14 | 首版：核心 client、poller、reporter、spool、transport 注入、错误模型、测试矩阵与 raw probe |

---

## 附录 A：最小示例

> 阶段 1 的 `examples/gateway-minimal` 只用核心 `agent.Client` 手动轮询；下面这段 poller/reporter 版本在阶段 2 落地后替换。

```go
package main

import (
    "context"
    "log"
    "os/signal"
    "syscall"
    "time"

    "github.com/linyu19872008/shield-x-client-go/agent"
    "github.com/linyu19872008/shield-x-client-go/poller"
    "github.com/linyu19872008/shield-x-client-go/reporter"
)

func main() {
    c, err := agent.New(agent.Config{
        BaseURL:      "http://127.0.0.1:8080/shieldx",
        Token:        "sx_xxx",
        GatewayID:    "gw-demo-01",
        AgentVersion: "gateway/1.4.2",
        Capabilities: &agent.Capabilities{
            Actions:             []agent.ActionType{agent.ActionObserve, agent.ActionBlock},
            MatchFields:         []string{"ip", "host", "path"},
            BlockTemplates:      true,
            TemplateSpecVersion: 1,
            MaxRules:            5000,
        },
    })
    if err != nil {
        log.Fatal(err)
    }
    defer c.Close()

    p, err := poller.New(c, poller.Options{
        Interval: 5 * time.Second,
        OnSnapshot: func(s *agent.PolicySnapshot) {
            log.Printf("revision=%d policies=%d", s.Revision, len(s.Policies))
        },
        OnError: func(err error) { log.Printf("poll: %v", err) },
    })
    if err != nil {
        log.Fatal(err)
    }

    r, err := reporter.New(c, reporter.Options{
        State: func() (int64, int) {
            s := p.Snapshot()
            if s == nil {
                return 0, 0
            }
            return s.Revision, len(s.Policies)
        },
        OnError: func(err error) { log.Printf("report: %v", err) },
    })
    if err != nil {
        log.Fatal(err)
    }

    ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer stop()

    go func() { _ = p.Run(ctx) }()
    go func() { _ = r.Run(ctx) }()

    <-ctx.Done()
}
```

---

## 附录 B：术语

| 术语 | 含义 |
|---|---|
| **gateway** | 任何实现本契约的网关（自研、OpenResty、Nginx 插件等），不特指某一项目或版本 |
| **agent** | 本 SDK 中负责与 shield-x 通信的客户端 |
| **contract** | shield-x 与 gateway 之间的接口契约，当前为 `agent-v1` |
| **poller** | 定时拉取策略、维护原子快照的组件 |
| **reporter** | 批量、压缩、重试、落盘补发事件上报的组件 |
| **spool** | 上报失败时的磁盘缓冲 |
| **probe** | 不依赖 SDK 的裸 HTTP 契约探针，仅用于 CI |
| **snapshot** | 某一 revision 下的全量策略集合，必须整体替换 |
