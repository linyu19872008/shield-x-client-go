# shield-x-client-go Public API

> Contract: `agent-v1` · Module: `github.com/linyu19872008/shield-x-client-go` · Status: `0.1.x` preview
>
> English is the default entry point. A Chinese translation is available at
> [zh-CN/client-go-api-v1.md](zh-CN/client-go-api-v1.md).

## 1. Scope

This document describes the public API of `shield-x-client-go`. The SDK only implements the
gateway-side protocol: policy/template pulls, event reporting, retries, batching and spool.
It does **not** evaluate policies, render templates, or enforce actions.

## 2. Package layout

| Package | Purpose |
|---|---|
| `agent` | Core client, config, models, errors, validation, encoding |
| `poller` | Scheduling, ETag, atomic snapshots, backoff |
| `reporter` | Queue, batching, 413/429, spool interface |
| `reporter/spool` | Disk-backed spool |
| `probe` | Raw HTTP contract probe (`contract_probe` build tag) |

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

| Field | Rule |
|---|---|
| `BaseURL` | Absolute `http(s)` URL; include the `/shieldx` prefix |
| `Token` | Site bearer token; never logged |
| `GatewayID` | 1–64 chars of `[A-Za-z0-9._-]`, stable per gateway |
| `GatewayGroup` | Optional site-local group label (≤64 chars), sent as `X-Shieldx-Gateway-Group` |
| `AgentVersion` | Gateway version, sent as `X-Shieldx-Agent-Version` |
| `Capabilities` | Optional capability declaration |
| `HTTPClient` / `Transport` | Mutually exclusive; caller owns an injected `HTTPClient` |
| `FetchTimeout` | Default 3s |
| `ReportTimeout` | Default 10s |
| `UserAgent` | Default `shield-x-client-go/<version>` |

`agent.New(cfg)` validates the config and returns a thread-safe `*Client`.

### 3.2 Client methods

```go
func (c *Client) FetchPolicies(ctx context.Context, sinceRevision int64) (PoliciesResult, error)
func (c *Client) FetchTemplates(ctx context.Context, sinceRevision int64) (TemplatesResult, error)
func (c *Client) ReportEvents(ctx context.Context, batch EventBatch, opts ...ReportOption) (ReportResult, error)
func (c *Client) Close() error
```

- `sinceRevision < 0`: do not send `If-None-Match`.
- `sinceRevision >= 0`: send `If-None-Match: "rev-N"`; revision 0 is legal.
- 304: `NotModified=true`, `Snapshot=nil`; the client returns no error.
- 200: decode the full snapshot and preserve server order.
- 4xx/5xx: return `*APIError` with status, code, message, request ID and `Retry-After`.

### 3.3 Models

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

`Match`, `Condition`, `Action`, `ActionParams`, `TemplateSnapshot`, `Template`, `Event`,
`EventBatch` and `ClientStats` mirror the contract. `Condition.Value` keeps the raw JSON value
so unknown shapes survive a round trip.

### 3.4 Event validation

`Event.Validate()` checks:

- `event_id`: 1–64 chars
- `ts`: non-zero
- `client_ip`: valid IP
- `host`, `path`, `method`: non-empty
- `action`, `action_result`, `decision_source`: contract values
- tri-state: `decision_source=policy` requires a non-empty policy ID; other sources must not
  carry a non-empty policy ID

`matched_policy_id` must serialize as `null` when no policy matched.

### 3.5 Errors

```go
type APIError struct {
    StatusCode int
    Code       string
    Message    string
    RequestID  string
    RetryAfter time.Duration
}

func (e *APIError) Retryable() bool // 429 or 5xx
func IsRetryable(err error) bool
func IsAuthError(err error) bool
func RetryAfter(err error) (time.Duration, bool)
```

Sentinel errors: `ErrInvalidConfig`, `ErrInvalidEvent`, `ErrBatchTooLarge`.
`ValidationError` identifies the event index and field.

### 3.6 Encoding

| Item | Rule |
|---|---|
| Policies/templates | `GET`, `Accept: application/json` |
| Events | Default `application/x-ndjson` + gzip; JSON optional |
| NDJSON | First line is batch metadata, then one event per line |
| Headers | `Authorization`, `User-Agent`, `X-Shieldx-Agent-Version` |
| Policy request | Adds `X-Shieldx-Gateway-Id`, `X-Shieldx-Capabilities` |
| Template request | Adds `X-Shieldx-Gateway-Id` |
| Limits | 1000 events or 1 MiB before compression |
| Compression | Set `Content-Encoding: gzip` after compressing |

Batch metadata fields: `agent_version`, `gateway_id`, `config_revision`, `policy_count`,
optional `client_stats`.

### 3.7 Capabilities

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

`Validate()` rejects empty/duplicate actions, empty match fields, non-positive `MaxRules`,
negative template spec version, and `BlockTemplates` without spec version 1.

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

Behavior:

- defaults: interval 5s, timeout 3s, initial revision -1
- immediate first poll; `Run` blocks until context cancellation
- 304 does not re-apply a snapshot
- empty `policies: []` clears the local snapshot
- failure keeps the old snapshot
- backoff 1s → 60s with ±20% jitter; `Retry-After` wins, capped at 60s
- `Refresh()` and `RefreshTemplates()` perform one immediate pull
- `Snapshot()` is read-only; `Stats()` returns counters and last error

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

Failure matrix:

| Response | Behavior |
|---|---|
| 202 | Clear batch; add accepted/rejected |
| 413 | Split in half and resend |
| 429 | Use `Retry-After`, else exponential backoff |
| 400 | Drop batch, no retry |
| 401/403 | Alert and retry, do not drop |
| 5xx/timeout/network | Spool if configured, else requeue |
| Queue full | Drop oldest, increment `dropped_total` |

`Enqueue` never blocks. `Run` owns a single dispatcher goroutine. Cancellation triggers a final
flush within `ShutdownTimeout`.

## 6. `reporter/spool`

`spool.NewDisk(dir, opts...)` stores one gzip JSON batch per file:

```text
v1-<epochNanos>-<seq>-<events>.json.gz
```

- temp file + atomic rename
- directory `0700`, files `0600`
- trim by file count/bytes, keeping at least the newest file
- replay oldest-first; corrupt files are removed
- `Stats()` returns files, bytes and pending events

## 7. Probe

```bash
SHIELDX_BASE_URL=http://127.0.0.1:8080/shieldx \
SHIELDX_TOKEN=sx_xxx \
go test -tags=contract_probe -v ./probe
```

The probe uses raw HTTP and does not import `agent`. It covers 200/304/401, empty snapshots,
JSON/NDJSON+gzip, 413, 429 (when triggerable), site isolation and invalid bodies.

## 8. Compatibility

| client-go | Contract | shield-x |
|---|---|---|
| `v0.1.x` | `agent-v1` | `v0.9.x` |

`0.1.x` may include breaking changes in minor versions. See [CHANGELOG](../CHANGELOG.md).

## 9. Minimal example

See [`examples/gateway-minimal`](../examples/gateway-minimal/main.go).

## 10. Version

`agent.Version()` returns the build version from Go module metadata, or `dev` for `go run`.
