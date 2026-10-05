package test

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type receiverBehavior int

const (
	receiverAccept receiverBehavior = iota
	receiverOffline
	receiverStall
	receiverServerError
	// receiverLoseResponse applies the batch, then drops the connection before replying.
	receiverLoseResponse
	// receiverPartialFailure reports per-record callback failures inside an HTTP 200.
	receiverPartialFailure
)

func receiverBehaviorFor(mode accountingMode) receiverBehavior {
	switch mode {
	case accountingLiteLLMOffline:
		return receiverOffline
	case accountingLiteLLMStalled:
		return receiverStall
	case accountingLiteLLMFailing:
		return receiverServerError
	default:
		return receiverAccept
	}
}

// spendAggregate is one identity counter as the simulated LiteLLM read API reports it.
type spendAggregate struct {
	Events           int64
	Successful       int64
	Failed           int64
	PromptTokens     int64
	CompletionTokens int64
	Spend            float64
}

// spendReceipt is the spend-log observation for one completion ID.
type spendReceipt struct {
	ID               string
	Rows             int
	KeyHash          string
	Model            string
	Provider         string
	Status           string
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	Spend            float64
}

// spendRow is one simulated spend-log row.
type spendRow struct {
	ID               string
	KeyHash          string
	UserID           string
	TeamID           string
	Model            string
	Provider         string
	Status           string
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	Spend            float64
}

// fakeLiteLLM simulates the stock receiver behavior documented in docs/litellm-exporter.md:
// spend-log rows deduplicate on the completion ID, while identity aggregates increment once
// per processed callback. It is a transport and bookkeeping model for the exporter, not a
// substitute for a check against a deployed LiteLLM.
type fakeLiteLLM struct {
	t        *testing.T
	baseURL  string
	mu       sync.Mutex
	behavior receiverBehavior
	// failIDs lists completion IDs whose callback fails under receiverPartialFailure.
	failIDs map[string]bool

	rows       map[string]spendRow
	aggregates map[string]spendAggregate
	// batches records the completion IDs of every logs request in arrival order.
	batches [][]string
	// processed counts callbacks that ran, including duplicates of an existing row.
	processed int

	logsHits    atomic.Int64
	release     chan struct{}
	releaseOnce sync.Once
	// leaked collects any request body that contains credential material.
	leaked []string
}

func newFakeLiteLLM(t *testing.T, behavior receiverBehavior) *fakeLiteLLM {
	t.Helper()
	f := &fakeLiteLLM{
		t: t, behavior: behavior, failIDs: map[string]bool{},
		rows: map[string]spendRow{}, aggregates: map[string]spendAggregate{},
		release: make(chan struct{}),
	}
	if behavior == receiverOffline {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		f.baseURL = "http://" + listener.Addr().String()
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
		return f
	}

	server := httptest.NewServer(http.HandlerFunc(f.serve))
	f.baseURL = server.URL
	t.Cleanup(func() {
		f.releaseStalled()
		server.CloseClientConnections()
		server.Close()
	})
	return f
}

func (f *fakeLiteLLM) URL() string { return f.baseURL }

func (f *fakeLiteLLM) releaseStalled() { f.releaseOnce.Do(func() { close(f.release) }) }

func (f *fakeLiteLLM) setBehavior(behavior receiverBehavior) {
	f.mu.Lock()
	f.behavior = behavior
	f.mu.Unlock()
}

func (f *fakeLiteLLM) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+gatewayAdminSecret {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	f.logsHits.Add(1)
	raw, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	behavior := f.behavior
	f.mu.Unlock()
	f.checkForCredentials(string(raw))

	switch behavior {
	case receiverStall:
		select {
		case <-f.release:
		case <-r.Context().Done():
		}
		return
	case receiverServerError:
		http.Error(w, "receiver failure", http.StatusInternalServerError)
		return
	}

	var batch struct {
		Records []struct {
			Status  string         `json:"status"`
			Payload map[string]any `json:"standard_logging_payload"`
		} `json:"records"`
	}
	if err := json.Unmarshal(raw, &batch); err != nil {
		http.Error(w, "bad batch", http.StatusUnprocessableEntity)
		return
	}

	f.mu.Lock()
	ids := make([]string, 0, len(batch.Records))
	type failure struct {
		Index int `json:"index"`
	}
	failures := []failure{}
	for i, record := range batch.Records {
		id, _ := record.Payload["id"].(string)
		ids = append(ids, id)
		if behavior == receiverPartialFailure && f.failIDs[id] {
			failures = append(failures, failure{Index: i})
			continue
		}
		f.apply(id, record.Status, record.Payload)
	}
	f.batches = append(f.batches, ids)
	f.mu.Unlock()

	if behavior == receiverLoseResponse {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"processed": len(batch.Records) - len(failures), "failed": len(failures), "failures": failures,
	})
}

func (f *fakeLiteLLM) checkForCredentials(body string) {
	for _, secret := range []string{gatewayAdminSecret, gatewayClientKey, "acct-"} {
		if strings.Contains(body, secret) {
			f.mu.Lock()
			f.leaked = append(f.leaked, secret)
			f.mu.Unlock()
		}
	}
}

// apply must be called with f.mu held.
func (f *fakeLiteLLM) apply(id, status string, payload map[string]any) {
	metadata, _ := payload["metadata"].(map[string]any)
	number := func(name string) int64 { v, _ := payload[name].(float64); return int64(v) }
	text := func(m map[string]any, name string) string { v, _ := m[name].(string); return v }
	spend, _ := payload["response_cost"].(float64)
	row := spendRow{
		ID: id, KeyHash: text(metadata, "user_api_key_hash"), UserID: text(metadata, "user_api_key_user_id"),
		TeamID: text(metadata, "user_api_key_team_id"), Model: text(payload, "model"), Provider: text(payload, "custom_llm_provider"),
		Status: status, PromptTokens: number("prompt_tokens"), CompletionTokens: number("completion_tokens"),
		TotalTokens: number("total_tokens"), Spend: spend,
	}
	f.processed++
	if _, exists := f.rows[id]; !exists {
		f.rows[id] = row
	}
	for _, key := range []string{"key/" + row.KeyHash, "user/" + row.UserID, "team/" + row.TeamID} {
		aggregate := f.aggregates[key]
		aggregate.Events++
		if status == "success" {
			aggregate.Successful++
		} else {
			aggregate.Failed++
		}
		aggregate.PromptTokens += row.PromptTokens
		aggregate.CompletionTokens += row.CompletionTokens
		aggregate.Spend += row.Spend
		f.aggregates[key] = aggregate
	}
}

// receipts reads the simulated spend-log API for the given completion IDs.
func (f *fakeLiteLLM) receipts(ids ...string) []spendReceipt {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]spendReceipt, 0, len(ids))
	for _, id := range ids {
		row, ok := f.rows[id]
		rows := 0
		if ok {
			rows = 1
		}
		out = append(out, spendReceipt{
			ID: id, Rows: rows, KeyHash: row.KeyHash, Model: row.Model,
			Provider: row.Provider, Status: row.Status, PromptTokens: row.PromptTokens,
			CompletionTokens: row.CompletionTokens, TotalTokens: row.TotalTokens, Spend: row.Spend,
		})
	}
	return out
}

func (f *fakeLiteLLM) aggregateSnapshot() map[string]spendAggregate {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]spendAggregate, len(f.aggregates))
	for key, aggregate := range f.aggregates {
		out[key] = aggregate
	}
	return out
}

func (f *fakeLiteLLM) leakedSecrets() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.leaked...)
}

func (f *fakeLiteLLM) rowCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rows)
}

func (f *fakeLiteLLM) processedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.processed
}
