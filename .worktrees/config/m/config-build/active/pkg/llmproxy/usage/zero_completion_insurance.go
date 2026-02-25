// Package usage provides Zero Completion Insurance functionality.
// This ensures users are not charged for requests that result in zero output tokens
// due to errors or blank responses.
package usage

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// CompletionStatus represents the status of a request completion
type CompletionStatus string

const (
	// StatusSuccess indicates successful completion
	StatusSuccess CompletionStatus = "success"
	// StatusZeroTokens indicates zero output tokens (should be refunded)
	StatusZeroTokens CompletionStatus = "zero_tokens"
	// StatusError indicates an error occurred (should be refunded)
	StatusError CompletionStatus = "error"
	// StatusFiltered indicates content was filtered (partial refund)
	StatusFiltered CompletionStatus = "filtered"
)

// RequestRecord tracks a request for insurance purposes
type RequestRecord struct {
	RequestID    string
	ModelID     string
	Provider    string
	APIKey      string
	InputTokens int
	// Completion fields set after response
	OutputTokens    int
	Status         CompletionStatus
	Error          string
	FinishReason   string
	Timestamp      time.Time
	PriceCharged  float64
	RefundAmount   float64
	IsInsured      bool
	RefundReason   string
}

// ZeroCompletionInsurance tracks requests and provides refunds for failed completions
type ZeroCompletionInsurance struct {
	mu           sync.RWMutex
	records      map[string]*RequestRecord
	refundTotal  float64
	requestCount int64
	enabled      bool
	// Configuration
	refundZeroTokens    bool
	refundErrors        bool
	refundFiltered      bool
	filterErrorPatterns []string
}

// NewZeroCompletionInsurance creates a new insurance service
func NewZeroCompletionInsurance() *ZeroCompletionInsurance {
	return &ZeroCompletionInsurance{
		records:            make(map[string]*RequestRecord),
		enabled:            true,
		refundZeroTokens:  true,
		refundErrors:      true,
		refundFiltered:    false,
		filterErrorPatterns: []string{
			"rate_limit",
			"quota_exceeded",
			"context_length_exceeded",
		},
	}
}

// StartRequest records the start of a request for insurance tracking
func (z *ZeroCompletionInsurance) StartRequest(ctx context.Context, reqID, modelID, provider, apiKey string, inputTokens int) *RequestRecord {
	z.mu.Lock()
	defer z.mu.Unlock()

	record := &RequestRecord{
		RequestID:    reqID,
		ModelID:     modelID,
		Provider:    provider,
		APIKey:      apiKey,
		InputTokens: inputTokens,
		Timestamp:  time.Now(),
		IsInsured:   z.enabled,
	}

	z.records[reqID] = record
	z.requestCount++

	return record
}

// CompleteRequest records the completion of a request and determines if refund is needed
func (z *ZeroCompletionInsurance) CompleteRequest(ctx context.Context, reqID string, outputTokens int, finishReason, err string) (*RequestRecord, float64) {
	z.mu.Lock()
	defer z.mu.Unlock()

	record, ok := z.records[reqID]
	if !ok {
		return nil, 0
	}

	record.OutputTokens = outputTokens
	record.FinishReason = finishReason
	record.Timestamp = time.Now()

	// Determine status and refund
	record.Status, record.RefundAmount = z.determineRefund(outputTokens, finishReason, err)

	if err != "" {
		record.Error = err
	}

	// Process refund
	if record.RefundAmount > 0 && record.IsInsured {
		z.refundTotal += record.RefundAmount
		record.RefundReason = z.getRefundReason(record.Status, err)
	}

	return record, record.RefundAmount
}

// determineRefund calculates the refund amount based on completion status
func (z *ZeroCompletionInsurance) determineRefund(outputTokens int, finishReason, err string) (CompletionStatus, float64) {
	// Zero output tokens case
	if outputTokens == 0 {
		if z.refundZeroTokens {
			return StatusZeroTokens, 1.0 // Full refund
		}
		return StatusZeroTokens, 0
	}

	// Error case
	if err != "" {
		// Check if error is refundable
		for _, pattern := range z.filterErrorPatterns {
			if contains(err, pattern) {
				if z.refundErrors {
					return StatusError, 1.0 // Full refund
				}
				return StatusError, 0
			}
		}
		// Non-refundable errors
		return StatusError, 0
	}

	// Filtered content
	if contains(finishReason, "content_filter") || contains(finishReason, "filtered") {
		if z.refundFiltered {
			return StatusFiltered, 0.5 // 50% refund
		}
		return StatusFiltered, 0
	}

	return StatusSuccess, 0
}

// getRefundReason returns a human-readable reason for the refund
func (z *ZeroCompletionInsurance) getRefundReason(status CompletionStatus, err string) string {
	switch status {
	case StatusZeroTokens:
		return "Zero output tokens - covered by Zero Completion Insurance"
	case StatusError:
		if err != "" {
			return fmt.Sprintf("Error: %s - covered by Zero Completion Insurance", err)
		}
		return "Request error - covered by Zero Completion Insurance"
	case StatusFiltered:
		return "Content filtered - partial refund applied"
	default:
		return ""
	}
}

// contains is a simple string contains check
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// GetStats returns insurance statistics
func (z *ZeroCompletionInsurance) GetStats() InsuranceStats {
	z.mu.RLock()
	defer z.mu.RUnlock()

	var zeroTokenCount, errorCount, filteredCount, successCount int64
	var totalRefunded float64

	for _, r := range z.records {
		switch r.Status {
		case StatusZeroTokens:
			zeroTokenCount++
		case StatusError:
			errorCount++
		case StatusFiltered:
			filteredCount++
		case StatusSuccess:
			successCount++
		}
		totalRefunded += r.RefundAmount
	}

	return InsuranceStats{
		TotalRequests:      z.requestCount,
		SuccessCount:      successCount,
		ZeroTokenCount:    zeroTokenCount,
		ErrorCount:        errorCount,
		FilteredCount:     filteredCount,
		TotalRefunded:     totalRefunded,
		RefundPercent:     func() float64 { 
			if z.requestCount == 0 { return 0 }
			return float64(zeroTokenCount+errorCount) / float64(z.requestCount) * 100 
		}(),
	}
}

// InsuranceStats holds insurance statistics
type InsuranceStats struct {
	TotalRequests   int64   `json:"total_requests"`
	SuccessCount   int64   `json:"success_count"`
	ZeroTokenCount int64   `json:"zero_token_count"`
	ErrorCount     int64   `json:"error_count"`
	FilteredCount  int64   `json:"filtered_count"`
	TotalRefunded  float64 `json:"total_refunded"`
	RefundPercent  float64 `json:"refund_percent"`
}

// Enable enables or disables the insurance
func (z *ZeroCompletionInsurance) Enable(enabled bool) {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.enabled = enabled
}

// IsEnabled returns whether insurance is enabled
func (z *ZeroCompletionInsurance) IsEnabled() bool {
	z.mu.RLock()
	defer z.mu.RUnlock()
	return z.enabled
}
