package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReportApplyStatus(t *testing.T) {
	var got ApplyStatusRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gateways/v1/apply-status" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Fatalf("缺少 Bearer")
		}
		if r.Header.Get("X-Shieldx-Caps-Hash") == "" {
			t.Fatalf("缺少能力哈希")
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("解码失败：%v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	defer srv.Close()

	c, err := New(Config{
		BaseURL: srv.URL, Token: "tok", GatewayID: "gw-1", AgentVersion: "gateway/1.0",
		Capabilities: &Capabilities{Actions: []ActionType{ActionObserve}, MatchFields: []string{"ip"}, MaxRules: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ReportApplyStatus(context.Background(), ApplyStatusRequest{
		AppliedRevision: 7, Status: "applied",
	}); err != nil {
		t.Fatalf("上报失败：%v", err)
	}
	if got.GatewayID != "gw-1" || got.AgentVersion != "gateway/1.0" || got.AppliedRevision != 7 || got.Status != "applied" {
		t.Fatalf("payload 默认值错误：%+v", got)
	}
	if got.AppliedAt.IsZero() {
		t.Fatal("applied_at 应自动补齐")
	}

	if err := c.ReportApplyStatus(context.Background(), ApplyStatusRequest{Status: "applied"}); err == nil {
		t.Fatal("revision=0 应报错")
	}
}
