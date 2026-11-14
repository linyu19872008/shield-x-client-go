package reporter

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linyu19872008/shield-x-client-go/agent"
)

func testEvent(id string) agent.Event {
	pid := "pol_01"
	return agent.Event{
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
		Action:          agent.ActionBlock,
		ActionResult:    agent.ResultExecuted,
		DecisionSource:  agent.DecisionPolicy,
	}
}

func newClientFor(t *testing.T, srv *httptest.Server) *agent.Client {
	t.Helper()
	c, err := agent.New(agent.Config{
		BaseURL:      srv.URL + "/shieldx",
		Token:        "sx_test_token",
		GatewayID:    "gw-test-01",
		AgentVersion: "gateway/1.4.2",
	})
	if err != nil {
		t.Fatalf("创建客户端失败: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func newTestReporter(t *testing.T, srv *httptest.Server, mutate ...func(*Options)) *Reporter {
	t.Helper()
	opts := Options{
		BatchSize:     10,
		FlushInterval: time.Hour,
		DisableGzip:   true,
		State:         func() (int64, int) { return 39, 2 },
	}
	for _, m := range mutate {
		m(&opts)
	}
	r, err := New(newClientFor(t, srv), opts)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	return r
}

func okHandler(sizes *[]int) http.HandlerFunc {
	var mu sync.Mutex
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		n := countNDJSONEventsForTest(body)
		mu.Lock()
		*sizes = append(*sizes, n)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprintf(w, `{"accepted":%d,"rejected":0}`, n)
	}
}

func countNDJSONEventsForTest(body []byte) int {
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) == 0 {
		return 0
	}
	return len(lines) - 1
}

func TestFlushBatchesBySize(t *testing.T) {
	var sizes []int
	srv := httptest.NewServer(okHandler(&sizes))
	t.Cleanup(srv.Close)

	r := newTestReporter(t, srv, func(o *Options) { o.BatchSize = 2 })

	for i := 0; i < 5; i++ {
		if !r.Enqueue(testEvent(fmt.Sprintf("e-%d", i))) {
			t.Fatalf("Enqueue e-%d 失败", i)
		}
	}
	if err := r.Flush(context.Background()); err != nil {
		t.Fatalf("Flush 失败: %v", err)
	}

	if len(sizes) != 3 || sizes[0] != 2 || sizes[1] != 2 || sizes[2] != 1 {
		t.Fatalf("批次大小 = %v，期望 [2 2 1]", sizes)
	}
	st := r.Stats()
	if st.Enqueued != 5 || st.Sent != 3 || st.Accepted != 5 || st.QueueDepth != 0 {
		t.Fatalf("统计不符: %+v", st)
	}
}

func TestReporter413Splits(t *testing.T) {
	var sizes []int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		n := countNDJSONEventsForTest(body)
		mu.Lock()
		sizes = append(sizes, n)
		mu.Unlock()
		if n > 2 {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			_, _ = w.Write([]byte(`{"error":{"code":"BATCH_TOO_LARGE","message":"too large"}}`))
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprintf(w, `{"accepted":%d,"rejected":0}`, n)
	}))
	t.Cleanup(srv.Close)

	r := newTestReporter(t, srv, func(o *Options) { o.BatchSize = 4 })
	for i := 0; i < 4; i++ {
		r.Enqueue(testEvent(fmt.Sprintf("e-%d", i)))
	}
	if err := r.Flush(context.Background()); err != nil {
		t.Fatalf("Flush 失败: %v", err)
	}

	if len(sizes) != 3 || sizes[0] != 4 || sizes[1] != 2 || sizes[2] != 2 {
		t.Fatalf("批次大小 = %v，期望 [4 2 2]", sizes)
	}
	st := r.Stats()
	if st.Sent != 2 || st.Accepted != 4 {
		t.Fatalf("统计不符: %+v", st)
	}
}

func TestReporter429Spools(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"QUEUE_FULL","message":"busy"}}`))
	}))
	t.Cleanup(srv.Close)

	sp := &memSpool{}
	r := newTestReporter(t, srv, func(o *Options) { o.Spool = sp })
	r.Enqueue(testEvent("e-1"))
	r.Enqueue(testEvent("e-2"))

	if err := r.Flush(context.Background()); err == nil {
		t.Fatal("期望 Flush 返回 429 错误")
	}

	stats := sp.Stats()
	if stats.Files != 1 || stats.Events != 2 {
		t.Fatalf("spool 统计不符: %+v", stats)
	}
	if st := r.Stats(); st.Spooled != 2 {
		t.Fatalf("Spooled = %d，期望 2", st.Spooled)
	}
}

func TestReporter400Drops(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"INVALID_BODY","message":"bad"}}`))
	}))
	t.Cleanup(srv.Close)

	r := newTestReporter(t, srv)
	r.Enqueue(testEvent("e-1"))
	r.Enqueue(testEvent("e-2"))

	if err := r.Flush(context.Background()); err != nil {
		t.Fatalf("400 批应被丢弃而不是返回错误: %v", err)
	}
	if st := r.Stats(); st.Dropped != 2 || st.QueueDepth != 0 {
		t.Fatalf("统计不符: %+v", st)
	}
	if err := r.Flush(context.Background()); err != nil {
		t.Fatalf("队列已空，第二次 Flush 应成功: %v", err)
	}
}

func TestReporterQueueFullDropsOldest(t *testing.T) {
	r, err := New(&agent.Client{}, Options{
		QueueSize: 2,
		State:     func() (int64, int) { return 1, 1 },
	})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}

	r.Enqueue(testEvent("e-1"))
	r.Enqueue(testEvent("e-2"))
	r.Enqueue(testEvent("e-3"))

	st := r.Stats()
	if st.Enqueued != 3 || st.Dropped != 1 || st.QueueDepth != 2 {
		t.Fatalf("统计不符: %+v", st)
	}
}

func TestReporterRejectsInvalidEvent(t *testing.T) {
	var reasons []string
	r, err := New(&agent.Client{}, Options{
		QueueSize: 10,
		State:     func() (int64, int) { return 1, 1 },
		OnDrop: func(n int, reason string) {
			reasons = append(reasons, reason)
		},
	})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}

	ev := testEvent("e-1")
	ev.MatchedPolicyID = nil // 三态不一致
	if r.Enqueue(ev) {
		t.Fatal("非法事件不应入队")
	}
	if len(reasons) != 1 || reasons[0] != "invalid_event" {
		t.Fatalf("OnDrop = %v", reasons)
	}
	if st := r.Stats(); st.Dropped != 1 || st.Enqueued != 0 {
		t.Fatalf("统计不符: %+v", st)
	}
}

func TestReporterReplaysSpool(t *testing.T) {
	sp := &memSpool{}
	if err := sp.Save(context.Background(), agent.EventBatch{
		ConfigRevision: 39,
		PolicyCount:    2,
		Events:         []agent.Event{testEvent("e-1"), testEvent("e-2")},
	}); err != nil {
		t.Fatalf("预置 spool 失败: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":2,"rejected":0}`))
	}))
	t.Cleanup(srv.Close)

	r := newTestReporter(t, srv, func(o *Options) { o.Spool = sp })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for r.Stats().Replayed < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run 未退出")
	}

	if st := r.Stats(); st.Replayed != 2 {
		t.Fatalf("Replayed = %d，期望 2", st.Replayed)
	}
	if stats := sp.Stats(); stats.Events != 0 {
		t.Fatalf("补发后 spool 应为空: %+v", stats)
	}
}

func TestNextBackoffHonorsRetryAfter(t *testing.T) {
	r := &Reporter{opts: Options{MaxBackoff: time.Minute}}
	d := r.nextBackoff(&agent.APIError{StatusCode: http.StatusTooManyRequests, RetryAfter: 7 * time.Second})
	if d != 7*time.Second {
		t.Fatalf("退避 = %v，期望 7s", d)
	}
}

func TestNewReporterValidation(t *testing.T) {
	c := &agent.Client{}
	cases := []struct {
		name string
		c    *agent.Client
		opts Options
	}{
		{"client 为 nil", nil, Options{State: func() (int64, int) { return 1, 1 }}},
		{"缺少 State", c, Options{}},
		{"BatchSize 过大", c, Options{BatchSize: 1001, State: func() (int64, int) { return 1, 1 }}},
		{"格式非法", c, Options{Format: agent.Format("xml"), State: func() (int64, int) { return 1, 1 }}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.c, tc.opts); err == nil {
				t.Fatal("期望错误")
			}
		})
	}
}

// memSpool 是测试用内存 Spool。
type memSpool struct {
	mu      sync.Mutex
	batches []agent.EventBatch
}

func (s *memSpool) Save(_ context.Context, batch agent.EventBatch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches = append(s.batches, batch)
	return nil
}

func (s *memSpool) Replay(_ context.Context, fn func(agent.EventBatch) error) error {
	s.mu.Lock()
	pending := append([]agent.EventBatch{}, s.batches...)
	s.batches = nil
	s.mu.Unlock()

	for _, b := range pending {
		if err := fn(b); err != nil {
			s.mu.Lock()
			s.batches = append([]agent.EventBatch{b}, s.batches...)
			s.mu.Unlock()
			return err
		}
	}
	return nil
}

func (s *memSpool) Stats() SpoolStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out SpoolStats
	out.Files = len(s.batches)
	for _, b := range s.batches {
		out.Events += len(b.Events)
	}
	return out
}

func (s *memSpool) Trim(context.Context) error { return nil }
