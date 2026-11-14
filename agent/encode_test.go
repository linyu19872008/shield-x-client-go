package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestEncodeBatchNDJSON(t *testing.T) {
	batch := EventBatch{
		ConfigRevision: 1,
		PolicyCount:    1,
		Events:         []Event{validEvent("e-1")},
	}
	body, contentType, contentEncoding, err := encodeBatch(batch, batchMeta{
		AgentVersion: "gateway/1.0.0",
		GatewayID:    "gw-1",
	}, reportOptions{format: FormatNDJSON})
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	if contentType != "application/x-ndjson" {
		t.Fatalf("Content-Type = %q", contentType)
	}
	if contentEncoding != "" {
		t.Fatalf("未开启 gzip 时 Content-Encoding 应为空，实际 %q", contentEncoding)
	}

	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 2 {
		t.Fatalf("行数 = %d，期望 2", len(lines))
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &meta); err != nil {
		t.Fatalf("解析元信息失败: %v", err)
	}
	if meta["agent_version"] != "gateway/1.0.0" || meta["gateway_id"] != "gw-1" {
		t.Fatalf("元信息不符: %v", meta)
	}
}

func TestEncodeBatchJSONEmptyEvents(t *testing.T) {
	body, contentType, _, err := encodeBatch(EventBatch{}, batchMeta{AgentVersion: "v1", GatewayID: "gw"}, reportOptions{format: FormatJSON})
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	if contentType != "application/json" {
		t.Fatalf("Content-Type = %q", contentType)
	}
	if !strings.Contains(string(body), `"events":[]`) {
		t.Fatalf("空批次应输出空数组: %s", body)
	}
}

func TestEncodeBatchUnknownFormat(t *testing.T) {
	_, _, _, err := encodeBatch(EventBatch{}, batchMeta{}, reportOptions{format: Format("xml")})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("错误 = %v，期望 ErrInvalidConfig", err)
	}
}

func TestEncodeBatchTooLarge(t *testing.T) {
	events := make([]Event, 600)
	for i := range events {
		ev := validEvent(fmt.Sprintf("e-%d", i))
		ev.Query = strings.Repeat("x", 4096)
		events[i] = ev
	}
	_, _, _, err := encodeBatch(EventBatch{Events: events}, batchMeta{AgentVersion: "v1", GatewayID: "gw"}, reportOptions{format: FormatNDJSON})
	if !errors.Is(err, ErrBatchTooLarge) {
		t.Fatalf("错误 = %v，期望 ErrBatchTooLarge", err)
	}
}
