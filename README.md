# shield-x-client-go

[![CI](https://github.com/linyu19872008/shield-x-client-go/actions/workflows/ci.yml/badge.svg)](https://github.com/linyu19872008/shield-x-client-go/actions/workflows/ci.yml)

Go client for integrating a gateway with **shield-x**：拉策略快照、拉拦截模板、批量上报请求事件。

契约版本：**`agent-v1`**（见 [`CONTRACT_VERSION`](CONTRACT_VERSION)）。API 设计见 [docs/client-go-api-v1.md](docs/client-go-api-v1.md)。

## 特性

- **核心零依赖**：只使用标准库，Go 1.22+。
- **304 不是错误**：返回值显式区分 `NotModified` 与快照。
- **契约语义集中**：整体替换、空策略清空、失败保留旧快照、不排序、批次上限。
- **可选 poller**：轮询、ETag、原子快照、指数退避、模板按需拉取。
- **可选 reporter**：非阻塞入队、批量/压缩、413 拆批、429 退避、磁盘 spool 补发。
- **契约探针**：不依赖 SDK 的裸 HTTP 校验，避免 SDK 与服务端共享同一错误假设。

## 安装

```bash
go get github.com/linyu19872008/shield-x-client-go@v0.1.0
```

## 快速开始

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

	p, _ := poller.New(c, poller.Options{
		Interval: 5 * time.Second,
		OnSnapshot: func(s *agent.PolicySnapshot) {
			log.Printf("revision=%d policies=%d", s.Revision, len(s.Policies))
		},
	})

	r, _ := reporter.New(c, reporter.Options{
		State: func() (int64, int) {
			s := p.Snapshot()
			if s == nil {
				return 0, 0
			}
			return s.Revision, len(s.Policies)
		},
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() { _ = p.Run(ctx) }()
	go func() { _ = r.Run(ctx) }()

	// 业务侧：命中策略后构造 agent.Event 并 r.Enqueue(ev)。
	<-ctx.Done()
}
```

完整示例见 [`examples/gateway-minimal`](examples/gateway-minimal/main.go)。

## 包

| 包 | 作用 |
|---|---|
| [`agent`](agent/) | 核心客户端、模型、错误、校验、编码 |
| [`poller`](poller/) | 策略轮询、ETag、原子快照、退避 |
| [`reporter`](reporter/) | 批量上报、背压、413/429、spool 接口 |
| [`reporter/spool`](reporter/spool/) | 磁盘缓冲实现 |

## 契约保证

| 契约条款 | SDK 行为 |
|---|---|
| 304 Not Modified | 不是错误；`Snapshot=nil`，`Revision` 从 ETag 解析 |
| 空策略 `policies: []` | 合法；poller 用空快照整体替换 |
| 失败保留旧快照 | poller 不覆盖；reporter 落 spool 或回队列 |
| 不排序 | 模型保持服务端顺序 |
| 批次 ≤1000 条 / 1 MiB | 本地预检，超限返回 `ErrBatchTooLarge` |
| `matched_policy_id` 未命中 | 必须为 `null`；事件校验强制三态一致 |
| 429 | 优先按 `Retry-After` 退避；缺失则指数退避 |
| 413 | reporter 自动拆半重发 |

## 契约探针

探针只使用标准库，**不 import `agent`**，用于在真实 shield-x 实例上校验线格式：

```bash
SHIELDX_BASE_URL=http://127.0.0.1:8080/shieldx \
SHIELDX_TOKEN=sx_xxx \
go test -tags=contract_probe -v ./probe
```

| 环境变量 | 必填 | 用途 |
|---|---|---|
| `SHIELDX_BASE_URL` | 是 | 含 `/shieldx` 前缀的基地址 |
| `SHIELDX_TOKEN` | 是 | 租户 Token |
| `SHIELDX_GATEWAY_ID` | 否 | 默认 `gw-probe-01` |
| `SHIELDX_PROBE_EMPTY_TOKEN` | 否 | 空策略租户用例（P4） |
| `SHIELDX_PROBE_TOKEN_B` | 否 | 跨租户隔离用例（P9） |

未设置必填变量时探针整体 `Skip`；P8（429 + `Retry-After`）默认 `Skip`，由 shield-x 单测覆盖。

## 兼容性

| client-go | 契约 | shield-x |
|---|---|---|
| v0.1.x | `agent-v1` | v0.9.x |

当前为 `v0.1`，1.0 之前 minor 版本可能包含破坏性调整；升级前请查看 CHANGELOG。

## 开发

```bash
go build ./...
go vet ./...
go test ./...
go test -race ./...
gofmt -l .
go vet -tags=contract_probe ./...   # 编译探针
```

CI 会额外校验零第三方依赖（`go list -m all` 只有本模块）。

## 文档

- [公开 API 设计 v1.0](docs/client-go-api-v1.md)
- 契约源：shield-x 仓库 `docs/gateway-contract-v1.md`（文档编号 `SHIELDX-IC-001`）
