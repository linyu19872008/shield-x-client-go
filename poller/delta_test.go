package poller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/linyu19872008/shield-x-client-go/agent"
)

func TestPollerMergesDelta(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"revision":1,"policies":[
				{"id":"p1","version":1,"name":"a","priority":10,"match":{"all":[{"field":"ip","op":"eq","value":"1.1.1.1"}]},"action":{"type":"observe"}},
				{"id":"p2","version":1,"name":"b","priority":20,"match":{"all":[{"field":"ip","op":"eq","value":"2.2.2.2"}]},"action":{"type":"observe"}}
			]}`))
			return
		}
		if r.URL.Query().Get("since_revision") != "1" {
			t.Fatalf("第二次请求缺少 since_revision=1：%s", r.URL.RawQuery)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"revision":2,"snapshot":"delta","base_revision":1,
			"upserts":[{"id":"p3","version":1,"name":"c","priority":30,"match":{"all":[{"field":"ip","op":"eq","value":"3.3.3.3"}]},"action":{"type":"observe"}}],
			"deletes":["p1"],"policy_count":2}`))
	}))
	defer srv.Close()

	c, err := agent.New(agent.Config{
		BaseURL: srv.URL, Token: "tok", GatewayID: "gw", AgentVersion: "gateway/1.0",
		Capabilities: &agent.Capabilities{
			Actions: []agent.ActionType{agent.ActionObserve}, MatchFields: []string{"ip"},
			MaxRules: 100, DeltaSnapshot: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(c, Options{Interval: time.Second, InitialRevision: -1})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Refresh(context.Background()); err != nil {
		t.Fatalf("首次刷新失败：%v", err)
	}
	if err := p.Refresh(context.Background()); err != nil {
		t.Fatalf("增量刷新失败：%v", err)
	}
	snap := p.Snapshot()
	if snap == nil || snap.Revision != 2 || len(snap.Policies) != 2 {
		t.Fatalf("合并结果错误：%+v", snap)
	}
	ids := []string{snap.Policies[0].ID, snap.Policies[1].ID}
	if ids[0] != "p3" || ids[1] != "p2" {
		t.Fatalf("合并后顺序/内容错误：%v", ids)
	}
}
