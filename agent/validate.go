package agent

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
)

// Valid 报告动作类型是否属于契约 v1 的合法取值。
func (a ActionType) Valid() bool {
	switch a {
	case ActionObserve, ActionBlock, ActionRedirect, ActionJSChallenge, ActionNone:
		return true
	default:
		return false
	}
}

// Valid 报告动作结果是否属于契约 v1 的合法取值。
func (r ActionResult) Valid() bool {
	switch r {
	case ResultExecuted, ResultChallenged, ResultPassed, ResultFailed, ResultSkipped:
		return true
	default:
		return false
	}
}

// Valid 报告决策来源是否属于契约 v1 的合法取值。
func (d DecisionSource) Valid() bool {
	switch d {
	case DecisionPolicy, DecisionNone, DecisionGatewayError:
		return true
	default:
		return false
	}
}

// Validate 校验单个事件。
func (e Event) Validate() error { return e.validate(-1) }

func (e Event) validate(index int) error {
	invalid := func(field, format string, args ...any) error {
		return &ValidationError{EventIndex: index, Field: field, Err: fmt.Errorf(format, args...)}
	}

	if e.EventID == "" || len(e.EventID) > 64 {
		return invalid("event_id", "必须为 1~64 字符")
	}
	if e.TS.IsZero() {
		return invalid("ts", "不能为零值")
	}
	if net.ParseIP(e.ClientIP) == nil {
		return invalid("client_ip", "不是合法 IP：%q", e.ClientIP)
	}
	if e.Host == "" || e.Path == "" || e.Method == "" {
		return invalid("host/path/method", "三者都不能为空")
	}
	if !e.Action.Valid() {
		return invalid("action", "非法取值：%q", e.Action)
	}
	if !e.ActionResult.Valid() {
		return invalid("action_result", "非法取值：%q", e.ActionResult)
	}
	if !e.DecisionSource.Valid() {
		return invalid("decision_source", "非法取值：%q", e.DecisionSource)
	}

	// 三态语义：命中策略必须与 decision_source=policy 一致。
	if e.DecisionSource == DecisionPolicy {
		if e.MatchedPolicyID == nil || *e.MatchedPolicyID == "" {
			return invalid("matched_policy_id", "decision_source=policy 时必须提供非空策略 ID")
		}
	} else if e.MatchedPolicyID != nil && *e.MatchedPolicyID != "" {
		return invalid("matched_policy_id", "decision_source=%s 时不能提供策略 ID", e.DecisionSource)
	}

	return nil
}

// VerifyChecksum 校验模板内容与声明的 sha256 checksum 是否一致。
func (t Template) VerifyChecksum() error {
	const prefix = "sha256:"
	if !strings.HasPrefix(t.Checksum, prefix) {
		return fmt.Errorf("不支持的 checksum 格式：%q", t.Checksum)
	}
	want, err := hex.DecodeString(strings.TrimPrefix(t.Checksum, prefix))
	if err != nil || len(want) != sha256.Size {
		return fmt.Errorf("checksum 非法：%q", t.Checksum)
	}
	got := sha256.Sum256([]byte(t.Content))
	if subtle.ConstantTimeCompare(got[:], want) != 1 {
		return errors.New("模板 checksum 不匹配")
	}
	return nil
}
