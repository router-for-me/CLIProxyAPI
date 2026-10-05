package usage

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	return config.LiteLLMExporterConfig{Enabled: true, URL: url, Version: LiteLLMVersion, ReceiverProfile: "ai-proxy-completed-v1", AdminKeyEnv: "TEST_LITELLM_ADMIN", Clients: map[string]config.LiteLLMIdentity{"local-client": {KeyHash: strings.Repeat("a", 64), UserID: "accounting-user", TeamID: "accounting-team"}}}
}

func TestLiteLLMOutcomes(t *testing.T) {
	cases := []struct {
		name   string
		code   int
		body   string
		state  DeliveryState
		status string
	}{
		{"accepted", 200, `{"processed":1,"failed":0,"failures":[]}`, DeliveryAmbiguous, "callbacks_accepted"},
		{"per-record", 200, `{"processed":0,"failed":1,"failures":[{"index":0,"error":"secret echoed"}]}`, DeliveryAmbiguous, "callback_failure"},
		{"bad-index", 200, `{"processed":0,"failed":1,"failures":[{"index":7}]}`, DeliveryAmbiguous, "response_ambiguous"},
		{"malformed", 200, `{`, DeliveryAmbiguous, "response_ambiguous"},
		{"auth", 403, `secret echoed`, DeliveryRetryable, "authorization_failure"},
		{"unsupported", 404, ``, DeliveryRetryable, "unsupported_route"},
		{"schema", 422, `secret echoed`, DeliveryRejected, "schema_failure"},
		{"rate", 429, ``, DeliveryRetryable, "rate_limited"},
		{"outage", 503, ``, DeliveryAmbiguous, "service_failure"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					_, _ = w.Write([]byte(`{"version":"1.103.0","profile":"ai-proxy-completed-v1"}`))
					return
				}
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
				t.Fatal("automatic replay")
			}
		})
	}
}

func TestLiteLLMLostResponseAndReconciliation(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"version":"1.103.0","profile":"ai-proxy-completed-v1"}`))
			return
		}
		calls++
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()
	t.Setenv("TEST_LITELLM_ADMIN", "test-admin")
	cfg := config.AccountingOutboxConfig{DataPath: t.TempDir()}
	o := openTestOutbox(t, cfg)
	_ = o.Insert(exportEvent("lost"))
	e := NewLiteLLMExporter(o, exporterConfig(server.URL))
	if err := e.ExportOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = o.Close(context.Background())
	recovered := openTestOutbox(t, cfg)
	e = NewLiteLLMExporter(recovered, exporterConfig(server.URL))
	_ = e.ExportOnce(context.Background())
	if calls != 1 {
		t.Fatal("replayed after lost response and restart")
	}

	receipt := ExportReceipt{ID: "lost", Rows: 1, KeyHash: strings.Repeat("a", 64), UserID: "accounting-user", TeamID: "accounting-team", Model: "gpt-4o", Provider: "openai", Status: "success", PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, Spend: 0.000123456}
	aggregate := ExportAggregate{Events: 1, Successful: 1, PromptTokens: 10, CompletionTokens: 5, Spend: receipt.Spend}
	report := ExportReconciliation{Receipts: []ExportReceipt{receipt}, CounterDeltas: map[string]ExportAggregate{"key/" + receipt.KeyHash: aggregate, "user/" + receipt.UserID: aggregate, "team/" + receipt.TeamID: aggregate}}
	duplicated := aggregate
	duplicated.Events = 2
	report.CounterDeltas["team/"+receipt.TeamID] = duplicated
	if err := e.Reconcile(report); err == nil {
		t.Fatal("duplicate counter accepted")
	}
	report.CounterDeltas["team/"+receipt.TeamID] = aggregate
	if err := e.Reconcile(report); err != nil {
		t.Fatal(err)
	}
	d, _ := recovered.Delivery("lost")
	if d.State != DeliveryAcknowledged {
		t.Fatal(d)
	}
}

func TestLiteLLMContractFixture(t *testing.T) {
	records := []callbackRecord{}
	cfg := exporterConfig("")
	for _, status := range []string{"succeeded", "failed", "interrupted"} {
		event := exportEvent(status)
		event.Status = status
		record, err := completedPayload(event, cfg.Clients[event.ClientKeyID])
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	raw, err := json.MarshalIndent(map[string]any{"records": records}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "..", "test", "litellm", "records.json")
	if os.Getenv("UPDATE_LITELLM_FIXTURE") == "1" {
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil || string(want) != string(raw) {
		t.Fatal("contract fixture stale, run UPDATE_LITELLM_FIXTURE=1 go test ./sdk/cliproxy/usage -run TestLiteLLMContractFixture")
	}
	if strings.Contains(string(raw), "test-admin") || strings.Contains(string(raw), "usage.prompt_tokens") {
		t.Fatal("credentials or raw evidence exported")
	}
}

func TestLiteLLMCancellationAndVersion(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"version":"1.103.0","profile":"ai-proxy-completed-v1"}`))
			return
		}
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
	if d.State != DeliveryAmbiguous {
		t.Fatal(d)
	}
	cfg := exporterConfig(server.URL)
	cfg.Version = "1.0"
	e = NewLiteLLMExporter(o, cfg)
	e.Start()
	<-e.done
	if err := e.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(e.Status(), "unsupported_version") {
		t.Fatal(e.Status())
	}
}

func TestLiteLLMUnsupportedReceiverDoesNotClaim(t *testing.T) {
	t.Setenv("TEST_LITELLM_ADMIN", "test-admin")
	for _, body := range []string{`{"version":"1.102.0","profile":"ai-proxy-completed-v1"}`, `{"version":"1.103.0","profile":"stock"}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Error("sent unverified batch")
			}
			_, _ = w.Write([]byte(body))
		}))
		o := openTestOutbox(t, config.AccountingOutboxConfig{})
		_ = o.Insert(exportEvent("unsupported"))
		e := NewLiteLLMExporter(o, exporterConfig(server.URL))
		if err := e.ExportOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		d, _ := o.Delivery("unsupported")
		if d.State != DeliveryPending || d.Attempts != 0 || !strings.HasPrefix(e.Status(), "unsupported_route") {
			t.Fatalf("%+v %s", d, e.Status())
		}
		server.Close()
	}
}

func TestLiteLLMMappingRejectsSecretsAndIncompleteAccounting(t *testing.T) {
	identity := exporterConfig("").Clients["local-client"]
	event := exportEvent("mapping")
	identity.KeyHash = "sk-credential"
	if _, err := completedPayload(event, identity); err == nil {
		t.Fatal("credential accepted as hash")
	}
	identity = exporterConfig("").Clients["local-client"]
	event.Tokens.Breakdown = NewUnclassifiedTokenBreakdown(15)
	if _, err := completedPayload(event, identity); err == nil {
		t.Fatal("unclassified usage exported")
	}
	event = exportEvent("mapping")
	event.Estimate.Status = "partial"
	if _, err := completedPayload(event, identity); err == nil {
		t.Fatal("partial price exported")
	}
	event = exportEvent("mapping")
	event.Status = "upstream credential text"
	if _, err := completedPayload(event, identity); err == nil {
		t.Fatal("unsafe status exported")
	}
}

func TestLiteLLMBatchLimit(t *testing.T) {
	t.Setenv("TEST_LITELLM_ADMIN", "test-admin")
	batches := []int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"version":"1.103.0","profile":"ai-proxy-completed-v1"}`))
			return
		}
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
