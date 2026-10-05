package usage

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func exportEvent(id string) AccountingEvent {
	amount := "0.000123456"
	return AccountingEvent{SchemaVersion: 1, ExecutionID: id, StartedAt: time.Unix(1700000000, 0), CompletedAt: time.Unix(1700000001, 0), ExecutedModel: "gpt-4o", Provider: "openai", ClientKeyID: "local-client", Status: "succeeded", Tokens: &AccountingTokens{Breakdown: NewSubsetTokenBreakdown(10, 2, 0, 5, 1, 15), Evidence: map[string]int64{"usage.prompt_tokens": 10}}, Estimate: &CostEstimate{Status: "priced", Kind: "api_equivalent_estimate", Currency: "USD", Amount: &amount}}
}

func exporterConfig(url string) config.LiteLLMExporterConfig {
	return config.LiteLLMExporterConfig{Enabled: true, URL: url, AdminKeyEnv: "TEST_LITELLM_ADMIN", Clients: map[string]config.LiteLLMIdentity{"local-client": {KeyHash: strings.Repeat("a", 64), UserID: "accounting-user", TeamID: "accounting-team"}}}
}

func TestLiteLLMOutcomes(t *testing.T) {
	cases := []struct {
		name   string
		code   int
		body   string
		state  DeliveryState
		status string
	}{
		{"accepted", 200, `{"processed":1,"failed":0,"failures":[]}`, DeliveryAcknowledged, "exported"},
		{"per-record", 200, `{"processed":0,"failed":1,"failures":[{"index":0,"error":"secret echoed"}]}`, DeliveryRetryable, "callback_failure"},
		{"bad-index", 200, `{"processed":0,"failed":1,"failures":[{"index":7}]}`, DeliveryRetryable, "response_invalid"},
		{"malformed", 200, `{`, DeliveryRetryable, "response_invalid"},
		{"auth", 403, `secret echoed`, DeliveryRetryable, "authorization_failure"},
		{"unsupported", 404, ``, DeliveryRetryable, "unsupported_route"},
		{"schema", 422, `secret echoed`, DeliveryRejected, "schema_failure"},
		{"rate", 429, ``, DeliveryRetryable, "rate_limited"},
		{"outage", 503, ``, DeliveryRetryable, "service_failure"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/v1/rust_control_plane/logs" || r.Header.Get("Authorization") != "Bearer test-admin" {
					t.Error("invalid request")
				}
				var batch struct {
					Records []callbackRecord `json:"records"`
				}
				if err := json.NewDecoder(r.Body).Decode(&batch); err != nil || len(batch.Records) != 1 {
					t.Error("invalid batch")
				}
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			t.Setenv("TEST_LITELLM_ADMIN", "test-admin")
			o := openTestOutbox(t, config.AccountingOutboxConfig{})
			if err := o.Insert(exportEvent("one")); err != nil {
				t.Fatal(err)
			}
			e := NewLiteLLMExporter(o, exporterConfig(server.URL))
			if err := e.ExportOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			d, _ := o.Delivery("one")
			if d.State != tc.state || !strings.HasPrefix(e.Status(), tc.status) || strings.Contains(e.Status(), "secret echoed") {
				t.Fatalf("%+v %s", d, e.Status())
			}
			if err := e.ExportOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatal("replayed before backoff")
			}
		})
	}
}

func TestLiteLLMCancellation(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
	}))
	defer server.Close()
	t.Setenv("TEST_LITELLM_ADMIN", "test-admin")
	o := openTestOutbox(t, config.AccountingOutboxConfig{})
	_ = o.Insert(exportEvent("cancel"))
	e := NewLiteLLMExporter(o, exporterConfig(server.URL))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.ExportOnce(ctx) }()
	<-entered
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	d, _ := o.Delivery("cancel")
	if d.State != DeliveryRetryable {
		t.Fatal(d)
	}
}

func TestLiteLLMSkipsFailedEvents(t *testing.T) {
	t.Setenv("TEST_LITELLM_ADMIN", "test-admin")
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("failed event sent to stock LiteLLM")
	}))
	defer server.Close()
	o := openTestOutbox(t, config.AccountingOutboxConfig{})
	for _, status := range []string{"failed", "interrupted"} {
		event := exportEvent(status)
		event.Status = status
		if err := o.Insert(event); err != nil {
			t.Fatal(err)
		}
	}
	e := NewLiteLLMExporter(o, exporterConfig(server.URL))
	if err := e.ExportOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"failed", "interrupted"} {
		if d, _ := o.Delivery(id); d.State != DeliveryRejected {
			t.Fatalf("%s: %+v", id, d)
		}
	}
}

func TestLiteLLMMappingRejectsSecretsAndIncompleteAccounting(t *testing.T) {
	identity := exporterConfig("").Clients["local-client"]
	event := exportEvent("mapping")
	identity.KeyHash = "sk-credential"
	if _, err := successPayload(event, identity); err == nil {
		t.Fatal("credential accepted as hash")
	}
	identity = exporterConfig("").Clients["local-client"]
	event.Tokens.Breakdown = NewUnclassifiedTokenBreakdown(15)
	if _, err := successPayload(event, identity); err == nil {
		t.Fatal("unclassified usage exported")
	}
	event = exportEvent("mapping")
	event.Estimate.Status = "partial"
	if _, err := successPayload(event, identity); err == nil {
		t.Fatal("partial price exported")
	}
	event = exportEvent("mapping")
	event.Status = "failed"
	if _, err := successPayload(event, identity); err == nil {
		t.Fatal("failed attempt exported")
	}
}

func TestLiteLLMBatchLimit(t *testing.T) {
	t.Setenv("TEST_LITELLM_ADMIN", "test-admin")
	batches := []int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var batch struct {
			Records []callbackRecord `json:"records"`
		}
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			t.Error(err)
		}
		batches = append(batches, len(batch.Records))
		_ = json.NewEncoder(w).Encode(map[string]any{"processed": len(batch.Records), "failed": 0, "failures": []any{}})
	}))
	defer server.Close()
	o := openTestOutbox(t, config.AccountingOutboxConfig{})
	for i := 0; i < 65; i++ {
		if err := o.Insert(exportEvent(strconv.Itoa(i))); err != nil {
			t.Fatal(err)
		}
	}
	e := NewLiteLLMExporter(o, exporterConfig(server.URL))
	for i := 0; i < 3; i++ {
		if err := e.ExportOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(batches) != 2 || batches[0] != 64 || batches[1] != 1 {
		t.Fatal(batches)
	}
}
