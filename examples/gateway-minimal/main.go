// gateway-minimal 演示阶段 1 的最小接入：只用核心 Client 手动轮询策略。
//
// 阶段 2 会引入 poller / reporter，把这里的轮询与上报换成生产级实现。
package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"
	"time"

	"github.com/linyu19872008/shield-x-client-go/agent"
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
		log.Fatalf("创建客户端失败: %v", err)
	}
	defer c.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var since int64
	fetch := func() {
		res, err := c.FetchPolicies(ctx, since)
		if err != nil {
			log.Printf("拉策略失败: %v", err)
			return
		}
		if res.NotModified {
			log.Printf("策略未变更（revision=%d）", res.Revision)
			return
		}
		since = res.Revision
		log.Printf("策略已更新：revision=%d count=%d filtered_out=%d",
			res.Revision, len(res.Snapshot.Policies), res.Snapshot.FilteredOut)
	}

	fetch()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fetch()
		}
	}
}
