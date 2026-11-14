// Package agent 提供 shield-x agent API 的 Go 客户端。
//
// 三个接口：拉策略、拉模板、上报事件。客户端本身不启动后台协程、不落盘、
// 不自动重试；轮询与上报的可靠性由可选的 poller / reporter 包提供。
//
// 契约版本：agent-v1。契约源：shield-x 仓库 docs/gateway-contract-v1.md。
package agent
