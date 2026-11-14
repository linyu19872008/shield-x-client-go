// Package spool 提供 reporter 的磁盘缓冲实现。
//
// 每个文件保存一个完整批次（meta + events），文件名包含事件数与时间戳，
// 便于按序补发和快速统计；写入使用临时文件 + rename 保证原子性。
package spool

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/linyu19872008/shield-x-client-go/agent"
	"github.com/linyu19872008/shield-x-client-go/reporter"
)

const (
	formatVersion = 1
	filePrefix    = "v1"
	fileSuffix    = ".json.gz"
)

// Disk 是 reporter.Spool 的磁盘实现。
type Disk struct {
	dir      string
	maxBytes int64
	maxFiles int

	seqMu sync.Mutex
	seq   uint64
}

// Option 定制 Disk。
type Option func(*Disk)

// WithMaxBytes 设置目录总字节上限（默认 200 MiB）。
func WithMaxBytes(n int64) Option { return func(d *Disk) { d.maxBytes = n } }

// WithMaxFiles 设置文件数上限（默认 10000）。
func WithMaxFiles(n int) Option { return func(d *Disk) { d.maxFiles = n } }

// NewDisk 创建磁盘缓冲目录。
func NewDisk(dir string, opts ...Option) (*Disk, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("spool: dir 不能为空")
	}
	d := &Disk{dir: dir, maxBytes: 200 << 20, maxFiles: 10000}
	for _, opt := range opts {
		opt(d)
	}
	if d.maxBytes <= 0 {
		return nil, errors.New("spool: MaxBytes 必须为正")
	}
	if d.maxFiles <= 0 {
		return nil, errors.New("spool: MaxFiles 必须为正")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("spool: 创建目录失败: %w", err)
	}
	return d, nil
}

type record struct {
	Version int              `json:"v"`
	SavedAt time.Time        `json:"saved_at"`
	Batch   agent.EventBatch `json:"batch"`
}

// Save 原子写入一个批次。
func (d *Disk) Save(ctx context.Context, batch agent.EventBatch) error {
	if len(batch.Events) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	d.seqMu.Lock()
	d.seq++
	seq := d.seq
	d.seqMu.Unlock()

	name := fmt.Sprintf("%s-%019d-%06d-%d%s",
		filePrefix, time.Now().UnixNano(), seq, len(batch.Events), fileSuffix)
	path := filepath.Join(d.dir, name)
	tmp := path + ".tmp"

	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("spool: 创建文件失败: %w", err)
	}

	writeErr := func() error {
		zw := gzip.NewWriter(f)
		enc := json.NewEncoder(zw)
		if err := enc.Encode(record{Version: formatVersion, SavedAt: time.Now().UTC(), Batch: batch}); err != nil {
			_ = zw.Close()
			return err
		}
		return zw.Close()
	}()
	if writeErr != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("spool: 写入失败: %w", writeErr)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("spool: 关闭文件失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("spool: 落盘失败: %w", err)
	}
	// Trim 失败不应让已落盘的文件被视为失败，否则 reporter 会重复入队。
	_ = d.Trim(context.Background())
	return nil
}

// Replay 按时间顺序回调每个批次；回调成功即删除文件，失败则保留并停止。
func (d *Disk) Replay(ctx context.Context, fn func(agent.EventBatch) error) error {
	files, err := d.list()
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		batch, err := readBatch(f.path)
		if err != nil {
			// 损坏文件直接删除，避免永久阻塞补发。
			_ = os.Remove(f.path)
			continue
		}
		if err := fn(batch); err != nil {
			return err
		}
		if err := os.Remove(f.path); err != nil {
			return fmt.Errorf("spool: 删除已补发文件失败: %w", err)
		}
	}
	return nil
}

// Stats 返回文件数、字节数与待补发事件数。
func (d *Disk) Stats() reporter.SpoolStats {
	files, err := d.list()
	if err != nil {
		return reporter.SpoolStats{}
	}
	var s reporter.SpoolStats
	s.Files = len(files)
	for _, f := range files {
		s.Bytes += f.size
		s.Events += f.events
	}
	return s
}

// Trim 按最旧优先删除，直到满足文件数与字节数上限；至少保留最新一个文件。
func (d *Disk) Trim(ctx context.Context) error {
	files, err := d.list()
	if err != nil {
		return err
	}
	var total int64
	for _, f := range files {
		total += f.size
	}
	for len(files) > 1 && (len(files) > d.maxFiles || total > d.maxBytes) {
		if err := ctx.Err(); err != nil {
			return err
		}
		oldest := files[0]
		if err := os.Remove(oldest.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("spool: 删除最旧文件失败: %w", err)
		}
		total -= oldest.size
		files = files[1:]
	}
	return nil
}

type fileInfo struct {
	path   string
	name   string
	size   int64
	events int
}

func (d *Disk) list() ([]fileInfo, error) {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return nil, fmt.Errorf("spool: 读取目录失败: %w", err)
	}
	out := make([]fileInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, filePrefix+"-") || !strings.HasSuffix(name, fileSuffix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, fileInfo{
			path:   filepath.Join(d.dir, name),
			name:   name,
			size:   info.Size(),
			events: parseEvents(name),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

func parseEvents(name string) int {
	base := strings.TrimSuffix(name, fileSuffix)
	parts := strings.Split(base, "-")
	if len(parts) < 4 {
		return 0
	}
	n, _ := strconv.Atoi(parts[len(parts)-1])
	return n
}

func readBatch(path string) (agent.EventBatch, error) {
	f, err := os.Open(path)
	if err != nil {
		return agent.EventBatch{}, err
	}
	defer f.Close()

	zr, err := gzip.NewReader(f)
	if err != nil {
		return agent.EventBatch{}, err
	}
	defer zr.Close()

	var rec record
	if err := json.NewDecoder(zr).Decode(&rec); err != nil {
		return agent.EventBatch{}, err
	}
	if rec.Version != formatVersion {
		return agent.EventBatch{}, fmt.Errorf("spool: 不支持的文件版本 %d", rec.Version)
	}
	return rec.Batch, nil
}
