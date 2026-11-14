package spool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/linyu19872008/shield-x-client-go/agent"
)

func testBatch(n int) agent.EventBatch {
	events := make([]agent.Event, n)
	for i := range events {
		events[i] = agent.Event{EventID: fmt.Sprintf("e-%d", i)}
	}
	return agent.EventBatch{ConfigRevision: 1, PolicyCount: 1, Events: events}
}

func TestSaveReplayStats(t *testing.T) {
	dir := t.TempDir()
	d, err := NewDisk(dir)
	if err != nil {
		t.Fatalf("NewDisk 失败: %v", err)
	}

	if err := d.Save(context.Background(), testBatch(2)); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}
	st := d.Stats()
	if st.Files != 1 || st.Events != 2 || st.Bytes == 0 {
		t.Fatalf("统计不符: %+v", st)
	}

	var replayed []agent.EventBatch
	if err := d.Replay(context.Background(), func(b agent.EventBatch) error {
		replayed = append(replayed, b)
		return nil
	}); err != nil {
		t.Fatalf("Replay 失败: %v", err)
	}
	if len(replayed) != 1 || len(replayed[0].Events) != 2 {
		t.Fatalf("补发批次不符: %+v", replayed)
	}
	if st := d.Stats(); st.Files != 0 || st.Events != 0 {
		t.Fatalf("补发后应清空: %+v", st)
	}
}

func TestReplayKeepsFileOnFailure(t *testing.T) {
	d, err := NewDisk(t.TempDir())
	if err != nil {
		t.Fatalf("NewDisk 失败: %v", err)
	}
	if err := d.Save(context.Background(), testBatch(1)); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}

	wantErr := errors.New("send failed")
	if err := d.Replay(context.Background(), func(agent.EventBatch) error { return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("Replay 错误 = %v，期望 %v", err, wantErr)
	}
	if st := d.Stats(); st.Files != 1 {
		t.Fatalf("失败后文件应保留: %+v", st)
	}
}

func TestReplayRemovesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	d, err := NewDisk(dir)
	if err != nil {
		t.Fatalf("NewDisk 失败: %v", err)
	}

	name := "v1-0000000000000000001-000001-1.json.gz"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("not gzip"), 0o600); err != nil {
		t.Fatalf("写入损坏文件失败: %v", err)
	}

	if err := d.Replay(context.Background(), func(agent.EventBatch) error {
		t.Fatal("损坏文件不应回调")
		return nil
	}); err != nil {
		t.Fatalf("Replay 失败: %v", err)
	}
	if st := d.Stats(); st.Files != 0 {
		t.Fatalf("损坏文件应被删除: %+v", st)
	}
}

func TestTrimByMaxFiles(t *testing.T) {
	d, err := NewDisk(t.TempDir(), WithMaxFiles(2))
	if err != nil {
		t.Fatalf("NewDisk 失败: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := d.Save(context.Background(), testBatch(1)); err != nil {
			t.Fatalf("Save 失败: %v", err)
		}
	}
	if st := d.Stats(); st.Files != 2 {
		t.Fatalf("文件数 = %d，期望 2", st.Files)
	}
}

func TestTrimByMaxBytesKeepsNewest(t *testing.T) {
	d, err := NewDisk(t.TempDir(), WithMaxBytes(1))
	if err != nil {
		t.Fatalf("NewDisk 失败: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := d.Save(context.Background(), testBatch(1)); err != nil {
			t.Fatalf("Save 失败: %v", err)
		}
	}
	if st := d.Stats(); st.Files != 1 {
		t.Fatalf("文件数 = %d，期望至少保留最新 1 个", st.Files)
	}
}

func TestNewDiskValidation(t *testing.T) {
	if _, err := NewDisk(""); err == nil {
		t.Fatal("空目录应报错")
	}
	if _, err := NewDisk(t.TempDir(), WithMaxBytes(0)); err == nil {
		t.Fatal("MaxBytes=0 应报错")
	}
	if _, err := NewDisk(t.TempDir(), WithMaxFiles(0)); err == nil {
		t.Fatal("MaxFiles=0 应报错")
	}
}
