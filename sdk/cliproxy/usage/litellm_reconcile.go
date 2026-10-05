package usage

import (
	"encoding/json"
	"errors"
	bolt "go.etcd.io/bbolt"
	"math"
	"time"
)

// ExportReceipt contains observations from LiteLLM's read APIs after all receiver
// workers have drained. Counter deltas require an isolated identity and baseline.
type ExportReceipt struct {
	ID               string
	Rows             int
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

type ExportAggregate struct {
	Events           int64
	Successful       int64
	Failed           int64
	PromptTokens     int64
	CompletionTokens int64
	Spend            float64
}

type ExportReconciliation struct {
	Receipts []ExportReceipt
	// Keys are "key/", "user/", or "team/" followed by the LiteLLM identity.
	CounterDeltas map[string]ExportAggregate
}

// Reconcile acknowledges only verified deliveries. Missing rows or inconsistent
// counters remain quarantined. Absence is never permission to replay callbacks.
func (e *LiteLLMExporter) Reconcile(report ExportReconciliation) error {
	if len(report.Receipts) == 0 || len(report.Receipts) > e.outbox.cfg.MaxEvents {
		return errors.New("reconciliation receipt count exceeds outbox limits")
	}
	expected := map[string]ExportAggregate{}
	attempts := map[string]uint64{}
	for _, receipt := range report.Receipts {
		if _, exists := attempts[receipt.ID]; exists {
			return errors.New("duplicate reconciliation ID")
		}
		event, err := e.outbox.Event(receipt.ID)
		if err != nil {
			return err
		}
		d, err := e.outbox.Delivery(receipt.ID)
		if err != nil {
			return err
		}
		if d.State != DeliveryAmbiguous {
			return errors.New("reconciliation requires ambiguous delivery")
		}
		identity := e.cfg.Clients[event.ClientKeyID]
		record, err := completedPayload(event, identity)
		if err != nil {
			return err
		}
		b, cost := event.Tokens.Breakdown, record.Payload["response_cost"].(float64)
		if receipt.Rows != 1 || receipt.KeyHash != identity.KeyHash || receipt.UserID != identity.UserID || receipt.TeamID != identity.TeamID || receipt.Model != event.ExecutedModel || receipt.Provider != event.Provider || receipt.Status != record.Status || receipt.PromptTokens != b.Input.TotalTokens || receipt.CompletionTokens != b.Output.TotalTokens || receipt.TotalTokens != b.TotalTokens || !matchingSpend(receipt.Spend, cost) {
			return errors.New("spend row reconciliation mismatch")
		}
		attempts[receipt.ID] = d.Attempts
		keys := []string{"key/" + identity.KeyHash}
		if identity.UserID != "" {
			keys = append(keys, "user/"+identity.UserID)
		}
		if identity.TeamID != "" {
			keys = append(keys, "team/"+identity.TeamID)
		}
		for _, key := range keys {
			a := expected[key]
			a.Events++
			if record.Status == "success" {
				a.Successful++
			} else {
				a.Failed++
			}
			a.PromptTokens += b.Input.TotalTokens
			a.CompletionTokens += b.Output.TotalTokens
			a.Spend += cost
			expected[key] = a
		}
	}
	if len(expected) != len(report.CounterDeltas) {
		return errors.New("aggregate identity reconciliation mismatch")
	}
	for key, want := range expected {
		got, exists := report.CounterDeltas[key]
		if !exists || got.Events != want.Events || got.Successful != want.Successful || got.Failed != want.Failed || got.PromptTokens != want.PromptTokens || got.CompletionTokens != want.CompletionTokens || !matchingSpend(got.Spend, want.Spend) {
			return errors.New("aggregate counters reconciliation mismatch")
		}
	}
	return e.outbox.update(func(tx *bolt.Tx) error {
		if err := e.outbox.checkDisk(tx); err != nil {
			return err
		}
		for id, attempt := range attempts {
			var d Delivery
			if err := readDelivery(tx, id, &d); err != nil {
				return err
			}
			if d.State != DeliveryAmbiguous || d.Attempts != attempt {
				return errors.New("reconciliation delivery changed")
			}
			d.State, d.UpdatedAt, d.NextAttemptAt = DeliveryAcknowledged, e.outbox.now(), time.Time{}
			raw, err := json.Marshal(d)
			if err != nil {
				return err
			}
			if err := tx.Bucket(deliveriesBucket).Put([]byte(id), raw); err != nil {
				return err
			}
		}
		return nil
	})
}

func matchingSpend(got, want float64) bool {
	return !math.IsNaN(got) && !math.IsInf(got, 0) && math.Abs(got-want) <= 1e-9
}
