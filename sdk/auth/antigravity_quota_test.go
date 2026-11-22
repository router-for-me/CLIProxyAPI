package auth

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchAntigravityQuotaSummary(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != `{"project":"test-project"}` {
			t.Errorf("request body = %q, error = %v", body, err)
		}
		if r.Proto != "HTTP/1.1" || r.Method != http.MethodPost || r.URL.Path != AntigravityQuotaSummaryPath {
			t.Errorf("unexpected request %s %s %s", r.Proto, r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("User-Agent") != "test-agent" ||
			r.Header.Get("Content-Type") != "application/json" {
			t.Error("missing probe headers")
		}
		_, _ = io.WriteString(w, `{
			"groups":[{"displayName":" Gemini Argon ","buckets":[
				{"bucketId":"gemini-argon-5h","window":"5h","remainingFraction":0.8,"description":"rolling"},
				{"bucketId":"gemini-weekly","window":"weekly","resetTime":"2026-11-29T18:00:00Z"},
				{"bucketId":"disabled","window":"5h","remainingFraction":0,"disabled":true},
				{"bucketId":"missing","window":"5h"},
				{"bucketId":"unknown","window":"monthly","remainingFraction":0},
				{"bucketId":"invalid","window":"5h","remainingFraction":null,"resetTime":"2026-11-29T18:00:00Z"},
				{"bucketId":"gemini-weekly","window":"weekly","remainingFraction":0.9}
			]}],
			"buckets":[{"bucketId":"root","window":"5h","remainingFraction":1}]
		}`)
	}))
	server.EnableHTTP2 = true
	server.TLS = &tls.Config{GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		if len(hello.SupportedProtos) != 0 {
			t.Errorf("ClientHello ALPN = %v, want no ALPN extension", hello.SupportedProtos)
		}
		return nil, nil
	}}
	server.StartTLS()
	defer server.Close()
	original := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = original })
	got, status := FetchAntigravityQuotaSummary(t.Context(), AntigravityModelProbeRequest{
		BaseURLs: []string{server.URL}, AccessToken: " test-token ", ProjectID: " test-project ", UserAgent: " test-agent ",
	})
	if status != AntigravityProbeStatusSuccess || len(got) != 2 {
		t.Fatalf("summary status = %v, buckets = %+v", status, got)
	}
	if got[0].ID != "gemini-argon-5h" || got[0].Window != "5h" || got[0].GroupDisplayName != "Gemini Argon" ||
		got[0].Description != "rolling" || got[0].RemainingFraction != 0.8 {
		t.Errorf("unexpected Argon bucket %+v", got[0])
	}
	reset := time.Date(2026, 11, 29, 18, 0, 0, 0, time.UTC)
	if got[1].ID != "gemini-weekly" || got[1].RemainingFraction != 0 || !got[1].ResetTime.Equal(reset) {
		t.Errorf("unexpected proto3-omitted fraction bucket %+v", got[1])
	}
}

func TestFetchAntigravityQuotaSummaryOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		httpStatus int
		body       string
		probe      AntigravityProbeStatus
		buckets    int
	}{
		{"flat buckets", 200, `{"groups":[{"buckets":[]}],"buckets":[{"bucketId":"3p-5h","window":"5h","remainingFraction":0}]}`, AntigravityProbeStatusSuccess, 1},
		{"restricted", 200, `{"groups":[{"buckets":[{"bucketId":"gemini-5h","window":"5h","disabled":true,"remainingFraction":0}]}]}`, AntigravityProbeStatusSuccess, 0},
		{"empty body", 204, "", AntigravityProbeStatusSuccess, 0},
		{"missing endpoint", 404, "", AntigravityProbeStatusSuccess, 0},
		{"unsupported method", 405, "", AntigravityProbeStatusSuccess, 0},
		{"unimplemented", 501, "", AntigravityProbeStatusSuccess, 0},
		{"unauthorized", 401, "", AntigravityProbeStatusAuthError, 0},
		{"forbidden", 403, "", AntigravityProbeStatusForbidden, 0},
		{"unavailable", 503, "", AntigravityProbeStatusTransientError, 0},
		{"malformed", 200, `{"buckets":`, AntigravityProbeStatusTransientError, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.httpStatus)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			buckets, status := FetchAntigravityQuotaSummary(t.Context(), AntigravityModelProbeRequest{
				BaseURLs: []string{server.URL}, AccessToken: "test-token",
			})
			if status != tc.probe || len(buckets) != tc.buckets {
				t.Fatalf("summary status = %v, buckets = %+v", status, buckets)
			}
		})
	}
}

func TestParseAntigravityWeeklyModelQuotas(t *testing.T) {
	hints, ok := ParseAntigravityModelCapabilityHints([]byte(`{"models":{
		"gemini-pro":{"quotaInfo":{"remainingFraction":0.8},"weeklyQuotaInfo":{"remainingFraction":0.2}},
		"claude":{"weeklyQuotaInfo":{"resetTime":"2026-11-29T18:00:00Z"}},
		"disabled":{"disabled":true,"weeklyQuotaInfo":{"remainingFraction":0}},
		"invalid":{"weeklyQuotaInfo":{"remainingFraction":null}}
	}}`))
	if !ok || len(hints.ModelQuotas) != 1 || len(hints.WeeklyModelQuotas) != 2 {
		t.Fatalf("unexpected quotas %+v", hints)
	}
	claude := hints.WeeklyModelQuotas["claude"]
	if hints.ModelQuotas["gemini-pro"].RemainingFraction != 0.8 || hints.WeeklyModelQuotas["gemini-pro"].RemainingFraction != 0.2 ||
		claude.ModelID != "claude" || claude.RemainingFraction != 0 || claude.ResetTime.IsZero() {
		t.Fatalf("unexpected quota values %+v", hints)
	}
	cloned := hints.Clone()
	delete(cloned.WeeklyModelQuotas, "claude")
	if len(hints.WeeklyModelQuotas) != 2 {
		t.Error("clone shares the original weekly model quotas")
	}
}
