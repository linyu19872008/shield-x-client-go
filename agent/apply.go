package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ApplyStatusRequest 是一次应用回执。
type ApplyStatusRequest struct {
	GatewayID       string    `json:"gateway_id"`
	AgentVersion    string    `json:"agent_version,omitempty"`
	AppliedRevision int64     `json:"applied_revision"`
	Status          string    `json:"status"` // applied | failed
	PolicyCount     int       `json:"policy_count,omitempty"`
	TemplateCount   int       `json:"template_count,omitempty"`
	CapsHash        string    `json:"caps_hash,omitempty"`
	AppliedAt       time.Time `json:"applied_at,omitempty"`
	DurationMS      int       `json:"duration_ms,omitempty"`
	ErrorCode       string    `json:"error_code,omitempty"`
	ErrorDetail     string    `json:"error_detail,omitempty"`
}

// ReportApplyStatus 上报 ACK/NACK。网关应在成功应用快照后调用；
// 应用失败时带上 error_code/error_detail，控制台会保留失败状态直到下一次成功 ACK。
func (c *Client) ReportApplyStatus(ctx context.Context, req ApplyStatusRequest) error {
	if ctx == nil {
		return errors.New("agent: ctx 不能为 nil")
	}
	if req.GatewayID == "" {
		req.GatewayID = c.cfg.GatewayID
	}
	if req.AgentVersion == "" {
		req.AgentVersion = c.cfg.AgentVersion
	}
	if req.CapsHash == "" {
		req.CapsHash = c.capsHash
	}
	if req.AppliedRevision <= 0 {
		return fmt.Errorf("%w: applied_revision 必须大于 0", ErrInvalidConfig)
	}
	if req.Status != "applied" && req.Status != "failed" {
		return fmt.Errorf("%w: status 只能是 applied 或 failed", ErrInvalidConfig)
	}
	if req.Status == "failed" && req.ErrorCode == "" {
		req.ErrorCode = "UNKNOWN"
	}
	if req.AppliedAt.IsZero() {
		req.AppliedAt = time.Now().UTC()
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("编码应用回执失败: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.ReportTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("gateways/v1/apply-status"), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("构造应用回执请求失败: %w", err)
	}
	c.setCommonHeaders(httpReq)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiErrorFromResponse(resp)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return nil
}
