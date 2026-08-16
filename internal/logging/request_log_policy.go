package logging

import (
	"strings"
	"sync"
)

// RequestLogPolicy owns privacy decisions independently of a concrete logger.
type RequestLogPolicy struct {
	mu         sync.RWMutex
	noLog      map[string]struct{}
	generation uint64
}

type RequestLogPolicyReload struct {
	policy     *RequestLogPolicy
	generation uint64
	previous   map[string]struct{}
	next       map[string]struct{}
	finished   bool
}

func NewRequestLogPolicy(keys []string) *RequestLogPolicy {
	policy := &RequestLogPolicy{}
	policy.SetNoLogAPIKeys(keys)
	return policy
}

func normalizeNoLogKeys(keys []string) map[string]struct{} {
	next := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if trimmed := strings.TrimSpace(key); trimmed != "" {
			next[trimmed] = struct{}{}
		}
	}
	return next
}

func cloneNoLogKeys(keys map[string]struct{}) map[string]struct{} {
	clone := make(map[string]struct{}, len(keys))
	for key := range keys {
		clone[key] = struct{}{}
	}
	return clone
}

func unionNoLogKeys(left, right map[string]struct{}) map[string]struct{} {
	result := cloneNoLogKeys(left)
	for key := range right {
		result[key] = struct{}{}
	}
	return result
}

func (p *RequestLogPolicy) SetNoLogAPIKeys(keys []string) {
	if p == nil {
		return
	}
	next := normalizeNoLogKeys(keys)
	p.mu.Lock()
	p.generation++
	p.noLog = next
	p.mu.Unlock()
}

// BeginReload protects the running and candidate sets until reload completion.
func (p *RequestLogPolicy) BeginReload(candidateKeys []string) *RequestLogPolicyReload {
	next := normalizeNoLogKeys(candidateKeys)
	if p == nil {
		return &RequestLogPolicyReload{next: next, finished: true}
	}
	p.mu.Lock()
	previous := cloneNoLogKeys(p.noLog)
	p.generation++
	generation := p.generation
	p.noLog = unionNoLogKeys(previous, next)
	p.mu.Unlock()

	return &RequestLogPolicyReload{
		policy:     p,
		generation: generation,
		previous:   previous,
		next:       next,
	}
}

func (r *RequestLogPolicyReload) Commit() bool {
	if r == nil || r.policy == nil {
		return false
	}
	r.policy.mu.Lock()
	defer r.policy.mu.Unlock()
	if r.finished {
		return false
	}
	r.finished = true
	if r.policy.generation != r.generation {
		return false
	}
	r.policy.noLog = cloneNoLogKeys(r.next)
	r.policy.generation++
	return true
}

func (r *RequestLogPolicyReload) Rollback() {
	if r == nil || r.policy == nil {
		return
	}
	r.policy.mu.Lock()
	defer r.policy.mu.Unlock()
	if r.finished {
		return
	}
	r.finished = true
	if r.policy.generation != r.generation {
		return
	}
	r.policy.noLog = cloneNoLogKeys(r.previous)
	r.policy.generation++
}

// FailClosed completes a reload without narrowing the already-published union.
func (r *RequestLogPolicyReload) FailClosed() {
	if r == nil || r.policy == nil {
		return
	}
	r.policy.mu.Lock()
	defer r.policy.mu.Unlock()
	if r.finished {
		return
	}
	r.finished = true
	if r.policy.generation != r.generation {
		return
	}
	r.policy.generation++
}

func (p *RequestLogPolicy) ShouldSkipLog(apiKey string) bool {
	if strings.TrimSpace(apiKey) == "" {
		return false
	}
	if p == nil {
		return true
	}
	p.mu.RLock()
	_, found := p.noLog[apiKey]
	p.mu.RUnlock()
	return found
}
