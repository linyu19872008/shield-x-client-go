# shield-x-client-go

[![CI](https://github.com/linyu19872008/shield-x-client-go/actions/workflows/ci.yml/badge.svg)](https://github.com/linyu19872008/shield-x-client-go/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/linyu19872008/shield-x-client-go)](https://github.com/linyu19872008/shield-x-client-go/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.22%2B-00ADD8.svg)](https://go.dev)
[![Contract](https://img.shields.io/badge/contract-agent--v1-blue.svg)](CONTRACT_VERSION)

**English** | [简体中文](README.zh-CN.md)

Go client for integrating a gateway with **shield-x**: pull policy snapshots, pull block templates, and report request events in batches.

> **Status: private preview.** The module is not on the public Go module proxy. Request repository access first; see [Install](#install). The API is `0.1.x` and may change before `1.0`.

- Contract version: `agent-v1` ([`CONTRACT_VERSION`](CONTRACT_VERSION))
- Reference API guide: [docs/client-go-api-v1.md](docs/client-go-api-v1.md)
- License: [MIT](LICENSE)

## Why this SDK

- **Zero third-party dependencies**: only the Go standard library.
- **304 is not an error**: results distinguish `NotModified` from a snapshot.
- **Contract semantics in one place**: whole-snapshot replacement, empty list clears, failures keep the old snapshot, no sorting, batch limits.
- **Optional poller**: scheduling, ETag, atomic snapshot replacement, exponential backoff, on-demand template refresh.
- **Optional reporter**: non-blocking queue, batching/compression, 413 split, 429 backoff, disk spool replay.
- **Independent contract probe**: raw HTTP checks that do not import the SDK, so an SDK bug cannot mask a server bug.

## Boundaries

| This SDK does | This SDK does not do |
|---|---|
| Pull policies/templates and report events | Evaluate policies, render templates, or enforce actions |
| 304/ETag, empty snapshot, retries, batching | Persist snapshots or decide `fail_open` / `fail_closed` |
| Event validation and capability declaration | Resolve client IPs, redact data, or generate events |
| Optional poller/reporter/spool | Console API, CLI, or configuration framework |

## Requirements

- Go 1.22+
- A shield-x deployment and a site token
- Network access to the shield-x base URL (which includes the `/shieldx` prefix)

## Install

The repository is private during preview:

```bash
export GOPRIVATE=github.com/linyu19872008/*
go get github.com/linyu19872008/shield-x-client-go@v0.1.1
```

For local development, clone the repository and use a `replace` directive. The module has no third-party dependencies, so `go mod download` only needs the standard library.

## Quick start

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

See [`examples/gateway-minimal`](examples/gateway-minimal/main.go) for a complete runnable example.

## Packages

| Package | Purpose |
|---|---|
| [`agent`](agent/) | Core client, models, errors, validation, encoding |
| [`poller`](poller/) | Polling, ETag, atomic snapshots, backoff |
| [`reporter`](reporter/) | Batching, backpressure, 413/429, spool interface |
| [`reporter/spool`](reporter/spool/) | Disk-backed spool |
| [`probe`](probe/) | Raw HTTP contract probe (`contract_probe` build tag) |

## Contract guarantees

| Contract rule | SDK behavior |
|---|---|
| 304 Not Modified | Not an error; `Snapshot=nil`, revision parsed from ETag |
| Empty `policies: []` | Legal; poller replaces with an empty snapshot |
| Failure keeps old snapshot | Poller does not overwrite; reporter spools or requeues |
| No sorting | Server order is preserved |
| Batch ≤1000 events / 1 MiB | Local pre-check with `ErrBatchTooLarge` |
| `matched_policy_id` on miss | Must be `null`; event validation enforces the tri-state rule |
| 429 | Prefer `Retry-After`; otherwise exponential backoff |
| 413 | Reporter splits the batch in half |

## Contract probe

The probe uses only the standard library and **does not import `agent`**:

```bash
SHIELDX_BASE_URL=http://127.0.0.1:8080/shieldx \
SHIELDX_TOKEN=sx_xxx \
go test -tags=contract_probe -v ./probe
```

| Variable | Required | Purpose |
|---|---|---|
| `SHIELDX_BASE_URL` | yes | Base URL including `/shieldx` |
| `SHIELDX_TOKEN` | yes | Site token |
| `SHIELDX_GATEWAY_ID` | no | Defaults to `gw-probe-01` |
| `SHIELDX_PROBE_EMPTY_TOKEN` | no | Empty-site case (P4) |
| `SHIELDX_PROBE_TOKEN_B` | no | Cross-site isolation case (P9) |

Missing required variables skip the probe. P8 (429 + `Retry-After`) is skipped unless the server can trigger rate limiting; P9 requires both sites to have policies.

## Compatibility

| client-go | Contract | shield-x |
|---|---|---|
| `v0.1.x` | `agent-v1` | `v0.9.x` |

`0.1.x` is an early line; minor versions may include breaking changes. Check the [CHANGELOG](CHANGELOG.md) before upgrading.

## Development

```bash
go build ./...
go vet ./...
go test ./...
go test -race ./...
gofmt -l .
go vet -tags=contract_probe ./...
```

CI additionally verifies that only the module itself is listed by `go list -m all`, and that `.codegraph/` / `.cache/` are not tracked.

## Documentation

- [API guide](docs/client-go-api-v1.md)
- [Documentation index](docs/README.md)
- [Changelog](CHANGELOG.md)
- Contract source: shield-x repository `docs/gateway-contract-v1.md` (document `SHIELDX-IC-001`)

## Contributing and security

- [Contributing](CONTRIBUTING.md)
- [Security policy](SECURITY.md)
- [Code of conduct](CODE_OF_CONDUCT.md)

## License

[MIT](LICENSE) © 2026 linyu19872008
