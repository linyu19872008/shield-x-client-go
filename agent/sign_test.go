package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestReportEventsSignsBody(t *testing.T) {
	var body []byte
	var sigHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		sigHeader = r.Header.Get("X-Shieldx-Signature")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":1,"rejected":0}`))
	}))
	defer srv.Close()

	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	c, err := New(Config{
		BaseURL: srv.URL, Token: "tok", GatewayID: "gw", AgentVersion: "gateway/1.0",
		SigningKey: hex.EncodeToString(seed),
	})
	if err != nil {
		t.Fatal(err)
	}
	batch := EventBatch{ConfigRevision: 1, Events: []Event{{
		EventID: "e1", TS: time.Now().UTC(), ClientIP: "203.0.113.1",
		Host: "example.com", Path: "/", Method: "GET", Status: 200,
		Action: ActionNone, ActionResult: ResultPassed, DecisionSource: DecisionNone,
	}}}
	if _, err := c.ReportEvents(context.Background(), batch); err != nil {
		t.Fatalf("上报失败：%v", err)
	}
	if sigHeader == "" {
		t.Fatal("缺少签名头")
	}
	var tsRaw, sigRaw string
	for _, part := range strings.Split(sigHeader, ",") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			tsRaw = kv[1]
		case "v1":
			sigRaw = kv[1]
		}
	}
	ts, _ := strconv.ParseInt(tsRaw, 10, 64)
	sig, _ := hex.DecodeString(sigRaw)
	sum := sha256.Sum256(body)
	msg := fmt.Sprintf("%d\nPOST\n/events/v1\n%s", ts, hex.EncodeToString(sum[:]))
	if !ed25519.Verify(priv.Public().(ed25519.PublicKey), []byte(msg), sig) {
		t.Fatal("签名校验失败")
	}
}

func TestConfigRejectsBadSigningKey(t *testing.T) {
	_, err := New(Config{
		BaseURL: "http://example.com", Token: "t", GatewayID: "gw", AgentVersion: "v",
		SigningKey: "bad",
	})
	if err == nil {
		t.Fatal("非法 SigningKey 应报错")
	}
}
