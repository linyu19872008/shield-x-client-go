// Package reporter 负责事件上报的批量、压缩、重试与背压。
//
// Enqueue 永不阻塞请求路径；失败时按契约 §6.8 处理 413/429/400/5xx，
// 可选地把批次写入 Spool 以便断网恢复后补发。
package reporter

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/linyu19872008/shield-x-client-go/agent"
)

const (
	defaultBatchSize       = 300
	defaultFlushInterval   = 2 * time.Second
	defaultQueueSize       = 10000
	defaultTimeout         = 10 * time.Second
	defaultMaxBackoff      = 60 * time.Second
	defaultShutdownTimeout = 5 * time.Second
	baseBackoff            = time.Second
)

// Options 配置 Reporter。
type Options struct {
	BatchSize     int           // 默认 300，上限 1000
	FlushInterval time.Duration // 默认 2s
	QueueSize     int           // 默认 10000 条

	Format      agent.Format  // 默认 NDJSON
	DisableGzip bool          // 默认 false（开启压缩）
	Timeout     time.Duration // 默认 10s
	MaxBackoff  time.Duration // 默认 60s

	Spool Spool // nil = 纯内存

	// State 必填：返回批次里的 config_revision / policy_count。
	State func() (revision int64, policyCount int)

	SkipValidation  bool // 默认 false（校验每个事件）
	OnError         func(error)
	OnDrop          func(n int, reason string)
	ShutdownTimeout time.Duration // 默认 5s
}

// Stats 是 Reporter 的运行统计快照。
type Stats struct {
	Enqueued    uint64
	Dropped     uint64
	Sent        uint64
	Accepted    uint64
	Rejected    uint64
	Retried     uint64
	Spooled     uint64
	Replayed    uint64
	QueueDepth  int
	SpoolFiles  int
	SpoolBytes  int64
	LastError   error
	LastSuccess time.Time
}

// Spool 是磁盘缓冲抽象；reporter/spool.Disk 是默认实现。
type Spool interface {
	Save(ctx context.Context, batch agent.EventBatch) error
	Replay(ctx context.Context, fn func(agent.EventBatch) error) error
	Stats() SpoolStats
	Trim(ctx context.Context) error
}

// SpoolStats 是 Spool 的统计。
type SpoolStats struct {
	Files  int
	Bytes  int64
	Events int
}

// Reporter 是事件上报器。
type Reporter struct {
	client *agent.Client
	opts   Options
	q      *eventQueue
	wake   chan struct{}
	spool  Spool

	mu      sync.Mutex
	stats   Stats
	backoff time.Duration
	running bool
}

// New 创建 Reporter。
func New(c *agent.Client, opts Options) (*Reporter, error) {
	if c == nil {
		return nil, errors.New("reporter: client 不能为 nil")
	}
	if opts.State == nil {
		return nil, errors.New("reporter: Options.State 必填")
	}
	opts, err := normalizeOptions(opts)
	if err != nil {
		return nil, err
	}
	return &Reporter{
		client: c,
		opts:   opts,
		q:      &eventQueue{},
		wake:   make(chan struct{}, 1),
		spool:  opts.Spool,
	}, nil
}

func normalizeOptions(o Options) (Options, error) {
	if o.BatchSize == 0 {
		o.BatchSize = defaultBatchSize
	}
	if o.BatchSize < 1 || o.BatchSize > agent.MaxBatchEvents {
		return o, fmt.Errorf("reporter: BatchSize 必须在 1~%d", agent.MaxBatchEvents)
	}
	if o.FlushInterval == 0 {
		o.FlushInterval = defaultFlushInterval
	}
	if o.FlushInterval < 0 {
		return o, errors.New("reporter: FlushInterval 不能为负")
	}
	if o.QueueSize == 0 {
		o.QueueSize = defaultQueueSize
	}
	if o.QueueSize < 1 {
		return o, errors.New("reporter: QueueSize 必须为正")
	}
	if o.Format == "" {
		o.Format = agent.FormatNDJSON
	}
	if o.Format != agent.FormatJSON && o.Format != agent.FormatNDJSON {
		return o, fmt.Errorf("reporter: 不支持的上报格式 %q", o.Format)
	}
	if o.Timeout == 0 {
		o.Timeout = defaultTimeout
	}
	if o.Timeout < 0 {
		return o, errors.New("reporter: Timeout 不能为负")
	}
	if o.MaxBackoff == 0 {
		o.MaxBackoff = defaultMaxBackoff
	}
	if o.MaxBackoff < 0 {
		return o, errors.New("reporter: MaxBackoff 不能为负")
	}
	if o.ShutdownTimeout == 0 {
		o.ShutdownTimeout = defaultShutdownTimeout
	}
	if o.ShutdownTimeout < 0 {
		return o, errors.New("reporter: ShutdownTimeout 不能为负")
	}
	return o, nil
}

// Enqueue 入队一个事件。永不阻塞；返回 false 表示该事件未入队。
// 队列满时丢弃最旧事件并计数（契约 §6.8）。
func (r *Reporter) Enqueue(ev agent.Event) bool {
	if !r.opts.SkipValidation {
		if err := ev.Validate(); err != nil {
			r.dropEvents(1, "invalid_event")
			r.reportError(err)
			return false
		}
	}

	dropped := r.q.push(ev, r.opts.QueueSize)
	r.mu.Lock()
	r.stats.Enqueued++
	if dropped > 0 {
		r.stats.Dropped += uint64(dropped)
	}
	r.mu.Unlock()
	if dropped > 0 {
		r.opts.drop(dropped, "queue_full")
	}

	select {
	case r.wake <- struct{}{}:
	default:
	}
	return true
}

// Run 阻塞运行，直到 ctx 取消；取消后按 ShutdownTimeout 做最后一次 flush。
func (r *Reporter) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("reporter: ctx 不能为 nil")
	}
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return errors.New("reporter: 已在运行")
	}
	r.running = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.running = false
		r.mu.Unlock()
	}()

	r.replaySpool(ctx)

	var wait time.Duration
	for {
		if wait > 0 {
			select {
			case <-ctx.Done():
				return r.shutdown()
			case <-time.After(wait):
			}
			wait = 0
		} else {
			select {
			case <-ctx.Done():
				return r.shutdown()
			case <-r.wake:
			case <-time.After(r.opts.FlushInterval):
			}
		}

		if err := r.drain(ctx); err != nil {
			wait = r.nextBackoff(err)
			continue
		}
		r.replaySpool(ctx)
	}
}

// Flush 立即把当前队列尽量发完；不做 spool 补发（补发由 Run 负责）。
func (r *Reporter) Flush(ctx context.Context) error {
	return r.drain(ctx)
}

// Stats 返回统计快照。
func (r *Reporter) Stats() Stats {
	r.mu.Lock()
	s := r.stats
	r.mu.Unlock()

	s.QueueDepth = r.q.len()
	if r.spool != nil {
		ss := r.spool.Stats()
		s.SpoolFiles = ss.Files
		s.SpoolBytes = ss.Bytes
	}
	return s
}

func (r *Reporter) shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), r.opts.ShutdownTimeout)
	defer cancel()

	err := r.drain(ctx)

	rest := r.q.takeAll()
	if len(rest) == 0 {
		return err
	}
	batch := r.batchOf(rest)
	if r.spool != nil {
		if serr := r.spool.Save(context.Background(), batch); serr == nil {
			r.mu.Lock()
			r.stats.Spooled += uint64(len(rest))
			r.mu.Unlock()
			return err
		} else {
			r.reportError(serr)
		}
	}
	r.dropEvents(len(rest), "shutdown")
	return err
}

func (r *Reporter) drain(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		batch, ok := r.nextBatch()
		if !ok {
			return nil
		}
		remaining, err := r.sendBatch(ctx, batch)
		if err == nil {
			continue
		}
		if r.handleFailure(ctx, batch, remaining, err) {
			return err
		}
		continue
	}
}

func (r *Reporter) nextBatch() (agent.EventBatch, bool) {
	events := r.q.pop(r.opts.BatchSize)
	if len(events) == 0 {
		return agent.EventBatch{}, false
	}
	return r.batchOf(events), true
}

func (r *Reporter) batchOf(events []agent.Event) agent.EventBatch {
	revision, policyCount := r.opts.State()
	return agent.EventBatch{
		ConfigRevision: revision,
		PolicyCount:    policyCount,
		ClientStats:    r.clientStats(),
		Events:         events,
	}
}

func (r *Reporter) clientStats() *agent.ClientStats {
	r.mu.Lock()
	dropped := r.stats.Dropped
	r.mu.Unlock()

	var spoolEvents int
	if r.spool != nil {
		spoolEvents = r.spool.Stats().Events
	}
	return &agent.ClientStats{
		QueueDepth:   r.q.len(),
		DroppedTotal: int64(dropped),
		SpoolSize:    spoolEvents,
	}
}

// sendBatch 发送一个批次；413 时递归拆半。
// 返回尚未成功发送的事件（用于失败后 spool/requeue）。
func (r *Reporter) sendBatch(ctx context.Context, batch agent.EventBatch) ([]agent.Event, error) {
	err := r.send(ctx, batch)
	if err == nil {
		return nil, nil
	}

	var apiErr *agent.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusRequestEntityTooLarge || len(batch.Events) <= 1 {
		return batch.Events, err
	}

	mid := len(batch.Events) / 2
	first := batch
	first.Events = batch.Events[:mid]
	second := batch
	second.Events = batch.Events[mid:]

	remaining, ferr := r.sendBatch(ctx, first)
	if ferr != nil {
		return append(append([]agent.Event{}, remaining...), second.Events...), ferr
	}
	return r.sendBatch(ctx, second)
}

func (r *Reporter) send(ctx context.Context, batch agent.EventBatch) error {
	sendCtx := ctx
	if r.opts.Timeout > 0 {
		var cancel context.CancelFunc
		sendCtx, cancel = context.WithTimeout(ctx, r.opts.Timeout)
		defer cancel()
	}

	res, err := r.client.ReportEvents(sendCtx, batch,
		agent.WithFormat(r.opts.Format),
		agent.WithGzip(!r.opts.DisableGzip),
	)
	if err != nil {
		return err
	}

	r.mu.Lock()
	r.stats.Sent++
	r.stats.Accepted += uint64(res.Accepted)
	r.stats.Rejected += uint64(res.Rejected)
	r.stats.LastSuccess = time.Now()
	r.stats.LastError = nil
	r.mu.Unlock()
	return nil
}

// handleFailure 处理发送失败；返回 true 表示需要退避后重试。
func (r *Reporter) handleFailure(ctx context.Context, batch agent.EventBatch, remaining []agent.Event, err error) bool {
	if len(remaining) == 0 {
		return false
	}

	var apiErr *agent.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
			r.dropEvents(len(remaining), "server_rejected")
			return false
		}
	}
	if errors.Is(err, agent.ErrInvalidEvent) {
		r.dropEvents(len(remaining), "invalid_event")
		return false
	}

	if r.spool != nil {
		spoolBatch := batch
		spoolBatch.Events = remaining
		if serr := r.spool.Save(ctx, spoolBatch); serr == nil {
			r.mu.Lock()
			r.stats.Spooled += uint64(len(remaining))
			r.mu.Unlock()
			r.reportError(err)
			// 已安全落盘，但仍需退避：避免停机期间空转刷完整个队列。
			return true
		} else {
			r.reportError(serr)
		}
	}

	dropped := r.q.requeueFront(remaining, r.opts.QueueSize)
	r.mu.Lock()
	r.stats.Retried++
	r.stats.LastError = err
	if dropped > 0 {
		r.stats.Dropped += uint64(dropped)
	}
	r.mu.Unlock()
	if dropped > 0 {
		r.opts.drop(dropped, "queue_full")
	}
	r.reportError(err)
	return true
}

func (r *Reporter) replaySpool(ctx context.Context) {
	if r.spool == nil {
		return
	}
	err := r.spool.Replay(ctx, func(batch agent.EventBatch) error {
		remaining, err := r.sendBatch(ctx, batch)
		if err != nil {
			return err
		}
		if len(remaining) > 0 {
			return errors.New("reporter: spool 补发仍有未发送事件")
		}
		r.mu.Lock()
		r.stats.Replayed += uint64(len(batch.Events))
		r.mu.Unlock()
		return nil
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		r.reportError(err)
	}
}

func (r *Reporter) nextBackoff(err error) time.Duration {
	if d, ok := agent.RetryAfter(err); ok {
		if d > r.opts.MaxBackoff {
			d = r.opts.MaxBackoff
		}
		return d
	}

	r.mu.Lock()
	if r.backoff < baseBackoff {
		r.backoff = baseBackoff
	} else {
		r.backoff *= 2
		if r.backoff > r.opts.MaxBackoff {
			r.backoff = r.opts.MaxBackoff
		}
	}
	d := r.backoff
	r.mu.Unlock()

	jitter := 0.8 + 0.4*rand.Float64()
	return time.Duration(float64(d) * jitter)
}

func (r *Reporter) dropEvents(n int, reason string) {
	if n <= 0 {
		return
	}
	r.mu.Lock()
	r.stats.Dropped += uint64(n)
	r.mu.Unlock()
	r.opts.drop(n, reason)
}

func (r *Reporter) reportError(err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	r.stats.LastError = err
	r.mu.Unlock()
	if r.opts.OnError != nil {
		r.opts.OnError(err)
	}
}

func (o Options) drop(n int, reason string) {
	if o.OnDrop != nil {
		o.OnDrop(n, reason)
	}
}

// eventQueue 是带丢弃策略的有界 FIFO。
type eventQueue struct {
	mu    sync.Mutex
	items []agent.Event
	head  int
}

func (q *eventQueue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items) - q.head
}

func (q *eventQueue) push(ev agent.Event, max int) (dropped int) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.items)-q.head >= max {
		q.head++
		dropped = 1
	}
	q.items = append(q.items, ev)
	q.compactLocked()
	return dropped
}

func (q *eventQueue) pop(n int) []agent.Event {
	q.mu.Lock()
	defer q.mu.Unlock()

	avail := len(q.items) - q.head
	if n > avail {
		n = avail
	}
	if n <= 0 {
		return nil
	}
	out := make([]agent.Event, n)
	copy(out, q.items[q.head:q.head+n])
	q.head += n
	q.compactLocked()
	return out
}

func (q *eventQueue) requeueFront(evs []agent.Event, max int) (dropped int) {
	if len(evs) == 0 {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	merged := make([]agent.Event, 0, len(evs)+len(q.items)-q.head)
	merged = append(merged, evs...)
	merged = append(merged, q.items[q.head:]...)
	if len(merged) > max {
		dropped = len(merged) - max
		merged = merged[dropped:]
	}
	q.items = merged
	q.head = 0
	return dropped
}

func (q *eventQueue) takeAll() []agent.Event {
	q.mu.Lock()
	defer q.mu.Unlock()

	out := append([]agent.Event{}, q.items[q.head:]...)
	q.items = nil
	q.head = 0
	return out
}

func (q *eventQueue) compactLocked() {
	if q.head > 1024 && q.head*2 >= len(q.items) {
		q.items = append([]agent.Event{}, q.items[q.head:]...)
		q.head = 0
	}
}
