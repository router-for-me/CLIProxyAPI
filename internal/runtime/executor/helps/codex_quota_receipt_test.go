package helps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestCodexResetReceiptSurvivesDelayedResult(t *testing.T) {
	receipt := time.Now().UTC().Truncate(time.Second).Add(-2 * time.Minute)
	for _, tc := range []struct {
		name   string
		record func(context.Context, http.Header)
	}{
		{"http", func(ctx context.Context, h http.Header) { recordAPIResponseMetadataAt(ctx, nil, 200, h, receipt) }},
		{"handshake", func(ctx context.Context, h http.Header) { recordAPIWebsocketHandshakeAt(ctx, nil, 101, h, receipt) }},
		{"upgrade rejection", func(ctx context.Context, h http.Header) {
			recordAPIWebsocketUpgradeRejectionAt(ctx, nil, UpstreamRequestLog{}, 429, h, nil, receipt)
		}},
		{"websocket event", func(ctx context.Context, _ http.Header) {
			appendCodexAPIWebsocketResponseAt(ctx, nil, []byte(`{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":20,"window_minutes":10080,"reset_after_seconds":180},"secondary":{"used_percent":30,"window_minutes":300,"reset_after_seconds":240}}}`), receipt)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := internallogging.WithResponseHeadersHolder(context.Background())
			headers := http.Header{"X-Codex-Primary-Window-Minutes": {"10080"}, "X-Codex-Primary-Reset-After-Seconds": {"180"}, "X-Codex-Secondary-Window-Minutes": {"300"}, "X-Codex-Secondary-Reset-After-Seconds": {"240"}}
			original := headers.Clone()
			tc.record(ctx, headers)
			if !reflect.DeepEqual(headers, original) {
				t.Fatal("receipt conversion changed raw upstream headers")
			}
			manager := auth.NewManager(nil, nil, nil)
			selected, err := manager.Register(ctx, &auth.Auth{ID: "receipt", Provider: "codex", Status: auth.StatusActive})
			if err != nil {
				t.Fatal(err)
			}
			manager.MarkResult(ctx, auth.Result{AuthID: selected.ID, Provider: "codex", Success: true})
			got, _ := manager.GetByID(selected.ID)
			want := receipt.Add(180 * time.Second)
			if !got.QuotaResetSchedule.WeeklyResetAt.Equal(want) || !got.QuotaResetSchedule.FiveHourResetAt.Equal(receipt.Add(240*time.Second)) {
				t.Fatalf("delayed accounting shifted receipt resets: %+v, want weekly %s", got.QuotaResetSchedule, want)
			}
			competitor := &auth.Auth{ID: "competitor", Provider: "codex", Status: auth.StatusActive, QuotaResetSchedule: auth.QuotaResetSchedule{WeeklyResetAt: receipt.Add(240 * time.Second), ObservedAt: receipt}}
			winner, err := (&auth.GreedySelector{}).Pick(ctx, "codex", "", cliproxyexecutor.Options{}, []*auth.Auth{got, competitor})
			if err != nil || winner == nil || winner.ID != selected.ID {
				t.Fatalf("delayed result reversed greedy winner: winner=%+v err=%v", winner, err)
			}
		})
	}
}

func TestCodexResetReceiptPartialEvents(t *testing.T) {
	receipt := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	recordAPIResponseMetadataAt(ctx, nil, 200, http.Header{"X-Codex-Primary-Window-Minutes": {"10080"}, "X-Codex-Primary-Reset-At": {strconv.FormatInt(receipt.Add(time.Hour).Unix(), 10)}}, receipt)
	appendCodexAPIWebsocketResponseAt(ctx, nil, []byte(`{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":20,"window_minutes":10080,"reset_after_seconds":180}}}`), receipt.Add(time.Minute))
	appendCodexAPIWebsocketResponseAt(ctx, nil, []byte(`{"type":"codex.rate_limits","rate_limits":{"secondary":{"used_percent":30,"window_minutes":300,"reset_after_seconds":240}}}`), receipt.Add(2*time.Minute))
	appendCodexAPIWebsocketResponseAt(ctx, nil, []byte(`{"type":"response.output_text.delta","delta":"hello"}`), receipt.Add(3*time.Minute))
	got := internallogging.GetResponseHeaders(ctx)
	for key, want := range map[string]time.Time{"X-Codex-Primary-Reset-At": receipt.Add(4 * time.Minute), "X-Codex-Secondary-Reset-At": receipt.Add(6 * time.Minute)} {
		if got.Get(key) != strconv.FormatInt(want.Unix(), 10) {
			t.Fatalf("%s=%q want %s; later partial event must neither retain old absolute nor rebase previous timer", key, got.Get(key), want)
		}
	}
}

func TestCodexResetReceiptValidationAndRawLog(t *testing.T) {
	receipt := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ name, absolute, relative, want string }{
		{"relative", "", "180", strconv.FormatInt(receipt.Add(3*time.Minute).Unix(), 10)},
		{"absolute wins", "1794909900", "180", "1794909900"},
		{"invalid absolute fallback", "bad", "180", strconv.FormatInt(receipt.Add(3*time.Minute).Unix(), 10)},
		{"zero interval", "", "0", strconv.FormatInt(receipt.Unix(), 10)},
		{"negative interval", "", "-1", ""},
		{"oversized interval", "", "604801", ""},
		{"malformed interval", "", "bad", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx := internallogging.WithResponseHeadersHolder(context.WithValue(context.Background(), "gin", ginCtx))
			h := http.Header{"X-Codex-Primary-Window-Minutes": {"10080"}, "X-Codex-Primary-Reset-After-Seconds": {tc.relative}}
			if tc.absolute != "" {
				h.Set("X-Codex-Primary-Reset-At", tc.absolute)
			}
			original := h.Clone()
			cfg := &config.Config{}
			cfg.RequestLog = true
			recordAPIResponseMetadataAt(ctx, cfg, 200, h, receipt)
			got := internallogging.GetResponseHeaders(ctx).Get("X-Codex-Primary-Reset-At")
			want := tc.want
			if want == "" {
				want = tc.absolute
			}
			if got != want {
				t.Fatalf("reset-at=%q want %q", got, want)
			}
			if !reflect.DeepEqual(h, original) {
				t.Fatal("raw headers mutated")
			}
			raw, _ := ginCtx.Get(apiResponseKey)
			if tc.absolute == "" && strings.Contains(string(raw.([]byte)), "Reset-At:") {
				t.Fatalf("synthesized absolute reset leaked into raw response log: %s", raw)
			}
		})
	}
}

func TestCodexResetReceiptPartialErrorHeaders(t *testing.T) {
	receipt := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	recordAPIResponseMetadataAt(ctx, nil, 200, http.Header{
		"X-Codex-Primary-Window-Minutes": {"10080"},
		"X-Codex-Primary-Reset-At":       {strconv.FormatInt(receipt.Add(time.Hour).Unix(), 10)},
	}, receipt)
	// Error headers can update one field while the prior window duration remains
	// in the request holder. Anchor this update before merging the observations.
	appendCodexAPIWebsocketResponseAt(ctx, nil, []byte(`{"type":"error","status_code":429,"headers":{"X-Codex-Primary-Reset-After-Seconds":"60"}}`), receipt.Add(2*time.Minute))
	got := internallogging.GetResponseHeaders(ctx)
	want := strconv.FormatInt(receipt.Add(3*time.Minute).Unix(), 10)
	if got.Get("X-Codex-Primary-Reset-At") != want || got.Get("X-Codex-Primary-Window-Minutes") != "10080" {
		t.Fatalf("partial error retained or rebased the old deadline: %#v, want reset-at %s", got, want)
	}
}
