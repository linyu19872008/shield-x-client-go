# 贡献指南

[English](CONTRIBUTING.md) | **简体中文**

感谢参与 `shield-x-client-go`。

## 开发

要求 Go 1.22+。

```bash
go build ./...
go vet ./...
go test ./...
go test -race ./...
gofmt -l .
go vet -tags=contract_probe ./...
```

CI 还会校验零第三方依赖，以及本地缓存没有被提交。

## 契约变更

本模块实现 `agent-v1`。涉及线格式字段、错误语义、重试行为、批次限制或 ETag 处理的变更，
必须先对照 shield-x 网关契约讨论；夹具与探针要在同一个 PR 中更新。

## Pull Request

- 改动聚焦，不把重构和行为变更混在一起。
- 为新行为和失败路径补测试。
- 用户可见变更更新 `CHANGELOG.zh-CN.md`。
- 不提交 Token、私钥、站点数据或 `.codegraph/`。
- 提交信息清晰；欢迎 conventional commits，但不强制。

## 报告问题

请使用 Bug 模板，附上 SDK 版本、Go 版本、契约版本、预期行为和最小复现。
