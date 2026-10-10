package helps

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

// Recorded from api.meta.ai on 2026-10-08; only the tier id is replaced.
const metaSubscriptionUsageEvent = `{"subscription":{"tier":"tier-1","weekly":{"resets_at":1791763200,"used_percent":5},"window":{"resets_at":1791465557,"used_percent":0,"window_duration_mins":300}},"type":"response.subscription_usage"}`

func TestParseMetaSubscriptionUsageHeaders(t *testing.T) {
	headers := ParseMetaSubscriptionUsageHeaders([]byte(metaSubscriptionUsageEvent))
	want := map[string]string{
		MetaQuotaTierHeader:          "tier-1",
		MetaQuotaWindowUsedHeader:    "0",
		MetaQuotaWindowMinutesHeader: "300",
		MetaQuotaWindowResetHeader:   "1791465557",
		MetaQuotaWeeklyUsedHeader:    "5",
		MetaQuotaWeeklyResetHeader:   "1791763200",
	}
	if len(headers) != len(want) {
		t.Fatalf("headers = %#v, want %d entries", headers, len(want))
	}
	for name, value := range want {
		if got := headers.Get(name); got != value {
			t.Fatalf("%s = %q, want %q", name, got, value)
		}
	}
}

func TestParseMetaSubscriptionUsageKeepsOverQuotaAndDropsInvalidWindows(t *testing.T) {
	headers := ParseMetaSubscriptionUsageHeaders([]byte(`{"type":"response.subscription_usage","subscription":{` +
		`"window":{"used_percent":104.5,"window_duration_mins":300,"resets_at":1791465557},` +
		`"weekly":{"used_percent":-1,"resets_at":1791763200}}}`))
	if got := headers.Get(MetaQuotaWindowUsedHeader); got != "104.5" {
		t.Fatalf("window used = %q, want the over-quota value verbatim", got)
	}
	if headers.Get(MetaQuotaWeeklyUsedHeader) != "" || headers.Get(MetaQuotaWeeklyResetHeader) != "" {
		t.Fatalf("negative weekly percentage was kept: %#v", headers)
	}
	if headers.Get(MetaQuotaTierHeader) != "" {
		t.Fatalf("missing tier produced a header: %#v", headers)
	}

	for _, payload := range []string{
		``,
		`{"type":"response.completed","subscription":{"window":{"used_percent":1,"window_duration_mins":300,"resets_at":1}}}`,
		`{"type":"response.subscription_usage"}`,
		`{"type":"response.subscription_usage","subscription":{"window":{"used_percent":1,"window_duration_mins":0,"resets_at":1791465557}}}`,
		`{"type":"response.subscription_usage","subscription":{"weekly":{"used_percent":"5","resets_at":1791763200}}}`,
		`{"type":"response.subscription_usage","subscription":{"tier":"t","weekly":{"used_percent":5}}}`,
	} {
		if headers := ParseMetaSubscriptionUsageHeaders([]byte(payload)); headers != nil {
			t.Fatalf("payload %s produced %#v, want nil", payload, headers)
		}
	}
}

func TestParseMetaSubscriptionUsageDropsUnsafeTier(t *testing.T) {
	headers := ParseMetaSubscriptionUsageHeaders([]byte(`{"type":"response.subscription_usage","subscription":{"tier":"a\nb","weekly":{"used_percent":5,"resets_at":1791763200}}}`))
	if headers.Get(MetaQuotaWeeklyUsedHeader) != "5" || headers.Get(MetaQuotaTierHeader) != "" {
		t.Fatalf("headers = %#v, want the weekly window without the tier", headers)
	}
}

func TestParseMetaSubscriptionUsageSSEUsesTheLastEvent(t *testing.T) {
	body := "event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
		"event: response.subscription_usage\n" +
		"data: {\"type\":\"response.subscription_usage\",\"subscription\":{\"weekly\":{\"used_percent\":5,\"resets_at\":1791763200}}}\n\n" +
		"event: response.subscription_usage\n" +
		"data: " + metaSubscriptionUsageEvent + "\n\n"
	headers := ParseMetaSubscriptionUsageSSE([]byte(body))
	if headers.Get(MetaQuotaWindowMinutesHeader) != "300" || headers.Get(MetaQuotaTierHeader) != "tier-1" {
		t.Fatalf("headers = %#v, want the last event", headers)
	}
	if ParseMetaSubscriptionUsageSSE([]byte("data: {\"type\":\"response.completed\"}\n\n")) != nil {
		t.Fatal("a stream without subscription usage produced headers")
	}
}

func TestObserveMetaSubscriptionUsageMergesIntoResponseHeaders(t *testing.T) {
	ctx := logging.WithResponseHeadersHolder(context.Background())
	logging.SetResponseHeaders(ctx, http.Header{"X-Request-Id": []string{"req-1"}})
	ObserveMetaSubscriptionUsage(ctx, []byte(metaSubscriptionUsageEvent))
	headers := logging.GetResponseHeaders(ctx)
	if headers.Get("X-Request-Id") != "req-1" || headers.Get(MetaQuotaWeeklyUsedHeader) != "5" {
		t.Fatalf("response headers = %#v", headers)
	}
}
