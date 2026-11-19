# shield-x-client-go

[![CI](https://github.com/linyu19872008/shield-x-client-go/actions/workflows/ci.yml/badge.svg)](https://github.com/linyu19872008/shield-x-client-go/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/linyu19872008/shield-x-client-go)](https://github.com/linyu19872008/shield-x-client-go/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.22%2B-00ADD8.svg)](https://go.dev)
[![Contract](https://img.shields.io/badge/contract-agent--v1-blue.svg)](CONTRACT_VERSION)

[English](README.md) | **简体中文**

用于对接 **shield-x** 的 Go 客户端：拉取策略快照、拉取拦截模板、批量上报请求事件。

> **状态：私有预览。** 模块尚未发布到公共 Go module proxy，请先申请仓库访问权限；安装方式见[安装](#安装)。API 处于 `0.1.x`，`1.0` 前可能调整。

- 契约版本：`agent-v1`（[`CONTRACT_VERSION`](CONTRACT_VERSION)）
- API 指南：[docs/zh-CN/client-go-api-v1.md](docs/zh-CN/client-go-api-v1.md)
- 许可证：[MIT](LICENSE)

## 为什么用它

- **核心零第三方依赖**：只使用 Go 标准库。
- **304 不是错误**：结果显式区分 `NotModified` 与快照。
- **契约语义集中**：整体替换、空策略清空、失败保留旧快照、不排序、批次上限。
- **可选 poller**：轮询、ETag、原子快照、指数退避、模板按需刷新。
- **可选 reporter**：非阻塞队列、批量/压缩、413 拆半、429 退避、磁盘 spool 补发。
- **独立契约探针**：不 import SDK 的裸 HTTP 校验，避免 SDK 与服务端共享同一错误假设。

## 边界

| 做 | 不做 |
|---|---|
| 拉策略/模板、上报事件 | 策略求值、模板渲染、拦截执行 |
| 304/ETag、空策略、重试、批量 | 快照持久化、`fail_open` / `fail_closed` |
| 事件校验、能力声明 | 真实 IP 解析、脱敏、事件生成 |
| 可选 poller/reporter/spool | 控制台 API、CLI、配置框架 |

## 要求

- Go 1.22+
- 已部署的 shield-x 与站点 Token
- 能访问 shield-x 的 base URL（包含 `/shieldx` 前缀）

## 安装

仓库目前是私有预览：

```bash
export GOPRIVATE=github.com/linyu19872008/*
go get github.com/linyu19872008/shield-x-client-go@v0.1.1
```

本地开发时 clone 仓库并使用 `replace`。模块没有第三方依赖，`go mod download` 只依赖标准库。

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
		BaseURL:      "http://shield-x.internal:8080/shieldx",
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	p := poller.New(c, poller.Options{
		Interval: 5 * time.Second,
		OnError:  func(err error) { log.Printf("poll failed: %v", err) },
	})

	r := reporter.New(c, reporter.Options{
		BatchSize: 300,
		State: func() (int64, int) {
			if snap := p.Snapshot(); snap != nil {
				return snap.Revision, len(snap.Policies)
			}
			return 0, 0
		},
		OnError: func(err error) { log.Printf("report failed: %v", err) },
	})

	go func() { _ = p.Run(ctx) }()
	if err := r.Run(ctx); err != nil && err != context.Canceled {
		log.Fatal(err)
	}
}
```

完整示例见 [`examples/gateway-minimal`](examples/gateway-minimal/main.go)。

## 包

| 包 | 作用 |
|---|---|
| [`agent`](agent/) | 核心客户端、模型、错误、校验、编码 |
| [`poller`](poller/) | 轮询、ETag、原子快照、退避 |
| [`reporter`](reporter/) | 批量、背压、413/429、spool 接口 |
| [`reporter/spool`](reporter/spool/) | 磁盘 spool 实现 |
| [`probe`](probe/) | 裸 HTTP 契约探针（`contract_probe` build tag） |

## 契约保证

| 契约条款 | SDK 行为 |
|---|---|
| 304 Not Modified | 不是错误；`Snapshot=nil`，revision 从 ETag 解析 |
| 空策略 `policies: []` | 合法；poller 用空快照整体替换 |
| 失败保留旧快照 | poller 不覆盖；reporter 落 spool 或回队列 |
| 不排序 | 模型保持服务端顺序 |
| 批次 ≤1000 条 / 1 MiB | 本地预检，超限返回 `ErrBatchTooLarge` |
| `matched_policy_id` 未命中 | 必须为 `null`；事件校验强制三态一致 |
| 429 | 优先按 `Retry-After`，否则指数退避 |
| 413 | reporter 自动拆半重发 |

## 契约探针

探针只使用标准库，**不 import `agent`**：

```bash
SHIELDX_BASE_URL=http://127.0.0.1:8080/shieldx \
SHIELDX_TOKEN=sx_xxx \
go test -tags=contract_probe -v ./probe
```

| 环境变量 | 必填 | 用途 |
|---|---|---|
| `SHIELDX_BASE_URL` | 是 | 含 `/shieldx` 的基地址 |
| `SHIELDX_TOKEN` | 是 | 站点 Token |
| `SHIELDX_GATEWAY_ID` | 否 | 默认 `gw-probe-01` |
| `SHIELDX_PROBE_EMPTY_TOKEN` | 否 | 空策略站点用例（P4） |
| `SHIELDX_PROBE_TOKEN_B` | 否 | 跨站点隔离用例（P9） |

未设置必填变量时整体 Skip。P8（429 + `Retry-After`）在服务端无法触发限流时 Skip；P9 需要两个站点都有策略。

## 兼容性

| client-go | 契约 | shield-x |
|---|---|---|
| `v0.1.x` | `agent-v1` | `v0.9.x` |

`0.1.x` 是早期版本线，minor 升级可能包含破坏性调整；升级前请查看 [CHANGELOG](CHANGELOG.md)。

## 开发

```bash
go build ./...
go vet ./...
go test ./...
go test -race ./...
gofmt -l .
go vet -tags=contract_probe ./...
```

CI 还会校验零第三方依赖，以及 `.codegraph/` / `.cache/` 没有被提交。

## 文档

- [API 指南](docs/zh-CN/client-go-api-v1.md)
- [文档索引](docs/zh-CN/README.md)
- [变更记录](CHANGELOG.zh-CN.md)
- 契约源：shield-x 仓库 `docs/gateway-contract-v1.md`（文档编号 `SHIELDX-IC-001`）

## 贡献与安全

- [贡献指南](CONTRIBUTING.zh-CN.md)
- [安全策略](SECURITY.zh-CN.md)
- [行为准则](CODE_OF_CONDUCT.zh-CN.md)

## 许可证

[MIT](LICENSE) © 2026 linyu19872008
