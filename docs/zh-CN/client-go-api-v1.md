# shield-x-client-go 公开 API

> 契约：`agent-v1` · 模块：`github.com/linyu19872008/shield-x-client-go` · 状态：`0.1.x` 预览
>
> [English](../client-go-api-v1.md) | **简体中文**

## 1. 范围

本文描述 `shield-x-client-go` 的公开 API。SDK 只实现网关侧协议：拉策略/模板、上报事件、
重试、批量与 spool。它**不**负责策略求值、模板渲染或拦截执行。

## 2. 包布局

| 包 | 作用 |
|---|---|
| `agent` | 核心客户端、配置、模型、错误、校验、编码 |
| `poller` | 轮询、ETag、原子快照、退避 |
| `reporter` | 队列、批量、413/429、spool 接口 |
| `reporter/spool` | 磁盘 spool |
| `probe` | 裸 HTTP 契约探针（`contract_probe` build tag） |

## 3. `agent`

### 3.1 Config

```go
type Config struct {
    BaseURL       string
    Token         string
    GatewayID     string
    AgentVersion  string
    Capabilities  *Capabilities
    HTTPClient    *http.Client
    Transport     http.RoundTripper
    FetchTimeout  time.Duration
    ReportTimeout time.Duration
    UserAgent     string
}
```

| 字段 | 规则 |
|---|---|
| `BaseURL` | 绝对 http(s) 地址；包含 `/shieldx` 前缀 |
| `Token` | 租户 Bearer Token；不写日志 |
| `GatewayID` | 1–64 个 `[A-Za-z0-9._-]` 字符，网关内稳定 |
| `AgentVersion` | 网关版本，写入 `X-Shieldx-Agent-Version` |
| `Capabilities` | 可选能力声明 |
| `HTTPClient` / `Transport` | 互斥；调用方拥有注入的 `HTTPClient` |
| `FetchTimeout` | 默认 3s |
| `ReportTimeout` | 默认 10s |
| `UserAgent` | 默认 `shield-x-client-go/<version>` |

`agent.New(cfg)` 校验配置并返回线程安全的 `*Client`。

### 3.2 方法

```go
func (c *Client) FetchPolicies(ctx context.Context, sinceRevision int64) (PoliciesResult, error)
func (c *Client) FetchTemplates(ctx context.Context, sinceRevision int64) (TemplatesResult, error)
func (c *Client) ReportEvents(ctx context.Context, batch EventBatch, opts ...ReportOption) (ReportResult, error)
func (c *Client) Close() error
```

- `sinceRevision < 0`：不发送 `If-None-Match`。
- `sinceRevision >= 0`：发送 `If-None-Match: "rev-N"`，revision 0 合法。
- 304：`NotModified=true`，`Snapshot=nil`，不是错误。
- 200：解码全量快照，保持服务端顺序。
- 4xx/5xx：返回 `*APIError`，包含 status/code/message/requestId/`Retry-After`。

### 3.3 模型

```go
type PolicySnapshot struct {
    Revision    int64
    GeneratedAt time.Time
    PolicyCount int
    FilteredOut int
    Policies    []Policy
}

type Policy struct {
    ID       string
    Version  int
    Name     string
    Priority int
    Match    Match
    Action   Action
}
```

`Match`、`Condition`、`Action`、`ActionParams`、`TemplateSnapshot`、`Template`、`Event`、
`EventBatch`、`ClientStats` 与契约一致。`Condition.Value` 保留原始 JSON，未知形态可往返。

### 3.4 事件校验

`Event.Validate()` 校验：

- `event_id`：1–64 字符
- `ts`：非零
- `client_ip`：合法 IP
- `host`、`path`、`method`：非空
- `action`、`action_result`、`decision_source`：契约取值
- 三态：`decision_source=policy` 时必须提供非空策略 ID；其他来源不能带非空策略 ID

未命中策略时 `matched_policy_id` 必须序列化为 `null`。

### 3.5 错误

```go
type APIError struct {
    StatusCode int
    Code       string
    Message    string
    RequestID  string
    RetryAfter time.Duration
}

func (e *APIError) Retryable() bool // 429 或 5xx
func IsRetryable(err error) bool
func IsAuthError(err error) bool
func RetryAfter(err error) (time.Duration, bool)
```

哨兵错误：`ErrInvalidConfig`、`ErrInvalidEvent`、`ErrBatchTooLarge`。
`ValidationError` 包含事件下标与字段。

### 3.6 编码

| 项 | 规则 |
|---|---|
| 策略/模板 | `GET`，`Accept: application/json` |
| 事件 | 默认 `application/x-ndjson` + gzip；可选 JSON |
| NDJSON | 第一行批次元信息，后续每行一个事件 |
| 请求头 | `Authorization`、`User-Agent`、`X-Shieldx-Agent-Version` |
| 策略请求 | 额外 `X-Shieldx-Gateway-Id`、`X-Shieldx-Capabilities` |
| 模板请求 | 额外 `X-Shieldx-Gateway-Id` |
| 上限 | 1000 条或压缩前 1 MiB |
| 压缩 | 压缩后设置 `Content-Encoding: gzip` |

批次元信息：`agent_version`、`gateway_id`、`config_revision`、`policy_count`、
可选 `client_stats`。

### 3.7 能力

```go
type Capabilities struct {
    Actions             []ActionType
    MatchFields         []string
    BlockTemplates      bool
    TemplateSpecVersion int
    MaxRules            int
    SupportsRateLimit   bool
}
```

`Validate()` 拒绝空/重复动作、空匹配字段、非正 `MaxRules`、负数模板版本，
以及 `BlockTemplates` 但模板版本不是 1 的组合。

## 4. `poller`

```go
type Options struct {
    Interval        time.Duration
    Timeout         time.Duration
    InitialRevision int64
    OnSnapshot      func(*agent.PolicySnapshot)
    OnNotModified   func(revision int64)
    OnError         func(error)
    FetchTemplates  bool
    OnTemplates     func(*agent.TemplateSnapshot)
}
```

- 默认 interval 5s、timeout 3s、initial revision -1
- 立即首拉；`Run` 阻塞到 context 取消
- 304 不重复应用快照
- 空 `policies: []` 清空本地快照
- 失败保留旧快照
- 退避 1s → 60s，±20% jitter；`Retry-After` 优先，上限 60s
- `Refresh()` / `RefreshTemplates()` 立即拉一次
- `Snapshot()` 只读；`Stats()` 返回计数与最后错误

## 5. `reporter`

```go
type Options struct {
    BatchSize       int
    FlushInterval   time.Duration
    QueueSize       int
    Format          agent.Format
    DisableGzip     bool
    Timeout         time.Duration
    MaxBackoff      time.Duration
    Spool           Spool
    State           func() (revision int64, policyCount int)
    SkipValidation  bool
    OnError         func(error)
    OnDrop          func(n int, reason string)
    ShutdownTimeout time.Duration
}
```

失败矩阵：

| 响应 | 行为 |
|---|---|
| 202 | 清空该批，累计 accepted/rejected |
| 413 | 拆半重发 |
| 429 | 按 `Retry-After`，否则指数退避 |
| 400 | 丢弃该批，不重试 |
| 401/403 | 告警 + 重试，不丢数据 |
| 5xx/超时/网络 | 有 spool 落盘，否则回队列 |
| 队列满 | 丢最旧，`dropped_total++` |

`Enqueue` 不阻塞；`Run` 单 dispatcher goroutine；取消后在 `ShutdownTimeout` 内做最后 flush。

## 6. `reporter/spool`

`spool.NewDisk(dir, opts...)` 每个批次一个 gzip JSON 文件：

```text
v1-<epochNanos>-<seq>-<events>.json.gz
```

- 临时文件 + 原子 rename
- 目录 `0700`，文件 `0600`
- 按文件数/字节淘汰，至少保留最新一个
- 最旧优先补发；损坏文件删除
- `Stats()` 返回文件数、字节数、待补发事件数

## 7. 探针

```bash
SHIELDX_BASE_URL=http://127.0.0.1:8080/shieldx \
SHIELDX_TOKEN=sx_xxx \
go test -tags=contract_probe -v ./probe
```

探针使用裸 HTTP，不 import `agent`。覆盖 200/304/401、空快照、JSON/NDJSON+gzip、413、
429（可触发时）、跨租户隔离与非法 body。

## 8. 兼容性

| client-go | 契约 | shield-x |
|---|---|---|
| `v0.1.x` | `agent-v1` | `v0.9.x` |

`0.1.x` 的 minor 版本可能包含破坏性调整。升级前见 [CHANGELOG](../../CHANGELOG.zh-CN.md)。

## 9. 最小示例

见 [`examples/gateway-minimal`](../../examples/gateway-minimal/main.go)。

## 10. 版本

`agent.Version()` 返回 Go module 构建信息中的版本；`go run` 时为 `dev`。
