package agent

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	defaultFetchTimeout  = 3 * time.Second
	defaultReportTimeout = 10 * time.Second
)

var gatewayIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// Config 是客户端配置。
type Config struct {
	// BaseURL 必填，且必须包含路径前缀：SDK 在其后拼 /policies/v1 等。
	// 例：http://shield-x.internal:8080/shieldx
	BaseURL string

	// Token 是站点级 Bearer Token，形如 sx_xxx。
	Token string

	// GatewayID 为 1~64 字符，[A-Za-z0-9._-]，站点内唯一且稳定。
	GatewayID string

	// GatewayGroup 是站点内的网关集群标签（可选，≤64 字符）。
	// 写入 X-Shieldx-Gateway-Group，用于按集群定向策略与分组展示。
	GatewayGroup string

	// AgentVersion 是 gateway 自身版本，写入 X-Shieldx-Agent-Version 与批次 agent_version。
	// 例：gateway/1.4.2。必填——身份不能靠猜。
	AgentVersion string

	// Capabilities 为 nil 时表示不声明能力，服务端按最小能力集处理。
	Capabilities *Capabilities

	// HTTPClient 与 Transport 互斥。
	HTTPClient *http.Client      // 调用方完全拥有；SDK 不设 Timeout、不 Close
	Transport  http.RoundTripper // SDK 用它构造 client，并负责关闭空闲连接

	// FetchTimeout / ReportTimeout 默认 3s / 10s；ctx 截止时间更早时以 ctx 为准。
	FetchTimeout  time.Duration
	ReportTimeout time.Duration

	// UserAgent 默认 shield-x-client-go/<version>。
	UserAgent string

	// SigningKey 是可选的 Ed25519 私钥 seed（64 位 hex）。
	// 由 shield-x 的 /gateways/v1/register 一次性下发；设置后事件上报会自动签名。
	SigningKey string
}

// Validate 校验配置。
func (c Config) Validate() error {
	if strings.TrimSpace(c.BaseURL) == "" {
		return fmt.Errorf("%w: BaseURL 必填", ErrInvalidConfig)
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || !u.IsAbs() || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("%w: BaseURL 必须是绝对 http(s) 地址", ErrInvalidConfig)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%w: BaseURL 不能包含 query 或 fragment", ErrInvalidConfig)
	}
	if c.Token == "" {
		return fmt.Errorf("%w: Token 必填", ErrInvalidConfig)
	}
	if !gatewayIDPattern.MatchString(c.GatewayID) {
		return fmt.Errorf("%w: GatewayID 必须是 1~64 个 [A-Za-z0-9._-] 字符", ErrInvalidConfig)
	}
	if len(c.GatewayGroup) > 64 {
		return fmt.Errorf("%w: GatewayGroup 不能超过 64 字节", ErrInvalidConfig)
	}
	for _, r := range c.GatewayGroup {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: GatewayGroup 不能包含控制字符", ErrInvalidConfig)
		}
	}
	if strings.TrimSpace(c.AgentVersion) == "" {
		return fmt.Errorf("%w: AgentVersion 必填", ErrInvalidConfig)
	}
	if c.HTTPClient != nil && c.Transport != nil {
		return fmt.Errorf("%w: HTTPClient 与 Transport 只能设置一个", ErrInvalidConfig)
	}
	if c.FetchTimeout < 0 || c.ReportTimeout < 0 {
		return fmt.Errorf("%w: 超时不能为负", ErrInvalidConfig)
	}
	if c.Capabilities != nil {
		if err := c.Capabilities.Validate(); err != nil {
			return err
		}
	}
	if c.SigningKey != "" {
		b, err := hex.DecodeString(c.SigningKey)
		if err != nil || len(b) != ed25519.SeedSize {
			return fmt.Errorf("%w: SigningKey 必须是 %d 字节 Ed25519 seed 的 hex", ErrInvalidConfig, ed25519.SeedSize)
		}
	}
	return nil
}

func (c Config) withDefaults() Config {
	if c.FetchTimeout == 0 {
		c.FetchTimeout = defaultFetchTimeout
	}
	if c.ReportTimeout == 0 {
		c.ReportTimeout = defaultReportTimeout
	}
	if c.UserAgent == "" {
		c.UserAgent = "shield-x-client-go/" + Version()
	}
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	return c
}
