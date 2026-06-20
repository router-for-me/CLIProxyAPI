package main

import (
	"sort"
	"sync"
)

const (
	penaltyBumpOn429503 = 5
	penaltyDecaySuccess = 1
)

var backendPenalties = struct {
	sync.Mutex
	scores map[routeBackend]int
}{
	scores: make(map[routeBackend]int),
}

func recordBackendFailure(backend routeBackend) {
	backendPenalties.Lock()
	defer backendPenalties.Unlock()
	backendPenalties.scores[backend] += penaltyBumpOn429503
}

func recordBackendSuccess(backend routeBackend) {
	backendPenalties.Lock()
	defer backendPenalties.Unlock()
	score := backendPenalties.scores[backend] - penaltyDecaySuccess
	if score < 0 {
		score = 0
	}
	backendPenalties.scores[backend] = score
}

func penaltyScore(backend routeBackend) int {
	backendPenalties.Lock()
	defer backendPenalties.Unlock()
	return backendPenalties.scores[backend]
}

func sortBackendsByPenalty(backends []routeBackend) []routeBackend {
	if len(backends) <= 1 {
		return append([]routeBackend(nil), backends...)
	}
	out := append([]routeBackend(nil), backends...)
	sort.SliceStable(out, func(i, j int) bool {
		return penaltyScore(out[i]) < penaltyScore(out[j])
	})
	return out
}

func resetBackendPenaltiesForTest() {
	backendPenalties.Lock()
	defer backendPenalties.Unlock()
	backendPenalties.scores = make(map[routeBackend]int)
}
