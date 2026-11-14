package poller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linyu19872008/shield-x-client-go/agent"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testdata", "contract", name))
	if err != nil {
		t.Fatalf("读取夹具 %s 失败: %v", name, err)
	}
	return b
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

func TestPollerRunAndNotModified(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("ETag", `"rev-39"`)
			_, _ = w.Write(fixture(t, "policies_200.json"))
			return
		}
		if got := r.Header.Get("If-None-Match"); got != `"rev-39"` {
			t.Errorf("If-None-Match = %q，期望 \"rev-39\"", got)
		}
		w.Header().Set("ETag", `"rev-39"`)
		w.WriteHeader(http.StatusNotModified)
	}))
	t.Cleanup(srv.Close)

	snapCh := make(chan *agent.PolicySnapshot, 4)
	notModifiedCh := make(chan int64, 4)

	p, err := New(newClientFor(t, srv), Options{
		Interval: time.Second,
		Timeout:  time.Second,
		OnSnapshot: func(s *agent.PolicySnapshot) {
			snapCh <- s
		},
		OnNotModified: func(rev int64) {
			notModifiedCh <- rev
		},
		OnError: func(err error) { t.Errorf("OnError: %v", err) },
	})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()

	select {
	case s := <-snapCh:
		if s.Revision != 39 || len(s.Policies) != 2 {
			t.Fatalf("快照不符: revision=%d policies=%d", s.Revision, len(s.Policies))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("等待首次快照超时")
	}

	select {
	case rev := <-notModifiedCh:
		if rev != 39 {
			t.Fatalf("304 revision = %d，期望 39", rev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("等待 304 超时")
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run 返回 %v，期望 context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run 未退出")
	}

	st := p.Stats()
	if st.Success != 1 || st.NotModified < 1 || st.Polls < 2 {
		t.Fatalf("统计不符: %+v", st)
	}
	if st.LastRevision != 39 {
		t.Fatalf("LastRevision = %d", st.LastRevision)
	}
}

func TestPollerEmptySnapshotClears(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = w.Write(fixture(t, "policies_200.json"))
			return
		}
		_, _ = w.Write(fixture(t, "policies_empty.json"))
	}))
	t.Cleanup(srv.Close)

	var snaps []*agent.PolicySnapshot
	p, err := New(newClientFor(t, srv), Options{
		Interval: time.Second,
		OnSnapshot: func(s *agent.PolicySnapshot) {
			snaps = append(snaps, s)
		},
	})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}

	if err := p.Refresh(context.Background()); err != nil {
		t.Fatalf("第一次 Refresh 失败: %v", err)
	}
	if err := p.Refresh(context.Background()); err != nil {
		t.Fatalf("第二次 Refresh 失败: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("OnSnapshot 调用 %d 次，期望 2", len(snaps))
	}
	if len(snaps[1].Policies) != 0 {
		t.Fatalf("空策略快照应清空，实际 %d 条", len(snaps[1].Policies))
	}
	if p.Snapshot().Revision != 40 {
		t.Fatalf("当前 revision = %d，期望 40", p.Snapshot().Revision)
	}
}

func TestPollerKeepsSnapshotOnError(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = w.Write(fixture(t, "policies_200.json"))
			return
		}
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"code":"INTERNAL","message":"boom"}}`))
	}))
	t.Cleanup(srv.Close)

	var errCount atomic.Int64
	p, err := New(newClientFor(t, srv), Options{
		Interval: time.Second,
		OnError:  func(error) { errCount.Add(1) },
	})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}

	if err := p.Refresh(context.Background()); err != nil {
		t.Fatalf("第一次 Refresh 失败: %v", err)
	}
	before := p.Snapshot()
	if before == nil || len(before.Policies) != 2 {
		t.Fatal("第一次快照不符")
	}

	if err := p.Refresh(context.Background()); err == nil {
		t.Fatal("第二次 Refresh 期望失败")
	}
	if p.Snapshot() != before {
		t.Fatal("失败后不应替换旧快照")
	}
	st := p.Stats()
	if st.Failures != 1 || st.CurrentBackoff <= 0 {
		t.Fatalf("失败统计不符: %+v", st)
	}
	if errCount.Load() != 1 {
		t.Fatalf("OnError 调用 %d 次，期望 1", errCount.Load())
	}
}

func TestPollerInitialRevisionSendsETag(t *testing.T) {
	ch := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ch <- r.Header.Get("If-None-Match")
		w.Header().Set("ETag", `"rev-39"`)
		w.WriteHeader(http.StatusNotModified)
	}))
	t.Cleanup(srv.Close)

	p, err := New(newClientFor(t, srv), Options{
		Interval:        time.Second,
		InitialRevision: 39,
	})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	if err := p.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh 失败: %v", err)
	}
	if got := <-ch; got != `"rev-39"` {
		t.Fatalf("If-None-Match = %q", got)
	}
	if p.Stats().NotModified != 1 {
		t.Fatalf("NotModified 统计不符: %+v", p.Stats())
	}
}

func TestPollerFetchTemplates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/shieldx/policies/v1":
			_, _ = w.Write(fixture(t, "policies_200.json"))
		case "/shieldx/templates/v1":
			_, _ = w.Write(fixture(t, "templates_200.json"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	tplCh := make(chan *agent.TemplateSnapshot, 1)
	p, err := New(newClientFor(t, srv), Options{
		Interval:       time.Second,
		FetchTemplates: true,
		OnTemplates: func(s *agent.TemplateSnapshot) {
			tplCh <- s
		},
	})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	if err := p.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh 失败: %v", err)
	}
	select {
	case s := <-tplCh:
		if len(s.Templates) != 1 || s.Templates[0].ID != "tpl_01" {
			t.Fatalf("模板不符: %+v", s)
		}
	case <-time.After(time.Second):
		t.Fatal("等待模板回调超时")
	}
}

func TestNewValidation(t *testing.T) {
	c := &agent.Client{}
	cases := []struct {
		name string
		c    *agent.Client
		opts Options
	}{
		{"client 为 nil", nil, Options{}},
		{"Interval 过小", c, Options{Interval: 100 * time.Millisecond}},
		{"InitialRevision 为负", c, Options{InitialRevision: -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.c, tc.opts); err == nil {
				t.Fatal("期望错误")
			}
		})
	}
}
