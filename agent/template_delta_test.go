package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchTemplatesDoesNotRequestDelta(t *testing.T) {
	var rawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"revision":2,"templates":[]}`))
	}))
	defer srv.Close()

	c, err := New(Config{
		BaseURL: srv.URL, Token: "tok", GatewayID: "gw", AgentVersion: "gateway/1.0",
		Capabilities: &Capabilities{Actions: []ActionType{ActionObserve}, MatchFields: []string{"ip"}, MaxRules: 100, DeltaSnapshot: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.FetchTemplates(context.Background(), 1); err != nil {
		t.Fatalf("拉模板失败：%v", err)
	}
	if rawQuery != "" {
		t.Fatalf("模板请求不应带 delta 参数，实际 %q", rawQuery)
	}
}
