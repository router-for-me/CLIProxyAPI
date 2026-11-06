package helps

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
)

func TestImmutableRequestLogPreservesDeferredBudget(t *testing.T) {
	for _, tt := range []struct {
		name                                    string
		length, capacity, remaining, wantCharge int
		wantTruncated                           bool
	}{
		{name: "complete body", length: 8, capacity: 8, remaining: 16, wantCharge: 8},
		{name: "spare capacity charged", length: 8, capacity: 12, remaining: 16, wantCharge: 12},
		{name: "spare capacity exceeds budget", length: 8, capacity: 32, remaining: 16, wantCharge: 8},
		{name: "truncated body", length: 8, capacity: 8, remaining: 4, wantCharge: 4, wantTruncated: true},
		{name: "exhausted budget", length: 8, capacity: 8, wantTruncated: true},
		{name: "empty body with spare capacity", capacity: 32, remaining: 16},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx := context.WithValue(t.Context(), "gin", ginCtx)
			initial := maxDeferredAPIRequestBodyBytes - tt.remaining
			ginCtx.Set(deferredAPIRequestBytesKey, initial)
			body := make([]byte, tt.length, tt.capacity)
			copy(body, "abcdefgh")
			RecordAPIRequestImmutableBody(ctx, &config.Config{}, UpstreamRequestLog{URL: "https://example.test/responses", Method: http.MethodPost, Body: body})
			gotCharge, _ := ginCtx.Get(deferredAPIRequestBytesKey)
			if gotCharge != initial+tt.wantCharge {
				t.Errorf("retained bytes=%v, want %d", gotCharge, initial+tt.wantCharge)
			}
			raw, exists := ginCtx.Get(logging.DeferredAPIRequestContextKey)
			if !exists {
				t.Fatal("missing deferred capture")
			}
			captures := raw.([]logging.DeferredAPIRequest)
			if len(captures) != 1 {
				t.Fatalf("captures=%d, want 1", len(captures))
			}
			result := string(captures[0]())
			if strings.Contains(result, "TRUNCATED") != tt.wantTruncated {
				t.Errorf("unexpected truncation: %s", result)
			}
			want := "<empty>"
			if tt.length > 0 {
				want = string(body[:min(tt.length, tt.remaining)])
			}
			if !strings.Contains(result, "\nBody:\n"+want) {
				t.Errorf("missing captured body %q: %s", want, result)
			}
		})
	}
}

func TestImmutableRequestLogMatchesSnapshotOutput(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: enabled}}
			info := UpstreamRequestLog{URL: "https://example.test/responses", Method: http.MethodPost, Headers: http.Header{"X-Debug": {"one", "two"}}, Body: []byte(`{"model":"test","input":"hello"}`)}
			var results []string
			for _, record := range []func(context.Context, *config.Config, UpstreamRequestLog){RecordAPIRequest, RecordAPIRequestImmutableBody} {
				ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
				record(context.WithValue(t.Context(), "gin", ginCtx), cfg, info)
				var result string
				if enabled {
					result = string(ginCtx.MustGet(apiRequestKey).([]byte))
				} else {
					result = string(ginCtx.MustGet(logging.DeferredAPIRequestContextKey).([]logging.DeferredAPIRequest)[0]())
				}
				var lines []string
				for _, line := range strings.Split(result, "\n") {
					if !strings.HasPrefix(line, "Timestamp:") {
						lines = append(lines, line)
					}
				}
				results = append(results, strings.Join(lines, "\n"))
			}
			if results[0] != results[1] {
				t.Fatalf("immutable log differs:\n%s\n%s", results[0], results[1])
			}
		})
	}
}

func BenchmarkDeferredAPIRequestBody(b *testing.B) {
	gin.SetMode(gin.TestMode)
	for _, size := range []int{1024, 1 << 20, 8 << 20} {
		for _, immutable := range []bool{false, true} {
			b.Run(fmt.Sprintf("bytes=%d/immutable=%t", size, immutable), func(b *testing.B) {
				ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
				ctx := context.WithValue(b.Context(), "gin", ginCtx)
				cfg := &config.Config{}
				info := UpstreamRequestLog{Body: bytes.Repeat([]byte("x"), size)}
				record := RecordAPIRequest
				if immutable {
					record = RecordAPIRequestImmutableBody
				}
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					ginCtx.Keys = nil
					record(ctx, cfg, info)
				}
			})
		}
	}
}
