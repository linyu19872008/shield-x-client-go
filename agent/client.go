package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client 是 shield-x agent API 客户端，可并发使用。
type Client struct {
	cfg                Config
	baseURL            *url.URL
	httpClient         *http.Client
	ownsTransport      bool
	capabilitiesHeader string
	capsHash           string
	signingKey         ed25519.PrivateKey
}

// New 创建客户端并校验配置。
func New(cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()

	base, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("%w: BaseURL 解析失败: %v", ErrInvalidConfig, err)
	}

	c := &Client{cfg: cfg, baseURL: base}
	switch {
	case cfg.HTTPClient != nil:
		c.httpClient = cfg.HTTPClient
	case cfg.Transport != nil:
		c.httpClient = &http.Client{Transport: cfg.Transport}
		c.ownsTransport = true
	default:
		c.httpClient = &http.Client{Transport: defaultTransport()}
		c.ownsTransport = true
	}

	if cfg.Capabilities != nil {
		header, err := cfg.Capabilities.headerJSON()
		if err != nil {
			return nil, fmt.Errorf("%w: capabilities 编码失败: %v", ErrInvalidConfig, err)
		}
		c.capabilitiesHeader = header
		sum := sha256.Sum256([]byte(header))
		c.capsHash = hex.EncodeToString(sum[:])
	}
	if cfg.SigningKey != "" {
		seed, _ := hex.DecodeString(cfg.SigningKey)
		c.signingKey = ed25519.NewKeyFromSeed(seed)
	}
	return c, nil
}

// Close 只关闭 SDK 自己创建的连接；调用方提供的 HTTPClient 不会被关闭。
func (c *Client) Close() error {
	if c.ownsTransport {
		c.httpClient.CloseIdleConnections()
	}
	return nil
}

// FetchPolicies 拉取策略快照。sinceRevision>0 时发送 If-None-Match。
// 304 不是错误：返回 NotModified=true 且 Snapshot=nil。
func (c *Client) FetchPolicies(ctx context.Context, sinceRevision int64) (PoliciesResult, error) {
	if ctx == nil {
		return PoliciesResult{}, errors.New("agent: ctx 不能为 nil")
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.FetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint("policies/v1"), nil)
	if err != nil {
		return PoliciesResult{}, fmt.Errorf("构造策略请求失败: %w", err)
	}
	c.setCommonHeaders(req)
	req.Header.Set("X-Shieldx-Gateway-Id", c.cfg.GatewayID)
	if c.capabilitiesHeader != "" {
		req.Header.Set("X-Shieldx-Capabilities", c.capabilitiesHeader)
	}
	if sinceRevision >= 0 {
		req.Header.Set("If-None-Match", fmt.Sprintf(`"rev-%d"`, sinceRevision))
		req.Header.Set("X-Shieldx-Applied-Revision", strconv.FormatInt(sinceRevision, 10))
	}
	if c.cfg.Capabilities != nil && c.cfg.Capabilities.DeltaSnapshot && sinceRevision > 0 {
		q := req.URL.Query()
		q.Set("since_revision", strconv.FormatInt(sinceRevision, 10))
		req.URL.RawQuery = q.Encode()
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return PoliciesResult{}, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		rev := revisionFromETag(resp.Header.Get("ETag"))
		if rev <= 0 {
			rev = sinceRevision
		}
		return PoliciesResult{Revision: rev, NotModified: true}, nil
	case http.StatusOK:
		var snap PolicySnapshot
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&snap); err != nil {
			return PoliciesResult{}, fmt.Errorf("解码策略快照失败: %w", err)
		}
		if snap.Policies == nil {
			snap.Policies = []Policy{}
		}
		return PoliciesResult{Revision: snap.Revision, Snapshot: &snap}, nil
	default:
		return PoliciesResult{}, apiErrorFromResponse(resp)
	}
}

// FetchTemplates 拉取模板快照。
func (c *Client) FetchTemplates(ctx context.Context, sinceRevision int64) (TemplatesResult, error) {
	if ctx == nil {
		return TemplatesResult{}, errors.New("agent: ctx 不能为 nil")
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.FetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint("templates/v1"), nil)
	if err != nil {
		return TemplatesResult{}, fmt.Errorf("构造模板请求失败: %w", err)
	}
	c.setCommonHeaders(req)
	req.Header.Set("X-Shieldx-Gateway-Id", c.cfg.GatewayID)
	if sinceRevision >= 0 {
		req.Header.Set("If-None-Match", fmt.Sprintf(`"rev-%d"`, sinceRevision))
		req.Header.Set("X-Shieldx-Applied-Revision", strconv.FormatInt(sinceRevision, 10))
	}
	if c.cfg.Capabilities != nil && c.cfg.Capabilities.DeltaSnapshot && sinceRevision > 0 {
		q := req.URL.Query()
		q.Set("since_revision", strconv.FormatInt(sinceRevision, 10))
		req.URL.RawQuery = q.Encode()
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return TemplatesResult{}, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		rev := revisionFromETag(resp.Header.Get("ETag"))
		if rev <= 0 {
			rev = sinceRevision
		}
		return TemplatesResult{Revision: rev, NotModified: true}, nil
	case http.StatusOK:
		var snap TemplateSnapshot
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&snap); err != nil {
			return TemplatesResult{}, fmt.Errorf("解码模板快照失败: %w", err)
		}
		if snap.Templates == nil {
			snap.Templates = []Template{}
		}
		return TemplatesResult{Revision: snap.Revision, Snapshot: &snap}, nil
	default:
		return TemplatesResult{}, apiErrorFromResponse(resp)
	}
}

// ReportEvents 上报一批事件。低层调用：一次请求，不重试、不拆批。
func (c *Client) ReportEvents(ctx context.Context, batch EventBatch, opts ...ReportOption) (ReportResult, error) {
	if ctx == nil {
		return ReportResult{}, errors.New("agent: ctx 不能为 nil")
	}
	if len(batch.Events) > MaxBatchEvents {
		return ReportResult{}, ErrBatchTooLarge
	}

	options := defaultReportOptions()
	for _, opt := range opts {
		opt(&options)
	}
	if !options.skipValidation {
		for i := range batch.Events {
			if err := batch.Events[i].validate(i); err != nil {
				return ReportResult{}, fmt.Errorf("%w: %w", ErrInvalidEvent, err)
			}
		}
	}

	body, contentType, contentEncoding, err := encodeBatch(batch, batchMeta{
		AgentVersion: c.cfg.AgentVersion,
		GatewayID:    c.cfg.GatewayID,
	}, options)
	if err != nil {
		return ReportResult{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, c.cfg.ReportTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("events/v1"), bytes.NewReader(body))
	if err != nil {
		return ReportResult{}, fmt.Errorf("构造上报请求失败: %w", err)
	}
	c.setCommonHeaders(req)
	req.Header.Set("X-Shieldx-Gateway-Id", c.cfg.GatewayID)
	if c.signingKey != nil {
		ts := time.Now().Unix()
		sum := sha256.Sum256(body)
		msg := fmt.Sprintf("%d\n%s\n%s\n%s", ts, req.Method, req.URL.RequestURI(), hex.EncodeToString(sum[:]))
		sig := ed25519.Sign(c.signingKey, []byte(msg))
		req.Header.Set("X-Shieldx-Signature", fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(sig)))
	}
	req.Header.Set("Content-Type", contentType)
	if contentEncoding != "" {
		req.Header.Set("Content-Encoding", contentEncoding)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ReportResult{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		return ReportResult{}, apiErrorFromResponse(resp)
	}

	var wire struct {
		Accepted   int    `json:"accepted"`
		Rejected   int    `json:"rejected"`
		ServerTime string `json:"server_time"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&wire); err != nil {
		return ReportResult{}, fmt.Errorf("解码上报响应失败: %w", err)
	}
	result := ReportResult{Accepted: wire.Accepted, Rejected: wire.Rejected}
	if wire.ServerTime != "" {
		if t, err := time.Parse(time.RFC3339, wire.ServerTime); err == nil {
			result.ServerTime = t
		}
	}
	return result, nil
}

func (c *Client) endpoint(path string) string {
	u := *c.baseURL
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(path, "/")
	return u.String()
}

func (c *Client) setCommonHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	req.Header.Set("X-Shieldx-Agent-Version", c.cfg.AgentVersion)
	if c.capsHash != "" {
		req.Header.Set("X-Shieldx-Caps-Hash", c.capsHash)
	}
}

func revisionFromETag(etag string) int64 {
	etag = strings.Trim(strings.TrimSpace(etag), `"`)
	if !strings.HasPrefix(etag, "rev-") {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(etag, "rev-"), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func defaultTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 100
	t.MaxIdleConnsPerHost = 8
	t.IdleConnTimeout = 90 * time.Second
	return t
}
