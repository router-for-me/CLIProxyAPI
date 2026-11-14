package openai

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHTTPSubmissionProviderRefusalBeforeAcceptanceCanFailOver(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var attempts atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				if attempts.Add(1) == 1 {
					w.WriteHeader(status)
					_, _ = io.WriteString(w, `{"error":{"message":"synthetic refusal"}}`)
					return
				}
				_, _ = io.WriteString(w, `{"id":"ok","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`)
			}))
			defer upstream.Close()
			response := submissionRequest(t, upstream.URL, false, nil)
			if response.Code != http.StatusOK || attempts.Load() != 2 {
				t.Fatalf("status=%d attempts=%d body=%s", response.Code, attempts.Load(), response.Body.String())
			}
		})
	}
}

func TestHTTPSubmissionAcceptedProviderPayloadMustContainErrorDetails(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		refusal bool
	}{
		{"explicit_refusal", `{"error":{"status":503,"message":"synthetic refusal"}}`, true},
		{"empty_error_object", `{"error":{}}`, false},
		{"error_event_without_error_payload", `{}`, false},
		{"malformed_error_json", `{"error":{"message":"truncated"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				if attempts.Add(1) == 1 {
					_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", tc.payload)
					return
				}
				_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer upstream.Close()
			response := submissionRequest(t, upstream.URL, true, nil)
			if tc.refusal {
				if response.Code != http.StatusOK || attempts.Load() != 2 || !strings.Contains(response.Body.String(), "done") {
					t.Fatalf("provider refusal no longer fails over: status=%d attempts=%d body=%s", response.Code, attempts.Load(), response.Body.String())
				}
				return
			}
			if response.Code != http.StatusBadGateway || attempts.Load() != 1 || !strings.Contains(response.Body.String(), `"code":"upstream_outcome_unknown"`) || response.Header().Get("x-should-retry") != "false" {
				t.Fatalf("unparsed failure treated as provider refusal: status=%d attempts=%d body=%s", response.Code, attempts.Load(), response.Body.String())
			}
		})
	}
}
