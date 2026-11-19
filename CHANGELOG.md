# Changelog

**English** | [简体中文](CHANGELOG.zh-CN.md)

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Optional `Config.GatewayGroup`, sent as `X-Shieldx-Gateway-Group` so policies can
  target a gateway group.

### Changed

- Terminology: tenant → site across code, docs and probes. Wire protocol is unchanged.

## [v0.1.1] - 2026-11-14

### Fixed

- Treat a negative `sinceRevision` as “no conditional header” and send
  `If-None-Match: "rev-0"` for revision 0, so empty tenants can receive 304.
- Allow `poller.InitialRevision = -1` to mean “no local snapshot”.

## [v0.1.0] - 2026-11-14

First usable release implementing the shield-x `agent-v1` contract.

### Added

- `agent`: policy/template pulls, event reporting, 304, empty snapshot, error envelope,
  capability declaration, event tri-state validation, template checksum, JSON/NDJSON/gzip.
- `poller`: scheduling, ETag, atomic snapshot replacement, backoff, optional template refresh.
- `reporter`: non-blocking queue, batching, 413 split, 429 backoff, disk spool and replay.
- `reporter/spool`: per-batch gzip files, atomic writes, capacity trimming, corrupt-file skip.
- `probe`: raw HTTP contract probe behind the `contract_probe` build tag.

[v0.1.1]: https://github.com/linyu19872008/shield-x-client-go/releases/tag/v0.1.1
[v0.1.0]: https://github.com/linyu19872008/shield-x-client-go/releases/tag/v0.1.0
