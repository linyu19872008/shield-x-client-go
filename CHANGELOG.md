# Changelog

## v0.1.1 — 2026-11-14

### Fixed

- `FetchPolicies` / `FetchTemplates` 的 `sinceRevision` 语义改为"负数 = 不带条件，
  `>= 0` 发送 `If-None-Match: "rev-N"`"。revision 0 是合法值，空租户现在也能
  在第二次轮询拿到 304，而不是每轮重新拉空快照。
- `poller.InitialRevision` 允许 `-1` 表示"没有本地快照"。

## v0.1.0 — 2026-11-14

首个可用版本，契约版本 `agent-v1`。

### Added

- `agent`：拉策略、拉模板、上报事件；304、空策略、错误信封、能力协商、
  事件三态校验、模板 checksum、JSON/NDJSON/gzip 编码。
- `poller`：轮询、ETag、整体原子替换、失败保留旧快照、指数退避、
  可选模板拉取与手动刷新。
- `reporter`：非阻塞入队、批量/定时 flush、队列满丢最旧、413 拆半、
  429 按 `Retry-After` 退避、spool 落盘与补发、运行统计。
- `reporter/spool`：按批次 gzip 存储、原子写、按容量淘汰、损坏文件跳过。
- `probe`：不依赖 SDK 的裸 HTTP 契约探针（build tag `contract_probe`）。
- CI：构建、vet、gofmt、race、零依赖校验；每日契约探针工作流。
