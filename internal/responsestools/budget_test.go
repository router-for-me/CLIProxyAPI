package responsestools

import (
	"sync"
	"testing"
)

func TestLimiterAcquireAndRelease(t *testing.T) {
	limiter := NewLimiter(Limits{MaxActiveToolBytes: 1024, MaxActiveAttempts: 2, MaxStateBytes: 2048, MaxAttemptBytes: 1024, MaxSchemaExpansionBytes: 512, MaxSchemaExpansionNodes: 100, MaxDepth: 8})
	first, err := limiter.Acquire(100)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	second, err := limiter.Acquire(100)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := limiter.Acquire(100); err == nil {
		t.Fatalf("expected capacity refusal at max attempts")
	}
	first.Close()
	first.Close()
	if _, err := limiter.Acquire(100); err != nil {
		t.Fatalf("release must free a slot: %v", err)
	}
	second.Close()
	if attempts, _ := limiter.Usage(); attempts != 1 {
		t.Fatalf("expected 1 attempt, got %d", attempts)
	}
}

func TestLimiterHotUpdateKeepsUsage(t *testing.T) {
	limiter := NewLimiter(Limits{MaxActiveToolBytes: 1024, MaxActiveAttempts: 8, MaxStateBytes: 4096, MaxAttemptBytes: 2048, MaxSchemaExpansionBytes: 512, MaxSchemaExpansionNodes: 100, MaxDepth: 8})
	lease, err := limiter.Acquire(1024)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer lease.Close()
	lowered := DefaultLimits()
	lowered.MaxActiveAttempts = 8
	lowered.MaxStateBytes = 1025
	limiter.UpdateLimits(lowered)
	if _, bytes := limiter.Usage(); bytes != 1024 {
		t.Fatalf("usage reset by hot update: %d", bytes)
	}
	if err := lease.Grow(1); err != nil {
		t.Fatalf("existing lease must keep counting, got %v", err)
	}
	if err := lease.Grow(1024); err == nil {
		t.Fatalf("growth past lowered quota must fail")
	}
}

// The per-attempt ceiling bounds retained protocol state only, and the shared
// ceiling still throttles concurrent attempts. Caller payload is never charged
// to the lease, so a long conversation cannot consume either ceiling.
func TestLeaseCeilingsBoundProtocolState(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxAttemptBytes = 256
	limits.MaxStateBytes = 300
	limiter := NewLimiter(limits)
	first, err := limiter.Acquire(0)
	if err != nil {
		t.Fatalf("acquire first: %v", err)
	}
	defer first.Close()
	if errGrow := first.Grow(200); errGrow != nil {
		t.Fatalf("protocol state within the ceiling must be accepted: %v", errGrow)
	}
	if errState := first.Grow(100); errState == nil {
		t.Fatal("protocol state above max-attempt-bytes must still be rejected")
	} else if !IsRequestScopedError(errState) {
		t.Fatal("attempt budget refusal must stay request-scoped")
	}

	second, err := limiter.Acquire(0)
	if err != nil {
		t.Fatalf("acquire second: %v", err)
	}
	defer second.Close()
	if errShared := second.Grow(150); errShared == nil {
		t.Fatal("concurrent growth beyond the shared ceiling must be rejected")
	} else if !IsRequestScopedError(errShared) {
		t.Fatal("shared capacity refusal must stay request-scoped")
	}
}

func TestLeaseConcurrentClose(t *testing.T) {
	limiter := NewLimiter(DefaultLimits())
	lease, err := limiter.Acquire(64)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease.Close()
		}()
	}
	wg.Wait()
	if attempts, bytes := limiter.Usage(); attempts != 0 || bytes != 0 {
		t.Fatalf("double close leaked: attempts=%d bytes=%d", attempts, bytes)
	}
}

func TestLeaseShrinkReleasesReservedBytes(t *testing.T) {
	limiter := NewLimiter(DefaultLimits())
	lease, err := limiter.Acquire(32)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := lease.Grow(16); err != nil {
		t.Fatalf("grow: %v", err)
	}
	lease.Shrink(24)
	if got := lease.Bytes(); got != 24 {
		t.Fatalf("lease bytes = %d, want 24", got)
	}
	if attempts, bytes := limiter.Usage(); attempts != 1 || bytes != 24 {
		t.Fatalf("usage after shrink = (%d, %d), want (1, 24)", attempts, bytes)
	}
	lease.Shrink(100)
	if got := lease.Bytes(); got != 0 {
		t.Fatalf("lease bytes after over-shrink = %d, want 0", got)
	}
	if attempts, bytes := limiter.Usage(); attempts != 1 || bytes != 0 {
		t.Fatalf("usage after over-shrink = (%d, %d), want (1, 0)", attempts, bytes)
	}
	lease.Close()
	if attempts, bytes := limiter.Usage(); attempts != 0 || bytes != 0 {
		t.Fatalf("usage after close = (%d, %d), want (0, 0)", attempts, bytes)
	}
}

func TestValidateLimitsRejectsNonPositive(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxDepth = 0
	if err := ValidateLimits(limits); err == nil {
		t.Fatalf("expected rejection for zero limit")
	}
	limits = DefaultLimits()
	limits.MaxStateBytes = -1
	if err := ValidateLimits(limits); err == nil {
		t.Fatalf("negative must never mean unlimited")
	}
	if err := ValidateLimits(DefaultLimits()); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
}

func TestCompactContractDropsDeclarations(t *testing.T) {
	contract := ParseContract([]byte("{\"tools\": [{\"type\": \"tool_search\"}], \"input\": [{\"type\": \"tool_search_output\", \"call_id\": \"c\", \"tools\": [{\"type\": \"function\", \"name\": \"big\"}]}]}"))
	if contract == nil {
		t.Fatalf("no contract")
	}
	if len(contract.Declarations) == 0 {
		t.Fatalf("expected declarations")
	}
	compact := CompactContract(contract)
	if len(compact.Declarations) != 0 {
		t.Fatalf("compact must drop declaration payloads")
	}
	if len(compact.Identities) != len(contract.Identities) {
		t.Fatalf("compact must keep identities")
	}
	if _, ok := compact.Resolve("big"); !ok {
		t.Fatalf("compact must keep resolving")
	}
}
