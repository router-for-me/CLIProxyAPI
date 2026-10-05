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

const LiteLLMVersion = "1.103.0"

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
		if e.cfg.Version != LiteLLMVersion || e.cfg.ReceiverProfile != "ai-proxy-completed-v1" {
			e.setStatus("unsupported_version: verify LiteLLM 1.103.0 with completed-failures.patch and receiver-profile ai-proxy-completed-v1")
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
				if strings.HasPrefix(e.Status(), "callbacks_accepted") || e.Status() == "ready" {
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

func completedPayload(event AccountingEvent, identity config.LiteLLMIdentity) (callbackRecord, error) {
	hash, errHash := hex.DecodeString(identity.KeyHash)
	if errHash != nil || len(hash) != 32 || event.Tokens == nil || !event.Tokens.Breakdown.Valid() || event.Tokens.Breakdown.Quality != TokenAccountingQualityComplete || event.Estimate == nil || event.Estimate.Status != "priced" || event.Estimate.Currency != "USD" || event.Estimate.Amount == nil {
		return callbackRecord{}, errors.New("requires mapped identity, complete usage and priced USD estimate")
	}
	cost, err := strconv.ParseFloat(*event.Estimate.Amount, 64)
	if err != nil || cost < 0 || math.IsNaN(cost) || math.IsInf(cost, 0) {
		return callbackRecord{}, errors.New("invalid estimate")
	}
	if event.StartedAt.IsZero() || event.CompletedAt.Before(event.StartedAt) {
		return callbackRecord{}, errors.New("invalid timestamps")
	}

	if event.Status != "succeeded" && event.Status != "failed" && event.Status != "interrupted" {
		return callbackRecord{}, errors.New("invalid attempt status")
	}
	status := "success"
	if event.Status != "succeeded" {
		status = "failure"
	}
	b := event.Tokens.Breakdown
	metadata := map[string]any{
		"user_api_key_hash":    identity.KeyHash,
		"user_api_key_user_id": identity.UserID,
		"user_api_key_team_id": identity.TeamID,
		"spend_logs_metadata":  map[string]any{"producer": "ai-proxy-completed-v1", "accounting_schema": event.SchemaVersion, "cost_kind": event.Estimate.Kind},
	}
	payload := map[string]any{
		"id": event.ExecutionID, "litellm_call_id": event.ExecutionID,
		"call_type": "acompletion", "model": event.ExecutedModel, "custom_llm_provider": event.Provider,
		"startTime": float64(event.StartedAt.UnixNano()) / 1e9, "endTime": float64(event.CompletedAt.UnixNano()) / 1e9,
		"completionStartTime": float64(event.CompletedAt.UnixNano()) / 1e9,
		"response_cost":       cost, "prompt_tokens": b.Input.TotalTokens, "completion_tokens": b.Output.TotalTokens,
		"total_tokens": b.TotalTokens, "metadata": metadata, "cache_hit": false,
		"status": status, "messages": []any{}, "response": map[string]any{}, "model_map_information": map[string]any{},
	}
	if status == "failure" {
		payload["error_str"] = "proxy attempt " + event.Status
	}
	return callbackRecord{Status: status, Payload: payload}, nil
}

// ExportOnce claims before I/O. Callback errors may follow partial side effects.
func (e *LiteLLMExporter) ExportOnce(ctx context.Context) error {
	if !e.cfg.Enabled {
		return nil
	}
	if e.cfg.Version != LiteLLMVersion || e.cfg.ReceiverProfile != "ai-proxy-completed-v1" {
		e.setStatus("unsupported_version: verify LiteLLM 1.103.0 and completed-failures.patch")
		return nil
	}
	if e.key == "" {
		e.setStatus("credential_unavailable: set the configured admin-key-env in central secrets")
		return nil
	}
	if !e.verifyReceiver(ctx) {
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
		record, errPayload := completedPayload(event, e.cfg.Clients[event.ClientKeyID])
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
	resp, err := e.client.Do(req)
	if err != nil {
		e.setStatus("transport_ambiguous: reconcile before replay")
		return nil
	}
	defer func() { _ = resp.Body.Close() }()

	state := DeliveryAmbiguous
	switch resp.StatusCode {
	case 401, 403:
		state = DeliveryRetryable
		e.setStatus("authorization_failure: verify proxy-admin secret")
	case 404, 405:
		state = DeliveryRetryable
		e.setStatus("unsupported_route: verify LiteLLM 1.103.0 route availability")
	case 422:
		state = DeliveryRejected
		e.setStatus("schema_failure")
	case 429:
		state = DeliveryRetryable
		e.setStatus("rate_limited")
	case 200:
		e.setStatus("callbacks_accepted: persistence requires reconciliation")
	default:
		e.setStatus("service_failure_ambiguous: reconcile before replay")
	}
	outcomes := make([]DeliveryState, len(records))
	for i := range outcomes {
		outcomes[i] = state
	}
	if resp.StatusCode == 200 {
		var result struct {
			Processed int `json:"processed"`
			Failed    int `json:"failed"`
			Failures  []struct {
				Index int `json:"index"`
			} `json:"failures"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&result); err != nil || result.Processed < 0 || result.Failed != len(result.Failures) || result.Processed+result.Failed != len(records) {
			e.setStatus("response_ambiguous: reconcile before replay")
		} else {
			seen := map[int]bool{}
			valid := true
			for _, f := range result.Failures {
				if f.Index < 0 || f.Index >= len(records) || seen[f.Index] {
					valid = false
					break
				}
				seen[f.Index] = true
			}
			if valid {
				// Even successful callback completion is not a database commit acknowledgement.
				if result.Failed > 0 {
					e.setStatus("callback_failure_ambiguous: reconcile before replay")
				}
			} else {
				e.setStatus("response_ambiguous: reconcile before replay")
			}
		}
	}
	for i, id := range claimed {
		delay := min(time.Second*time.Duration(1<<min(attempts[i], uint64(6))), time.Minute)
		if state == DeliveryRetryable && resp.StatusCode != 429 {
			delay = time.Hour
		}
		if err := e.outbox.Resolve(id, attempts[i], outcomes[i], e.outbox.now().Add(delay)); err != nil {
			return err
		}
	}
	return nil
}

func (e *LiteLLMExporter) verifyReceiver(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(e.cfg.URL, "/")+"/v1/rust_control_plane/logs/capabilities", nil)
	if err != nil {
		e.setStatus("invalid_configuration")
		return false
	}
	req.Header.Set("Authorization", "Bearer "+e.key)
	resp, err := e.client.Do(req)
	if err != nil {
		e.setStatus("receiver_unavailable: check accounting service connectivity")
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		e.setStatus("authorization_failure: verify proxy-admin secret")
		return false
	}
	if resp.StatusCode == 429 {
		e.setStatus("rate_limited")
		return false
	}
	if resp.StatusCode >= 500 {
		e.setStatus("receiver_unavailable: check accounting service health")
		return false
	}
	var capability struct {
		Version string `json:"version"`
		Profile string `json:"profile"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&capability) != nil || capability.Version != LiteLLMVersion || capability.Profile != "ai-proxy-completed-v1" {
		e.setStatus("unsupported_route: install completed-failures.patch on LiteLLM 1.103.0")
		return false
	}
	return true
}
