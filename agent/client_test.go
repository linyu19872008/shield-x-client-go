package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testdata", "contract", name))
	if err != nil {
		t.Fatalf("读取夹具 %s 失败: %v", name, err)
	}
	return b
}

func newTestClient(t *testing.T, h http.Handler, mutate ...func(*Config)) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	cfg := Config{
		BaseURL:      srv.URL + "/shieldx",
		Token:        "sx_test_token",
		GatewayID:    "gw-test-01",
		AgentVersion: "gateway/1.4.2",
		Capabilities: &Capabilities{
			Actions:             []ActionType{ActionObserve, ActionBlock},
			MatchFields:         []string{"ip", "host"},
			BlockTemplates:      true,
			TemplateSpecVersion: 1,
			MaxRules:            5000,
		},
	}
	for _, m := range mutate {
		m(&cfg)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func gunzip(t *testing.T, b []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("gzip 解压失败: %v", err)
	}
	defer zr.Close()
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("读取 gzip 内容失败: %v", err)
	}
	return out
}

func validEvent(id string) Event {
	pid := "pol_01"
	return Event{
		EventID:         id,
		TS:              time.Now(),
		ClientIP:        "203.0.113.7",
		Host:            "example.com",
		Path:            "/login",
		Method:          "GET",
		Status:          403,
		DurationMS:      3,
		ConfigRevision:  39,
		MatchedPolicyID: &pid,
		Action:          ActionBlock,
		ActionResult:    ResultExecuted,
		DecisionSource:  DecisionPolicy,
	}
}

func TestFetchPoliciesOK(t *testing.T) {
	type captured struct {
		path   string
		header http.Header
	}
	ch := make(chan captured, 1)

	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ch <- captured{path: r.URL.Path, header: r.Header.Clone()}
		w.Header().Set("ETag", `"rev-39"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture(t, "policies_200.json"))
	}))

	res, err := c.FetchPolicies(context.Background(), 0)
	if err != nil {
		t.Fatalf("FetchPolicies 失败: %v", err)
	}
	if res.NotModified {
		t.Fatal("不应为 NotModified")
	}
	if res.Revision != 39 {
		t.Fatalf("revision = %d，期望 39", res.Revision)
	}
	if got := len(res.Snapshot.Policies); got != 2 {
		t.Fatalf("策略数 = %d，期望 2", got)
	}
	if res.Snapshot.Policies[0].ID != "pol_01" || res.Snapshot.Policies[1].ID != "pol_02" {
		t.Fatalf("策略顺序被改变: %s, %s", res.Snapshot.Policies[0].ID, res.Snapshot.Policies[1].ID)
	}

	got := <-ch
	if got.path != "/shieldx/policies/v1" {
		t.Fatalf("路径 = %q", got.path)
	}
	if v := got.header.Get("Authorization"); v != "Bearer sx_test_token" {
		t.Fatalf("Authorization = %q", v)
	}
	if v := got.header.Get("X-Shieldx-Gateway-Id"); v != "gw-test-01" {
		t.Fatalf("Gateway-Id = %q", v)
	}
	if v := got.header.Get("X-Shieldx-Agent-Version"); v != "gateway/1.4.2" {
		t.Fatalf("Agent-Version = %q", v)
	}
	if v := got.header.Get("If-None-Match"); v != "" {
		t.Fatalf("since=0 不应带 If-None-Match，实际 %q", v)
	}
	if v := got.header.Get("User-Agent"); !strings.HasPrefix(v, "shield-x-client-go/") {
		t.Fatalf("User-Agent = %q", v)
	}

	var caps Capabilities
	if err := json.Unmarshal([]byte(got.header.Get("X-Shieldx-Capabilities")), &caps); err != nil {
		t.Fatalf("解析能力头失败: %v", err)
	}
	if len(caps.Actions) != 2 || caps.MaxRules != 5000 {
		t.Fatalf("能力头内容不符: %+v", caps)
	}
}

func TestFetchPolicies304(t *testing.T) {
	ch := make(chan http.Header, 1)
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ch <- r.Header.Clone()
		w.Header().Set("ETag", `"rev-39"`)
		w.WriteHeader(http.StatusNotModified)
	}))

	res, err := c.FetchPolicies(context.Background(), 39)
	if err != nil {
		t.Fatalf("304 不应返回错误: %v", err)
	}
	if !res.NotModified {
		t.Fatal("NotModified 应为 true")
	}
	if res.Snapshot != nil {
		t.Fatal("304 时 Snapshot 应为 nil")
	}
	if res.Revision != 39 {
		t.Fatalf("revision = %d，期望 39", res.Revision)
	}
	if got := (<-ch).Get("If-None-Match"); got != `"rev-39"` {
		t.Fatalf("If-None-Match = %q", got)
	}
}

func TestFetchPoliciesEmpty(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(fixture(t, "policies_empty.json"))
	}))
	res, err := c.FetchPolicies(context.Background(), 0)
	if err != nil {
		t.Fatalf("空策略不应返回错误: %v", err)
	}
	if res.Snapshot == nil {
		t.Fatal("Snapshot 不应为 nil")
	}
	if res.Revision != 40 {
		t.Fatalf("revision = %d，期望 40", res.Revision)
	}
	if len(res.Snapshot.Policies) != 0 {
		t.Fatalf("策略数 = %d，期望 0", len(res.Snapshot.Policies))
	}
	if res.Snapshot.Policies == nil {
		t.Fatal("Policies 应规范化为空切片而不是 nil")
	}
}

func TestFetchPoliciesAPIError(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "hdr-req")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write(fixture(t, "error_401.json"))
	}))

	_, err := c.FetchPolicies(context.Background(), 0)
	if err == nil {
		t.Fatal("期望错误")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("错误类型不是 *APIError: %T", err)
	}
	if apiErr.StatusCode != 401 || apiErr.Code != "UNAUTHORIZED" {
		t.Fatalf("错误内容不符: %+v", apiErr)
	}
	if apiErr.RequestID != "req-401" {
		t.Fatalf("request_id = %q，期望取信封里的 req-401", apiErr.RequestID)
	}
	if !IsAuthError(err) {
		t.Fatal("应识别为认证错误")
	}
	if IsRetryable(err) {
		t.Fatal("401 不应可重试")
	}
}

func TestRetryAfter(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"QUEUE_FULL","message":"busy","request_id":"req-429"}}`))
	}))

	_, err := c.ReportEvents(context.Background(), EventBatch{Events: []Event{validEvent("e-1")}})
	if err == nil {
		t.Fatal("期望错误")
	}
	if !IsRetryable(err) {
		t.Fatal("429 应可重试")
	}
	d, ok := RetryAfter(err)
	if !ok || d != 7*time.Second {
		t.Fatalf("RetryAfter = %v, %v，期望 7s", d, ok)
	}
}

func TestFetchTemplatesAndChecksum(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/shieldx/templates/v1" {
			t.Errorf("路径 = %q", r.URL.Path)
		}
		w.Header().Set("ETag", `"rev-39"`)
		_, _ = w.Write(fixture(t, "templates_200.json"))
	}))

	res, err := c.FetchTemplates(context.Background(), 0)
	if err != nil {
		t.Fatalf("FetchTemplates 失败: %v", err)
	}
	if res.Snapshot == nil || len(res.Snapshot.Templates) != 1 {
		t.Fatalf("模板数不符: %+v", res.Snapshot)
	}
	tpl := res.Snapshot.Templates[0]
	if err := tpl.VerifyChecksum(); err != nil {
		t.Fatalf("checksum 应通过: %v", err)
	}
	tampered := tpl
	tampered.Content += "x"
	if err := tampered.VerifyChecksum(); err == nil {
		t.Fatal("篡改内容后 checksum 应失败")
	}
}

func TestFetchTimeout(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(250 * time.Millisecond)
		_, _ = w.Write(fixture(t, "policies_200.json"))
	}), func(cfg *Config) {
		cfg.FetchTimeout = 40 * time.Millisecond
	})

	_, err := c.FetchPolicies(context.Background(), 0)
	if err == nil {
		t.Fatal("期望超时错误")
	}
	if !IsRetryable(err) {
		t.Fatalf("超时应可重试，实际错误: %v", err)
	}
}

func TestReportEventsNDJSONGzip(t *testing.T) {
	type captured struct {
		contentType     string
		contentEncoding string
		body            []byte
	}
	ch := make(chan captured, 1)

	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ch <- captured{
			contentType:     r.Header.Get("Content-Type"),
			contentEncoding: r.Header.Get("Content-Encoding"),
			body:            body,
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":2,"rejected":0,"server_time":"2026-11-14T00:21:05+08:00"}`))
	}))

	unmatched := validEvent("e-2")
	unmatched.MatchedPolicyID = nil
	unmatched.DecisionSource = DecisionNone
	unmatched.Action = ActionNone
	unmatched.ActionResult = ResultPassed

	batch := EventBatch{
		ConfigRevision: 39,
		PolicyCount:    2,
		Events:         []Event{validEvent("e-1"), unmatched},
	}
	res, err := c.ReportEvents(context.Background(), batch)
	if err != nil {
		t.Fatalf("ReportEvents 失败: %v", err)
	}
	if res.Accepted != 2 || res.Rejected != 0 {
		t.Fatalf("上报结果不符: %+v", res)
	}
	if res.ServerTime.IsZero() {
		t.Fatal("server_time 未解析")
	}

	got := <-ch
	if got.contentType != "application/x-ndjson" {
		t.Fatalf("Content-Type = %q", got.contentType)
	}
	if got.contentEncoding != "gzip" {
		t.Fatalf("Content-Encoding = %q", got.contentEncoding)
	}

	raw := gunzip(t, got.body)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 {
		t.Fatalf("行数 = %d，期望 3（元信息 + 2 事件）", len(lines))
	}

	var meta struct {
		AgentVersion   string `json:"agent_version"`
		GatewayID      string `json:"gateway_id"`
		ConfigRevision int64  `json:"config_revision"`
		PolicyCount    int    `json:"policy_count"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &meta); err != nil {
		t.Fatalf("解析元信息失败: %v", err)
	}
	if meta.AgentVersion != "gateway/1.4.2" || meta.GatewayID != "gw-test-01" {
		t.Fatalf("元信息不符: %+v", meta)
	}
	if meta.ConfigRevision != 39 || meta.PolicyCount != 2 {
		t.Fatalf("元信息 revision/policy_count 不符: %+v", meta)
	}
	if !strings.Contains(lines[1], `"matched_policy_id":"pol_01"`) {
		t.Fatalf("命中事件缺少 matched_policy_id: %s", lines[1])
	}
	if !strings.Contains(lines[2], `"matched_policy_id":null`) {
		t.Fatalf("未命中事件必须序列化为 null: %s", lines[2])
	}
}

func TestReportEventsJSON(t *testing.T) {
	ch := make(chan []byte, 1)
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ch <- body
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":1,"rejected":0,"server_time":"2026-11-14T00:21:05+08:00"}`))
	}))

	_, err := c.ReportEvents(context.Background(),
		EventBatch{ConfigRevision: 1, PolicyCount: 1, Events: []Event{validEvent("e-1")}},
		WithFormat(FormatJSON), WithGzip(false),
	)
	if err != nil {
		t.Fatalf("ReportEvents 失败: %v", err)
	}

	var wire struct {
		AgentVersion   string  `json:"agent_version"`
		GatewayID      string  `json:"gateway_id"`
		ConfigRevision int64   `json:"config_revision"`
		Events         []Event `json:"events"`
	}
	if err := json.Unmarshal(<-ch, &wire); err != nil {
		t.Fatalf("解析 JSON 请求体失败: %v", err)
	}
	if wire.AgentVersion != "gateway/1.4.2" || wire.GatewayID != "gw-test-01" {
		t.Fatalf("元信息不符: %+v", wire)
	}
	if len(wire.Events) != 1 || wire.Events[0].EventID != "e-1" {
		t.Fatalf("事件不符: %+v", wire.Events)
	}
}

func TestReportEventsBatchTooLarge(t *testing.T) {
	var called atomic.Bool
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":0,"rejected":0}`))
	}))

	events := make([]Event, MaxBatchEvents+1)
	_, err := c.ReportEvents(context.Background(), EventBatch{Events: events})
	if !errors.Is(err, ErrBatchTooLarge) {
		t.Fatalf("错误 = %v，期望 ErrBatchTooLarge", err)
	}
	if called.Load() {
		t.Fatal("超限批次不应发出请求")
	}
}

func TestReportEventsRejectsInvalidEvent(t *testing.T) {
	var called atomic.Bool
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
	}))

	ev := validEvent("e-1")
	ev.MatchedPolicyID = nil // 三态不一致：policy 但无 ID
	_, err := c.ReportEvents(context.Background(), EventBatch{Events: []Event{ev}})
	if !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("错误 = %v，期望 ErrInvalidEvent", err)
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("应携带 *ValidationError，实际 %T", err)
	}
	if ve.EventIndex != 0 {
		t.Fatalf("EventIndex = %d，期望 0", ve.EventIndex)
	}
	if called.Load() {
		t.Fatal("校验失败不应发出请求")
	}
}

func TestReportEvents413(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = w.Write(fixture(t, "error_413.json"))
	}))

	_, err := c.ReportEvents(context.Background(), EventBatch{Events: []Event{validEvent("e-1")}})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("错误类型不是 *APIError: %T", err)
	}
	if apiErr.StatusCode != 413 || apiErr.Code != "BATCH_TOO_LARGE" {
		t.Fatalf("错误内容不符: %+v", apiErr)
	}
}
