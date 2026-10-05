package usage

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	log "github.com/sirupsen/logrus"
)

type callbackRecord struct {
	Status  string         `json:"status"`
	Payload map[string]any `json:"standard_logging_payload"`
}

type LiteLLMExporter struct {
	outbox *Outbox
	cfg    config.LiteLLMExporterConfig
	client *http.Client
	key    string
	mu     sync.RWMutex
	status string
	cancel context.CancelFunc
	done   chan struct{}
}

func NewLiteLLMExporter(outbox *Outbox, cfg config.LiteLLMExporterConfig) *LiteLLMExporter {
	// Redirects could forward the admin credential or change POST semantics.
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{}).DialContext, ForceAttemptHTTP2: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &LiteLLMExporter{outbox: outbox, cfg: cfg, client: client, key: os.Getenv(cfg.AdminKeyEnv), status: "disabled"}
}

func (e *LiteLLMExporter) Status() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.status
}

func (e *LiteLLMExporter) setStatus(status string) {
	e.mu.Lock()
	if e.status != status {
		log.WithField("accounting_exporter_status", status).Info("LiteLLM accounting status")
	}
	e.status = status
	e.mu.Unlock()
}

func (e *LiteLLMExporter) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel, e.done = cancel, make(chan struct{})
	go func() {
		defer close(e.done)
		if !e.cfg.Enabled {
			return
		}
		if e.outbox.db == nil {
			e.setStatus("local_store_unavailable")
			return
		}
		if e.key == "" {
			e.setStatus("credential_unavailable: set the configured admin-key-env in central secrets")
			return
		}

		e.setStatus("ready")
		delay := time.Second
		for {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				if err := e.ExportOnce(ctx); err != nil {
					e.setStatus("local_store_failure")
				}
				if e.Status() == "exported" || e.Status() == "ready" {
					delay = time.Second
				} else {
					delay = min(delay*2, time.Minute)
				}
			}
		}
	}()
}

func (e *LiteLLMExporter) Stop(ctx context.Context) error {
	if e.cancel == nil {
		return nil
	}
	e.cancel()
	e.client.CloseIdleConnections()
	select {
	case <-e.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func successPayload(event AccountingEvent, identity config.LiteLLMIdentity) (callbackRecord, error) {
	hash, errHash := hex.DecodeString(identity.KeyHash)
	if errHash != nil || len(hash) != 32 || event.Status != "succeeded" || event.Tokens == nil || !event.Tokens.Breakdown.Valid() || event.Tokens.Breakdown.Quality != TokenAccountingQualityComplete || event.Estimate == nil || event.Estimate.Status != "priced" || event.Estimate.Currency != "USD" || event.Estimate.Amount == nil {
		return callbackRecord{}, errors.New("requires mapped identity, complete usage and priced USD estimate")
	}
	cost, err := strconv.ParseFloat(*event.Estimate.Amount, 64)
	if err != nil || cost < 0 || math.IsNaN(cost) || math.IsInf(cost, 0) {
		return callbackRecord{}, errors.New("invalid estimate")
	}
	if event.StartedAt.IsZero() || event.CompletedAt.Before(event.StartedAt) {
		return callbackRecord{}, errors.New("invalid timestamps")
	}

	b := event.Tokens.Breakdown
	metadata := map[string]any{
		"user_api_key_hash":    identity.KeyHash,
		"user_api_key_user_id": identity.UserID,
		"user_api_key_team_id": identity.TeamID,
		"spend_logs_metadata":  map[string]any{"accounting_schema": event.SchemaVersion, "cost_kind": event.Estimate.Kind},
	}
	payload := map[string]any{
		"id": event.ExecutionID, "litellm_call_id": event.ExecutionID,
		"call_type": "acompletion", "model": event.ExecutedModel, "custom_llm_provider": event.Provider,
		"startTime": float64(event.StartedAt.UnixNano()) / 1e9, "endTime": float64(event.CompletedAt.UnixNano()) / 1e9,
		"completionStartTime": float64(event.CompletedAt.UnixNano()) / 1e9,
		"response_cost":       cost, "prompt_tokens": b.Input.TotalTokens, "completion_tokens": b.Output.TotalTokens,
		"total_tokens": b.TotalTokens, "metadata": metadata, "cache_hit": false,
		"status": "success", "messages": []any{}, "response": map[string]any{}, "model_map_information": map[string]any{},
	}
	return callbackRecord{Status: "success", Payload: payload}, nil
}

// ExportOnce replays pending successful events through LiteLLM's stock callback route.
// Delivery is at-least-once: spend-log rows deduplicate by ID, aggregate counters may double count a retry.
func (e *LiteLLMExporter) ExportOnce(ctx context.Context) error {
	if !e.cfg.Enabled {
		return nil
	}
	if e.key == "" {
		e.setStatus("credential_unavailable: set the configured admin-key-env in central secrets")
		return nil
	}
	ids, err := e.outbox.ReadyIDs(64)
	if err != nil {
		return err
	}
	records := []callbackRecord{}
	claimed := []string{}
	attempts := []uint64{}
	for _, id := range ids {
		if ctx.Err() != nil {
			return nil
		}
		event, err := e.outbox.Event(id)
		if err != nil {
			return err
		}
		record, errPayload := successPayload(event, e.cfg.Clients[event.ClientKeyID])
		ok, err := e.outbox.Claim(id)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		delivery, err := e.outbox.Delivery(id)
		if err != nil {
			return err
		}
		if event.Status != "succeeded" {
			// Stock LiteLLM drops replayed failures, so there is nothing it could record.
			if err := e.outbox.Resolve(id, delivery.Attempts, DeliveryRejected, time.Time{}); err != nil {
				return err
			}
			continue
		}
		if errPayload != nil {
			e.setStatus("mapping_required: verify client identity, complete usage and USD pricing")
			// No request has been sent. Retain the event for configuration repair.
			if err := e.outbox.Resolve(id, delivery.Attempts, DeliveryRetryable, e.outbox.now().Add(time.Hour)); err != nil {
				return err
			}
			continue
		}
		claimed, attempts, records = append(claimed, id), append(attempts, delivery.Attempts), append(records, record)
	}
	if len(records) == 0 {
		return nil
	}

	raw, err := json.Marshal(map[string]any{"records": records})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(e.cfg.URL, "/")+"/v1/rust_control_plane/logs", bytes.NewReader(raw))
	if err != nil {
		return errors.New("invalid exporter URL")
	}
	req.Header.Set("Authorization", "Bearer "+e.key)
	req.Header.Set("Content-Type", "application/json")

	state, hold := DeliveryRetryable, false
	resp, err := e.client.Do(req)
	if err != nil {
		e.setStatus("receiver_unavailable: check accounting service connectivity")
	} else {
		defer func() { _ = resp.Body.Close() }()
		switch resp.StatusCode {
		case 401, 403:
			hold = true
			e.setStatus("authorization_failure: verify proxy-admin secret")
		case 404, 405:
			hold = true
			e.setStatus("unsupported_route: verify LiteLLM exposes /v1/rust_control_plane/logs")
		case 422:
			state = DeliveryRejected
			e.setStatus("schema_failure")
		case 429:
			e.setStatus("rate_limited")
		case 200:
			state = DeliveryAcknowledged
		default:
			e.setStatus("service_failure")
		}
	}

	outcomes := make([]DeliveryState, len(records))
	for i := range outcomes {
		outcomes[i] = state
	}
	if state == DeliveryAcknowledged {
		e.applyCallbackResult(resp.Body, outcomes)
	}
	for i, id := range claimed {
		delay := min(time.Second*time.Duration(1<<min(attempts[i], uint64(6))), time.Minute)
		if hold {
			delay = time.Hour
		}
		if err := e.outbox.Resolve(id, attempts[i], outcomes[i], e.outbox.now().Add(delay)); err != nil {
			return err
		}
	}
	return nil
}

// applyCallbackResult downgrades the outcomes of records LiteLLM reports as failed.
func (e *LiteLLMExporter) applyCallbackResult(body io.Reader, outcomes []DeliveryState) {
	var result struct {
		Processed int `json:"processed"`
		Failed    int `json:"failed"`
		Failures  []struct {
			Index int `json:"index"`
		} `json:"failures"`
	}
	if err := json.NewDecoder(io.LimitReader(body, 1024*1024)).Decode(&result); err != nil || result.Processed < 0 || result.Failed != len(result.Failures) || result.Processed+result.Failed != len(outcomes) {
		e.setStatus("response_invalid")
		for i := range outcomes {
			outcomes[i] = DeliveryRetryable
		}
		return
	}
	for _, f := range result.Failures {
		if f.Index < 0 || f.Index >= len(outcomes) {
			e.setStatus("response_invalid")
			for i := range outcomes {
				outcomes[i] = DeliveryRetryable
			}
			return
		}
		outcomes[f.Index] = DeliveryRetryable
	}
	if result.Failed > 0 {
		e.setStatus("callback_failure")
		return
	}
	e.setStatus("exported")
}
