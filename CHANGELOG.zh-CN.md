# 变更记录

[English](CHANGELOG.md) | **简体中文**

本文件记录项目的重要变更。

格式基于 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)。

## [v0.1.1] - 2026-11-14

### 修复

- `sinceRevision` 为负数时不再发送条件头；revision 0 也发送
  `If-None-Match: "rev-0"`，空租户可以在后续轮询拿到 304。
- `poller.InitialRevision = -1` 表示“没有本地快照”。

## [v0.1.0] - 2026-11-14

首个可用版本，实现 shield-x `agent-v1` 契约。

### 新增

- `agent`：拉策略/模板、上报事件、304、空策略、错误信封、能力声明、
  事件三态校验、模板 checksum、JSON/NDJSON/gzip。
- `poller`：轮询、ETag、原子快照替换、退避、可选模板刷新。
- `reporter`：非阻塞队列、批量、413 拆半、429 退避、磁盘 spool 与补发。
- `reporter/spool`：按批次 gzip 文件、原子写、容量淘汰、损坏文件跳过。
- `probe`：`contract_probe` build tag 下的裸 HTTP 契约探针。

[v0.1.1]: https://github.com/linyu19872008/shield-x-client-go/releases/tag/v0.1.1
[v0.1.0]: https://github.com/linyu19872008/shield-x-client-go/releases/tag/v0.1.0
