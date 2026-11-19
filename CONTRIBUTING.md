# Contributing

**English** | [简体中文](CONTRIBUTING.zh-CN.md)

Thanks for improving `shield-x-client-go`.

## Development

Requirements: Go 1.22+.

```bash
go build ./...
go vet ./...
go test ./...
go test -race ./...
gofmt -l .
go vet -tags=contract_probe ./...
```

CI also checks zero third-party dependencies and that local caches are not tracked.

## Contract changes

This module implements `agent-v1`. Changes to wire fields, error semantics, retry behavior,
batch limits, or ETag handling must be discussed against the shield-x gateway contract first.
Add or update contract fixtures and the probe in the same pull request.

## Pull requests

- Keep changes focused; avoid mixing refactors with behavior changes.
- Add tests for new behavior and failure paths.
- Update `CHANGELOG.md` for user-visible changes.
- Do not commit tokens, private keys, site data, or `.codegraph/`.
- Use clear commit messages; conventional commits are welcome but not required.

## Reporting bugs

Use the bug report template and include the SDK version, Go version, contract version,
expected behavior, and a minimal reproduction.
