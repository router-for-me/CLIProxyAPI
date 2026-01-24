// Package executor provides runtime execution components for CLI Proxy API.
package executor

import (
	"sync"
)

// ProxySelector implements proxy assignment for credentials with provider-based
// load balancing and credential affinity. It ensures:
// 1. Proxies are distributed evenly across credentials of the same provider
// 2. Once a credential is assigned a proxy, it always uses the same proxy (affinity)
// 3. Different providers maintain independent proxy distribution counts
type ProxySelector struct {
	// proxies is the list of available proxy URLs
	proxies []string
	// providerCounts tracks usage count per proxy for each provider
	// provider -> [count for proxy 0, count for proxy 1, ...]
	providerCounts map[string][]int
	// assignments maps authID to assigned proxy index for affinity
	assignments map[string]int
	// mu protects concurrent access to maps
	mu sync.RWMutex
}

// NewProxySelector creates a new ProxySelector with the given proxy list.
// The proxies slice is copied to prevent external mutation.
// Returns nil if proxies is nil.
func NewProxySelector(proxies []string) *ProxySelector {
	if proxies == nil {
		return &ProxySelector{
			proxies:        nil,
			providerCounts: make(map[string][]int),
			assignments:    make(map[string]int),
		}
	}
	// Copy the proxies slice to prevent external mutation
	copied := make([]string, len(proxies))
	copy(copied, proxies)
	return &ProxySelector{
		proxies:        copied,
		providerCounts: make(map[string][]int),
		assignments:    make(map[string]int),
	}
}

// AssignProxy assigns a proxy to the given credential (authID) for the specified provider.
// It implements the following algorithm:
// 1. If proxies list is empty, returns empty string
// 2. If authID already has an assigned proxy (affinity), returns that proxy
// 3. Otherwise, finds the proxy with minimum usage count for this provider
// 4. Assigns that proxy to the authID and increments the usage count
// 5. Returns the assigned proxy URL
//
// This method is thread-safe and can be called concurrently.
func (s *ProxySelector) AssignProxy(authID, provider string) string {
	if len(s.proxies) == 0 {
		return ""
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check for existing assignment (affinity)
	if idx, ok := s.assignments[authID]; ok {
		if idx >= 0 && idx < len(s.proxies) {
			return s.proxies[idx]
		}
	}

	// Get or initialize provider counts
	counts, ok := s.providerCounts[provider]
	if !ok {
		counts = make([]int, len(s.proxies))
		s.providerCounts[provider] = counts
	}

	// Find proxy with minimum usage for this provider
	minIdx := 0
	for i, c := range counts {
		if c < counts[minIdx] {
			minIdx = i
		}
	}

	// Assign and increment count
	s.assignments[authID] = minIdx
	counts[minIdx]++

	return s.proxies[minIdx]
}

// GetProxy returns the assigned proxy for the given authID if one exists.
// Returns the proxy URL and true if found, empty string and false otherwise.
// This method is thread-safe for concurrent reads.
func (s *ProxySelector) GetProxy(authID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if idx, ok := s.assignments[authID]; ok {
		if idx >= 0 && idx < len(s.proxies) {
			return s.proxies[idx], true
		}
	}
	return "", false
}

// Reset clears all assignments and provider counts.
// This method is thread-safe.
func (s *ProxySelector) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.assignments = make(map[string]int)
	s.providerCounts = make(map[string][]int)
}

// Len returns the number of available proxies.
func (s *ProxySelector) Len() int {
	return len(s.proxies)
}

// AssignmentCount returns the number of credentials currently assigned to proxies.
func (s *ProxySelector) AssignmentCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.assignments)
}
