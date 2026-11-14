// Package poller 定时拉取 shield-x 策略快照，并维护一个原子可读的本地快照。
//
// 语义来自契约 §4：整体替换、空策略清空、失败保留旧快照、304 不重复应用。
package poller

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/linyu19872008/shield-x-client-go/agent"
)

const (
	defaultInterval = 5 * time.Second
	minInterval     = time.Second
	defaultTimeout  = 3 * time.Second
	baseBackoff     = time.Second
	maxBackoff      = 60 * time.Second
)

// Options 配置 Poller。
type Options struct {
	Interval        time.Duration // 默认 5s，最小 1s
	Timeout         time.Duration // 默认 3s
	InitialRevision int64         // 从磁盘快照恢复时传入；-1 表示没有本地快照

	OnSnapshot    func(*agent.PolicySnapshot) // 仅 revision 变化时回调
	OnNotModified func(revision int64)        // 304 时回调（可选）
	OnError       func(error)                 // 失败回调，旧快照保持可用

	FetchTemplates bool // 默认 false
	OnTemplates    func(*agent.TemplateSnapshot)
}

// Stats 是 Poller 的运行统计快照。
type Stats struct {
	Polls          uint64
	Success        uint64
	NotModified    uint64
	Failures       uint64
	LastRevision   int64
	LastSuccess    time.Time
	LastError      error
	CurrentBackoff time.Duration
}

// Poller 是策略轮询器。
type Poller struct {
	client *agent.Client
	opts   Options

	pollMu     sync.Mutex // 串行化一次完整拉取
	templateMu sync.Mutex // 串行化模板拉取
	mu         sync.Mutex // 保护快照、revision、stats

	snapshot         *agent.PolicySnapshot
	revision         int64
	templateRevision int64
	stats            Stats
	backoff          time.Duration
	running          bool
}

// New 创建 Poller。
func New(c *agent.Client, opts Options) (*Poller, error) {
	if c == nil {
		return nil, errors.New("poller: client 不能为 nil")
	}
	if opts.Interval == 0 {
		opts.Interval = defaultInterval
	}
	if opts.Interval < minInterval {
		return nil, fmt.Errorf("poller: Interval 不能小于 %s", minInterval)
	}
	if opts.Timeout == 0 {
		opts.Timeout = defaultTimeout
	}
	if opts.Timeout < 0 {
		return nil, errors.New("poller: Timeout 不能为负")
	}
	if opts.InitialRevision < -1 {
		return nil, errors.New("poller: InitialRevision 不能小于 -1")
	}
	return &Poller{
		client:   c,
		opts:     opts,
		revision: opts.InitialRevision,
	}, nil
}

// Run 阻塞轮询，直到 ctx 取消；返回 ctx.Err()。
func (p *Poller) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("poller: ctx 不能为 nil")
	}
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return errors.New("poller: 已在运行")
	}
	p.running = true
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.running = false
		p.mu.Unlock()
	}()

	timer := time.NewTimer(0) // 立即拉一次
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			wait, _ := p.pollOnce(ctx)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			timer.Reset(wait)
		}
	}
}

// Refresh 立即拉取一次，返回本次错误。
func (p *Poller) Refresh(ctx context.Context) error {
	_, err := p.pollOnce(ctx)
	return err
}

// RefreshTemplates 立即拉取一次模板。
func (p *Poller) RefreshTemplates(ctx context.Context) error {
	return p.fetchTemplates(ctx)
}

// Snapshot 返回当前策略快照；首次成功前为 nil。返回值应视为只读。
func (p *Poller) Snapshot() *agent.PolicySnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.snapshot
}

// Stats 返回统计快照。
func (p *Poller) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats
}

type pollOutcome struct {
	wait           time.Duration
	err            error
	notModified    bool
	revision       int64
	snapshot       *agent.PolicySnapshot
	fetchTemplates bool
}

func (p *Poller) pollOnce(ctx context.Context) (time.Duration, error) {
	out := p.pollLocked(ctx)
	if out.err != nil {
		if p.opts.OnError != nil {
			p.opts.OnError(out.err)
		}
		return out.wait, out.err
	}
	if out.notModified {
		if p.opts.OnNotModified != nil {
			p.opts.OnNotModified(out.revision)
		}
	} else {
		if p.opts.OnSnapshot != nil {
			p.opts.OnSnapshot(out.snapshot)
		}
		if out.fetchTemplates {
			_ = p.fetchTemplates(ctx)
		}
	}
	return out.wait, nil
}

func (p *Poller) pollLocked(ctx context.Context) pollOutcome {
	p.pollMu.Lock()
	defer p.pollMu.Unlock()

	p.mu.Lock()
	since := p.revision
	p.mu.Unlock()

	fetchCtx, cancel := p.withTimeout(ctx)
	defer cancel()

	res, err := p.client.FetchPolicies(fetchCtx, since)

	p.mu.Lock()
	defer p.mu.Unlock()

	p.stats.Polls++
	if err != nil {
		p.stats.Failures++
		p.stats.LastError = err
		wait := p.nextBackoffLocked(err)
		p.stats.CurrentBackoff = wait
		return pollOutcome{wait: wait, err: err}
	}

	p.stats.LastSuccess = time.Now()
	p.stats.LastError = nil
	p.backoff = 0
	p.stats.CurrentBackoff = 0

	out := pollOutcome{wait: p.opts.Interval, revision: res.Revision}
	if res.NotModified {
		p.stats.NotModified++
		p.revision = res.Revision
		out.notModified = true
		return out
	}

	p.stats.Success++
	p.revision = res.Revision
	p.stats.LastRevision = res.Revision
	p.snapshot = res.Snapshot
	out.snapshot = res.Snapshot
	out.fetchTemplates = p.opts.FetchTemplates
	return out
}

type templateOutcome struct {
	snapshot *agent.TemplateSnapshot
	err      error
}

func (p *Poller) fetchTemplates(ctx context.Context) error {
	out := p.fetchTemplatesLocked(ctx)
	if out.err != nil {
		if p.opts.OnError != nil {
			p.opts.OnError(out.err)
		}
		return out.err
	}
	if out.snapshot != nil && p.opts.OnTemplates != nil {
		p.opts.OnTemplates(out.snapshot)
	}
	return nil
}

func (p *Poller) fetchTemplatesLocked(ctx context.Context) templateOutcome {
	p.templateMu.Lock()
	defer p.templateMu.Unlock()

	p.mu.Lock()
	since := p.templateRevision
	p.mu.Unlock()

	fetchCtx, cancel := p.withTimeout(ctx)
	defer cancel()

	res, err := p.client.FetchTemplates(fetchCtx, since)
	if err != nil {
		return templateOutcome{err: err}
	}

	p.mu.Lock()
	p.templateRevision = res.Revision
	var snap *agent.TemplateSnapshot
	if !res.NotModified {
		snap = res.Snapshot
	}
	p.mu.Unlock()
	return templateOutcome{snapshot: snap}
}

func (p *Poller) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if p.opts.Timeout > 0 {
		return context.WithTimeout(ctx, p.opts.Timeout)
	}
	return ctx, func() {}
}

// nextBackoffLocked 必须在持有 p.mu 时调用。
func (p *Poller) nextBackoffLocked(err error) time.Duration {
	if d, ok := agent.RetryAfter(err); ok {
		if d > maxBackoff {
			d = maxBackoff
		}
		p.backoff = d
		return d
	}
	if p.backoff < baseBackoff {
		p.backoff = baseBackoff
	} else {
		p.backoff *= 2
		if p.backoff > maxBackoff {
			p.backoff = maxBackoff
		}
	}
	jitter := 0.8 + 0.4*rand.Float64()
	return time.Duration(float64(p.backoff) * jitter)
}
