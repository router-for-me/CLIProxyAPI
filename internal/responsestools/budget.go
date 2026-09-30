package responsestools

import (
	"encoding/json"
	"sync"
)

// Lease is one attempt's reservation against the shared limiter. It tracks
// billable bytes retained by this attempt only; it never claims to measure Go
// heap. Close is idempotent and releases the reservation exactly once.
type Lease struct {
	limiter   *Limiter
	bytes     int
	closed    bool
	mu        sync.Mutex
	onRelease func(bytes int)
}

// Limiter is the long-lived Manager-owned budget shared by all attempts.
// Hot config updates replace limits without resetting already-counted usage;
// lowering a quota never evicts active attempts, it only gates new growth.
type Limiter struct {
	mu       sync.Mutex
	limits   Limits
	attempts int
	bytes    int
}

// NewLimiter creates a shared limiter from route limits.
func NewLimiter(limits Limits) *Limiter {
	return &Limiter{limits: limits}
}

// UpdateLimits replaces the enforced limits while keeping current usage
// counters, so in-flight attempts are never silently evicted or reset.
func (l *Limiter) UpdateLimits(limits Limits) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.limits = limits
}

// Limits returns the currently enforced limits snapshot.
func (l *Limiter) Limits() Limits {
	if l == nil {
		return DefaultLimits()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.limits
}

// Usage returns current attempt and byte counters for diagnostics. Only route
// and generation identifiers, kind counts, declaration bytes, and used budget
// may be logged; arguments, grammars, prompts, and results must not.
func (l *Limiter) Usage() (attempts, bytes int) {
	if l == nil {
		return 0, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.attempts, l.bytes
}

// Acquire reserves one attempt slot plus its initial byte budget. New
// admissions fail with 429 when the shared capacity is full; no credential is
// cooled down for a capacity refusal.
func (l *Limiter) Acquire(initialBytes int) (*Lease, error) {
	if l == nil {
		return &Lease{}, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if initialBytes > l.limits.MaxAttemptBytes {
		return nil, budgetError(ReasonAttemptBudget, errAttemptBudgetExceeded())
	}
	if l.attempts+1 > l.limits.MaxActiveAttempts {
		return nil, capacityError(errSharedCapacityFull())
	}
	if l.bytes+initialBytes > l.limits.MaxStateBytes {
		return nil, capacityError(errSharedCapacityFull())
	}
	l.attempts++
	l.bytes += initialBytes
	return &Lease{limiter: l, bytes: initialBytes}, nil
}

// Grow reserves additional bytes for one attempt. Every buffering growth must
// be pre-reserved, including growth after a hot config update lowered quotas.
func (ls *Lease) Grow(delta int) error {
	if ls == nil {
		return nil
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if ls.closed || delta <= 0 {
		return nil
	}
	limiter := ls.limiter
	if limiter == nil {
		ls.bytes += delta
		return nil
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if ls.bytes+delta > limiter.limits.MaxAttemptBytes {
		return budgetError(ReasonAttemptBudget, errAttemptBudgetExceeded())
	}
	if limiter.bytes+delta > limiter.limits.MaxStateBytes {
		return capacityError(errSharedCapacityFull())
	}
	limiter.bytes += delta
	ls.bytes += delta
	return nil
}

// Shrink releases bytes that an attempt no longer retains. It is safe to call
// with an amount larger than the current reservation; the lease remains
// non-negative and Close still releases the remaining reservation once.
func (ls *Lease) Shrink(delta int) {
	if ls == nil || delta <= 0 {
		return
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if ls.closed || ls.bytes == 0 {
		return
	}
	if delta > ls.bytes {
		delta = ls.bytes
	}
	ls.bytes -= delta
	if ls.limiter != nil {
		ls.limiter.mu.Lock()
		ls.limiter.bytes -= delta
		if ls.limiter.bytes < 0 {
			ls.limiter.bytes = 0
		}
		ls.limiter.mu.Unlock()
	}
}

// Close releases the lease exactly once. It is idempotent: double close,
// cancellation, and completion all converge here safely.
func (ls *Lease) Close() {
	if ls == nil {
		return
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if ls.closed {
		return
	}
	ls.closed = true
	if ls.limiter != nil {
		ls.limiter.mu.Lock()
		ls.limiter.attempts--
		if ls.limiter.attempts < 0 {
			ls.limiter.attempts = 0
		}
		ls.limiter.bytes -= ls.bytes
		if ls.limiter.bytes < 0 {
			ls.limiter.bytes = 0
		}
		ls.limiter.mu.Unlock()
	}
	if ls.onRelease != nil {
		onRelease := ls.onRelease
		ls.onRelease = nil
		onRelease(ls.bytes)
	}
}

// Bytes returns the bytes currently charged to this lease.
func (ls *Lease) Bytes() int {
	if ls == nil {
		return 0
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return ls.bytes
}

// ContractSize estimates the billable bytes of one parsed contract from its
// identities and aliases with a fixed per-entry overhead. Empty payloads can
// never reserve unbounded state through this path.
func ContractSize(contract *ToolContract) int {
	if contract == nil {
		return 0
	}
	summary := struct {
		Identities []ToolIdentity    `json:"identities"`
		Aliases    map[string]string `json:"aliases"`
		Search     string            `json:"search"`
	}{
		Identities: contract.Identities,
		Aliases:    make(map[string]string, len(contract.IDByAlias)),
		Search:     contract.SearchAlias,
	}
	for alias := range contract.IDByAlias {
		summary.Aliases[alias] = ""
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		return 1 << 30
	}
	return len(encoded)
}

// CompactContract drops declaration payloads while keeping every identity,
// alias, and activation mapping the response path needs. Large tool schemas
// are released as soon as the request is constructed; only the compact
// identity view is retained for the attempt lifetime.
func CompactContract(contract *ToolContract) *ToolContract {
	if contract == nil {
		return nil
	}
	compact := NewToolContract()
	compact.Identities = append([]ToolIdentity(nil), contract.Identities...)
	compact.ClientSearch = contract.ClientSearch
	compact.SearchBridged = contract.SearchBridged
	compact.ServerSearch = contract.ServerSearch
	compact.SearchAlias = contract.SearchAlias
	compact.searchSyntheticSeen = contract.searchSyntheticSeen
	for path := range contract.SearchSyntheticNulls {
		compact.SearchSyntheticNulls[path] = struct{}{}
	}
	for identity, deferred := range contract.Deferred {
		compact.Deferred[identity] = deferred
	}
	for identity, alias := range contract.AliasByID {
		compact.AliasByID[identity] = alias
	}
	for alias, identity := range contract.IDByAlias {
		compact.IDByAlias[alias] = identity
	}
	for name, identity := range contract.Exact {
		compact.Exact[name] = identity
	}
	for name, identity := range contract.Normalized {
		compact.Normalized[name] = identity
	}
	for name, identity := range contract.Local {
		compact.Local[name] = identity
	}
	for name, identity := range contract.Namespaces {
		compact.Namespaces[name] = identity
	}
	for name, count := range contract.NamespaceCounts {
		compact.NamespaceCounts[name] = count
	}
	for name := range contract.TopLevel {
		compact.TopLevel[name] = struct{}{}
	}
	return compact
}
