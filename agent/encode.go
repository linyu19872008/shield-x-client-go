package agent

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
)

// Format 是事件上报的请求体格式。
type Format string

const (
	FormatJSON   Format = "json"
	FormatNDJSON Format = "ndjson"
)

const (
	// MaxBatchEvents 是单批事件条数上限（契约 §6.3）。
	MaxBatchEvents = 1000
	// MaxBatchBytes 是单批压缩前字节上限（契约 §6.3）。
	MaxBatchBytes = 1 << 20
)

type reportOptions struct {
	format         Format
	gzip           bool
	skipValidation bool
}

func defaultReportOptions() reportOptions {
	return reportOptions{format: FormatNDJSON, gzip: true}
}

// ReportOption 定制单次上报行为。
type ReportOption func(*reportOptions)

// WithFormat 设置请求体格式，默认 FormatNDJSON。
func WithFormat(f Format) ReportOption {
	return func(o *reportOptions) { o.format = f }
}

// WithGzip 设置是否压缩请求体，默认 true。
func WithGzip(v bool) ReportOption {
	return func(o *reportOptions) { o.gzip = v }
}

// WithSkipValidation 跳过单事件校验（仅建议在调用方已完成校验时使用）。
func WithSkipValidation() ReportOption {
	return func(o *reportOptions) { o.skipValidation = true }
}

type batchMeta struct {
	AgentVersion string
	GatewayID    string
}

type batchMetaWire struct {
	AgentVersion   string       `json:"agent_version"`
	GatewayID      string       `json:"gateway_id"`
	ConfigRevision int64        `json:"config_revision"`
	PolicyCount    int          `json:"policy_count"`
	ClientStats    *ClientStats `json:"client_stats,omitempty"`
}

type batchWire struct {
	batchMetaWire
	Events []Event `json:"events"`
}

// encodeBatch 生成请求体；返回 body、Content-Type、Content-Encoding。
// 字节上限按压缩前计算。
func encodeBatch(batch EventBatch, meta batchMeta, opts reportOptions) ([]byte, string, string, error) {
	head := batchMetaWire{
		AgentVersion:   meta.AgentVersion,
		GatewayID:      meta.GatewayID,
		ConfigRevision: batch.ConfigRevision,
		PolicyCount:    batch.PolicyCount,
		ClientStats:    batch.ClientStats,
	}

	var (
		raw         []byte
		contentType string
		err         error
	)
	switch opts.format {
	case FormatJSON:
		events := batch.Events
		if events == nil {
			events = []Event{}
		}
		raw, err = json.Marshal(batchWire{batchMetaWire: head, Events: events})
		contentType = "application/json"
	case FormatNDJSON:
		var buf bytes.Buffer
		var line []byte
		line, err = json.Marshal(head)
		if err == nil {
			buf.Write(line)
			buf.WriteByte('\n')
			for i := range batch.Events {
				line, err = json.Marshal(batch.Events[i])
				if err != nil {
					break
				}
				buf.Write(line)
				buf.WriteByte('\n')
			}
		}
		raw = buf.Bytes()
		contentType = "application/x-ndjson"
	default:
		return nil, "", "", fmt.Errorf("%w: 未知上报格式 %q", ErrInvalidConfig, opts.format)
	}
	if err != nil {
		return nil, "", "", fmt.Errorf("编码事件失败: %w", err)
	}
	if len(raw) > MaxBatchBytes {
		return nil, "", "", ErrBatchTooLarge
	}
	if !opts.gzip {
		return raw, contentType, "", nil
	}

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(raw); err != nil {
		return nil, "", "", fmt.Errorf("压缩事件失败: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, "", "", fmt.Errorf("压缩事件失败: %w", err)
	}
	return gz.Bytes(), contentType, "gzip", nil
}
