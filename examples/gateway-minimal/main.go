// gateway-minimal 演示 gateway 侧的最小接入：
// poller 拉策略，reporter 上报事件，业务侧只需 Enqueue。
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
		log.Fatalf("创建客户端失败: %v", err)
	}
	defer c.Close()

	p, err := poller.New(c, poller.Options{
		Interval: 5 * time.Second,
		OnSnapshot: func(s *agent.PolicySnapshot) {
			log.Printf("策略已更新：revision=%d count=%d", s.Revision, len(s.Policies))
		},
		OnError: func(err error) {
			log.Printf("拉策略失败（继续用旧快照）: %v", err)
		},
	})
	if err != nil {
		log.Fatalf("创建 poller 失败: %v", err)
	}

	r, err := reporter.New(c, reporter.Options{
		State: func() (int64, int) {
			s := p.Snapshot()
			if s == nil {
				return 0, 0
			}
			return s.Revision, len(s.Policies)
		},
		OnError: func(err error) {
			log.Printf("上报失败（本地缓冲）: %v", err)
		},
	})
	if err != nil {
		log.Fatalf("创建 reporter 失败: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() { _ = p.Run(ctx) }()
	go func() { _ = r.Run(ctx) }()

	// 业务侧：命中策略后构造 agent.Event 并 r.Enqueue(ev)。
	<-ctx.Done()
}
