//go:build contract_probe

package probe

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// 探针只使用标准库，禁止 import agent —— 它是 SDK 之外的独立协议裁判。
type probeClient struct {
	base      string
	token     string
	gatewayID string
	http      *http.Client
}

func newProbe(t *testing.T) *probeClient {
	t.Helper()
	base := strings.TrimRight(os.Getenv("SHIELDX_BASE_URL"), "/")
	token := os.Getenv("SHIELDX_TOKEN")
	if base == "" || token == "" {
		t.Skip("未设置 SHIELDX_BASE_URL / SHIELDX_TOKEN，跳过契约探针")
	}
	gw := os.Getenv("SHIELDX_GATEWAY_ID")
	if gw == "" {
		gw = "gw-probe-01"
	}
	return &probeClient{
		base:      base,
		token:     token,
		gatewayID: gw,
		http:      &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *probeClient) do(t *testing.T, method, path string, body []byte, headers map[string]string, token string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, c.base+path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("User-Agent", "shield-x-contract-probe/1.0")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatalf("请求 %s %s 失败: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	return resp.StatusCode, resp.Header, data
}

type errorEnvelope struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	} `json:"error"`
}

func decodeError(t *testing.T, body []byte) errorEnvelope {
	t.Helper()
	var env errorEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("解析错误信封失败: %v（body=%s）", err, truncate(body))
	}
	return env
}

func truncate(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

func policiesHeaders(c *probeClient) map[string]string {
	return map[string]string{
		"X-Shieldx-Gateway-Id":    c.gatewayID,
		"X-Shieldx-Agent-Version": "probe/1.0.0",
		"X-Shieldx-Capabilities":  `{"actions":["observe","block"],"match_fields":["ip","host"],"block_templates":false,"template_spec_version":1,"max_rules":5000}`,
	}
}

func TestP1Unauthorized(t *testing.T) {
	c := newProbe(t)
	status, hdr, body := c.do(t, http.MethodGet, "/policies/v1", nil, policiesHeaders(c), "")
	if status != http.StatusUnauthorized {
		t.Fatalf("无 Token 状态码 = %d，期望 401（body=%s）", status, truncate(body))
	}
	env := decodeError(t, body)
	if env.Error.Code == "" {
		t.Fatal("401 响应缺少 error.code")
	}
	if env.Error.RequestID == "" {
		t.Fatal("401 响应缺少 error.request_id")
	}
	if hdr.Get("X-Request-Id") == "" {
		t.Fatal("401 响应缺少 X-Request-Id 头")
	}
}

func TestP2PoliciesAnd304(t *testing.T) {
	c := newProbe(t)
	status, hdr, body := c.do(t, http.MethodGet, "/policies/v1", nil, policiesHeaders(c), c.token)
	if status != http.StatusOK {
		t.Fatalf("拉策略状态码 = %d，期望 200（body=%s）", status, truncate(body))
	}

	var snap struct {
		Revision    int64 `json:"revision"`
		PolicyCount int   `json:"policy_count"`
		Policies    []struct {
			ID       string `json:"id"`
			Priority int    `json:"priority"`
		} `json:"policies"`
	}
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatalf("解析策略快照失败: %v", err)
	}
	if snap.PolicyCount != len(snap.Policies) {
		t.Fatalf("policy_count=%d 与 policies 长度 %d 不一致", snap.PolicyCount, len(snap.Policies))
	}

	etag := hdr.Get("ETag")
	if etag == "" {
		t.Fatal("200 响应缺少 ETag")
	}
	if want := fmt.Sprintf(`"rev-%d"`, snap.Revision); etag != want {
		t.Fatalf("ETag = %q，期望 %q", etag, want)
	}

	// 服务端已排序：priority 降序，同 priority 按 id 升序。
	for i := 1; i < len(snap.Policies); i++ {
		prev, cur := snap.Policies[i-1], snap.Policies[i]
		if prev.Priority < cur.Priority {
			t.Fatalf("策略未按 priority 降序：%s(%d) 在 %s(%d) 前", prev.ID, prev.Priority, cur.ID, cur.Priority)
		}
		if prev.Priority == cur.Priority && prev.ID > cur.ID {
			t.Fatalf("同 priority 未按 id 升序：%s 在 %s 前", prev.ID, cur.ID)
		}
	}

	// P3：带 If-None-Match 应得到空 body 的 304。
	status, hdr304, body304 := c.do(t, http.MethodGet, "/policies/v1", nil, map[string]string{
		"If-None-Match": etag,
	}, c.token)
	if status != http.StatusNotModified {
		t.Fatalf("带 If-None-Match 状态码 = %d，期望 304（body=%s）", status, truncate(body304))
	}
	if len(body304) != 0 {
		t.Fatalf("304 响应体应为空，实际 %d 字节", len(body304))
	}
	if got := hdr304.Get("ETag"); got != etag {
		t.Fatalf("304 ETag = %q，期望 %q", got, etag)
	}
}

func TestP4EmptyTenant(t *testing.T) {
	c := newProbe(t)
	token := os.Getenv("SHIELDX_PROBE_EMPTY_TOKEN")
	if token == "" {
		t.Skip("未设置 SHIELDX_PROBE_EMPTY_TOKEN，跳过空策略租户用例")
	}
	status, _, body := c.do(t, http.MethodGet, "/policies/v1", nil, policiesHeaders(c), token)
	if status != http.StatusOK {
		t.Fatalf("空策略租户状态码 = %d，期望 200（body=%s）", status, truncate(body))
	}
	var snap struct {
		Revision int64             `json:"revision"`
		Policies []json.RawMessage `json:"policies"`
	}
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatalf("解析快照失败: %v", err)
	}
	if len(snap.Policies) != 0 {
		t.Fatalf("空策略租户返回了 %d 条策略", len(snap.Policies))
	}
}

func probeEvent(id string) map[string]any {
	return map[string]any{
		"event_id":          id,
		"ts":                time.Now().UTC().Format(time.RFC3339Nano),
		"client_ip":         "203.0.113.7",
		"host":              "probe.example",
		"path":              "/probe",
		"method":            "GET",
		"status":            200,
		"duration_ms":       1,
		"matched_policy_id": nil,
		"config_revision":   1,
		"action":            "none",
		"action_result":     "passed",
		"decision_source":   "none",
	}
}

func probeBatch(c *probeClient, events []any) map[string]any {
	return map[string]any{
		"agent_version":   "probe/1.0.0",
		"gateway_id":      c.gatewayID,
		"config_revision": 1,
		"policy_count":    0,
		"events":          events,
	}
}

func TestP5JSONBatch(t *testing.T) {
	c := newProbe(t)
	events := []any{probeEvent("probe-1"), probeEvent("probe-2")}
	body, err := json.Marshal(probeBatch(c, events))
	if err != nil {
		t.Fatalf("编码批次失败: %v", err)
	}
	status, _, respBody := c.do(t, http.MethodPost, "/events/v1", body, map[string]string{
		"Content-Type": "application/json",
	}, c.token)
	if status != http.StatusAccepted {
		t.Fatalf("JSON 上报状态码 = %d，期望 202（body=%s）", status, truncate(respBody))
	}
	var out struct {
		Accepted int `json:"accepted"`
		Rejected int `json:"rejected"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		t.Fatalf("解析上报响应失败: %v", err)
	}
	if out.Accepted+out.Rejected > len(events) {
		t.Fatalf("accepted+rejected=%d 大于发送数 %d", out.Accepted+out.Rejected, len(events))
	}
	if out.Accepted+out.Rejected != len(events) {
		t.Logf("注意：accepted+rejected=%d < 发送数 %d（租户采样会丢弃事件）", out.Accepted+out.Rejected, len(events))
	}
}

func TestP6NDJSONGzip(t *testing.T) {
	c := newProbe(t)
	events := []any{probeEvent("probe-nd-1"), probeEvent("probe-nd-2")}

	var raw bytes.Buffer
	meta, _ := json.Marshal(map[string]any{
		"agent_version":   "probe/1.0.0",
		"gateway_id":      c.gatewayID,
		"config_revision": 1,
		"policy_count":    0,
	})
	raw.Write(meta)
	raw.WriteByte('\n')
	for _, ev := range events {
		line, _ := json.Marshal(ev)
		raw.Write(line)
		raw.WriteByte('\n')
	}

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(raw.Bytes()); err != nil {
		t.Fatalf("压缩失败: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("压缩失败: %v", err)
	}

	status, _, respBody := c.do(t, http.MethodPost, "/events/v1", gz.Bytes(), map[string]string{
		"Content-Type":     "application/x-ndjson",
		"Content-Encoding": "gzip",
	}, c.token)
	if status != http.StatusAccepted {
		t.Fatalf("NDJSON+gzip 状态码 = %d，期望 202（body=%s）", status, truncate(respBody))
	}
}

func TestP7BatchTooLarge(t *testing.T) {
	c := newProbe(t)
	events := make([]any, 1001)
	for i := range events {
		events[i] = map[string]any{}
	}
	body, err := json.Marshal(probeBatch(c, events))
	if err != nil {
		t.Fatalf("编码批次失败: %v", err)
	}
	status, _, respBody := c.do(t, http.MethodPost, "/events/v1", body, map[string]string{
		"Content-Type": "application/json",
	}, c.token)
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("1001 条状态码 = %d，期望 413（body=%s）", status, truncate(respBody))
	}
	if env := decodeError(t, respBody); env.Error.Code != "BATCH_TOO_LARGE" {
		t.Fatalf("错误码 = %q，期望 BATCH_TOO_LARGE", env.Error.Code)
	}
}

func TestP8RateLimit(t *testing.T) {
	t.Skip("429 需要服务端限流或队列压力，默认跳过；429 的 Retry-After 由 shield-x 单测覆盖")
}

func TestP9TenantIsolation(t *testing.T) {
	c := newProbe(t)
	tokenB := os.Getenv("SHIELDX_PROBE_TOKEN_B")
	if tokenB == "" {
		t.Skip("未设置 SHIELDX_PROBE_TOKEN_B，跳过跨租户隔离用例")
	}

	idsA := fetchPolicyIDs(t, c, c.token)
	idsB := fetchPolicyIDs(t, c, tokenB)
	if len(idsA) == 0 || len(idsB) == 0 {
		t.Skip("至少一个租户没有策略，无法验证隔离")
	}

	set := make(map[string]bool, len(idsA))
	for _, id := range idsA {
		set[id] = true
	}
	for _, id := range idsB {
		if set[id] {
			t.Fatalf("跨租户泄漏：租户 B 看到了租户 A 的策略 %s", id)
		}
	}
}

func fetchPolicyIDs(t *testing.T, c *probeClient, token string) []string {
	t.Helper()
	status, _, body := c.do(t, http.MethodGet, "/policies/v1", nil, policiesHeaders(c), token)
	if status != http.StatusOK {
		t.Fatalf("拉策略状态码 = %d（token 前缀 %.4s）", status, token)
	}
	var snap struct {
		Policies []struct {
			ID string `json:"id"`
		} `json:"policies"`
	}
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatalf("解析快照失败: %v", err)
	}
	ids := make([]string, 0, len(snap.Policies))
	for _, p := range snap.Policies {
		ids = append(ids, p.ID)
	}
	sort.Strings(ids)
	return ids
}

func TestP10InvalidBody(t *testing.T) {
	c := newProbe(t)
	status, _, body := c.do(t, http.MethodPost, "/events/v1", []byte(`{"events":"not-an-array"}`), map[string]string{
		"Content-Type": "application/json",
	}, c.token)
	if status != http.StatusBadRequest {
		t.Fatalf("非法 body 状态码 = %d，期望 400（body=%s）", status, truncate(body))
	}
	if env := decodeError(t, body); env.Error.Code != "INVALID_BODY" {
		t.Fatalf("错误码 = %q，期望 INVALID_BODY", env.Error.Code)
	}
}
