package codebuddy

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// GenerateRequestUUID returns a random RFC 4122 v4 style UUID for the
// X-Conversation-* / X-Request-ID headers. The official WorkBuddy client sends
// fresh values on every request and the upstream does not correlate them.
func GenerateRequestUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// GenerateHexID returns an n-byte random hex string for trace headers
// (16-byte trace id, 8-byte span id). Values are per-request random.
func GenerateHexID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// ApplyChatHeaders sets the session and identity headers the CodeBuddy chat
// endpoint requires. Without them the upstream routes the request through the
// strictest content-safety path and rejects otherwise harmless input.
func ApplyChatHeaders(req *http.Request, uid string) {
	req.Header.Set("X-Conversation-ID", GenerateRequestUUID())
	req.Header.Set("X-Conversation-Request-ID", GenerateRequestUUID())
	req.Header.Set("X-Conversation-Message-ID", GenerateRequestUUID())
	req.Header.Set("X-Request-ID", GenerateRequestUUID())
	req.Header.Set("X-Agent-Intent", "craft")
	req.Header.Set("X-Agent-Purpose", "conversation_topic")
	req.Header.Set("X-IDE-Type", "WorkBuddy")
	req.Header.Set("X-IDE-Name", "WorkBuddy")
	req.Header.Set("X-IDE-Version", "5.2.5")
	req.Header.Set("X-Private-Data", "false")
	req.Header.Set("X-Domain", DefaultDomain)
	req.Header.Set("X-Product", "SaaS")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("User-Agent", ClientUserAgent)
	if uid = strings.TrimSpace(uid); uid != "" {
		req.Header.Set("X-User-Id", uid)
	}
	// Stainless and trace headers identify the SDK origin and tracing context.
	// Random parts (trace/span ids) are regenerated per request.
	req.Header.Set("x-stainless-arch", "arm64")
	req.Header.Set("x-stainless-lang", "js")
	req.Header.Set("x-stainless-os", "MacOS")
	req.Header.Set("x-stainless-package-version", "6.25.0")
	req.Header.Set("x-stainless-retry-count", "0")
	req.Header.Set("x-stainless-runtime", "node")
	req.Header.Set("x-stainless-runtime-version", "v22.21.1")
	traceID := GenerateHexID(16)
	spanID := GenerateHexID(8)
	req.Header.Set("traceparent", "00-"+traceID+"-"+spanID+"-01")
	req.Header.Set("b3", traceID+"-"+spanID+"-1")
	req.Header.Set("X-B3-TraceId", traceID)
	req.Header.Set("X-B3-SpanId", spanID)
	req.Header.Set("X-B3-Sampled", "1")
	req.Header.Set("X-Trace-ID", traceID)
}
