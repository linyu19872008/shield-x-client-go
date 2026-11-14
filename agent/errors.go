package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrInvalidConfig 表示客户端配置非法。
	ErrInvalidConfig = errors.New("shield-x-client-go: 配置非法")
	// ErrInvalidEvent 表示事件字段非法。
	ErrInvalidEvent = errors.New("shield-x-client-go: 事件字段非法")
	// ErrBatchTooLarge 表示批次超过 1000 条或 1 MiB。
	ErrBatchTooLarge = errors.New("shield-x-client-go: 批次超过 1000 条或 1 MiB")
)

// APIError 是 shield-x 返回的结构化错误。
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	RequestID  string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "shield-x: HTTP %d", e.StatusCode)
	if e.Code != "" {
		fmt.Fprintf(&b, " %s", e.Code)
	}
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	if e.RequestID != "" {
		fmt.Fprintf(&b, " (request_id=%s)", e.RequestID)
	}
	return b.String()
}

// Retryable 表示该错误是否值得重试（429 / 5xx）。
func (e *APIError) Retryable() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500
}

// IsRetryable 判断错误是否可重试，覆盖 APIError、超时与网络错误。
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Retryable()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return false
}

// IsAuthError 判断是否为认证/授权错误（401 / 403）。
func IsAuthError(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden
}

// RetryAfter 返回服务端建议的退避时长。
func RetryAfter(err error) (time.Duration, bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
		return apiErr.RetryAfter, true
	}
	return 0, false
}

// ValidationError 描述单个事件或配置字段的校验失败。
type ValidationError struct {
	EventIndex int // -1 表示不是批次内事件
	Field      string
	Err        error
}

func (e *ValidationError) Error() string {
	if e.EventIndex >= 0 {
		return fmt.Sprintf("事件 #%d 字段 %s 非法: %v", e.EventIndex, e.Field, e.Err)
	}
	return fmt.Sprintf("字段 %s 非法: %v", e.Field, e.Err)
}

func (e *ValidationError) Unwrap() error { return e.Err }

func apiErrorFromResponse(resp *http.Response) *APIError {
	apiErr := &APIError{
		StatusCode: resp.StatusCode,
		RequestID:  resp.Header.Get("X-Request-Id"),
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if len(body) > 0 {
		var env struct {
			Error struct {
				Code      string `json:"code"`
				Message   string `json:"message"`
				RequestID string `json:"request_id"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &env) == nil && env.Error.Code != "" {
			apiErr.Code = env.Error.Code
			apiErr.Message = env.Error.Message
			if env.Error.RequestID != "" {
				apiErr.RequestID = env.Error.RequestID
			}
		} else {
			msg := strings.TrimSpace(string(body))
			if len(msg) > 200 {
				msg = msg[:200] + "…"
			}
			apiErr.Message = msg
		}
	}
	apiErr.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
	return apiErr
}

func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
