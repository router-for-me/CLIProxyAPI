package auth

import (
	"context"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// recentStartWindow is how far back the load signal of the least-used and p2c selectors looks.
const recentStartWindow = 60 * time.Second

const recentStartBuckets = int64(recentStartWindow / time.Second)

type recentStartBucket struct {
	sec int64
	n   int
}

// The counters live at package level so a selector swap on a config reload keeps them. Keys are bounded by the
// credentials and models in use.
var (
	recentStartsMu    sync.Mutex
	recentStartsByKey = map[string]*[recentStartBuckets]recentStartBucket{}
)

func recentStartKey(authID, model string) string { return authID + ":" + canonicalModelKey(model) }

// RecordStart counts one request started on authID for model.
func RecordStart(authID, model string) { recordStartAt(authID, model, time.Now()) }

func recordStartAt(authID, model string, at time.Time) {
	sec := at.Unix()
	key := recentStartKey(authID, model)
	recentStartsMu.Lock()
	defer recentStartsMu.Unlock()
	b := recentStartsByKey[key]
	if b == nil {
		b = new([recentStartBuckets]recentStartBucket)
		recentStartsByKey[key] = b
	}
	slot := &b[sec%recentStartBuckets]
	if slot.sec != sec {
		*slot = recentStartBucket{sec: sec}
	}
	slot.n++
}

// recentStarts is the number of requests started on authID for model in the recentStartWindow before now.
func recentStarts(authID, model string, now time.Time) int {
	sec := now.Unix()
	key := recentStartKey(authID, model)
	recentStartsMu.Lock()
	defer recentStartsMu.Unlock()
	b := recentStartsByKey[key]
	if b == nil {
		return 0
	}
	n := 0
	for _, slot := range b {
		if d := sec - slot.sec; d >= 0 && d < recentStartBuckets {
			n += slot.n
		}
	}
	return n
}

// spreadCandidates returns the top available tier as the builtin selectors see it.
func spreadCandidates(ctx context.Context, provider, model string, auths []*Auth, now time.Time) ([]*Auth, error) {
	available, err := getSelectorAvailableAuths(ctx, auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	return preferCodexWebsocketAuths(ctx, provider, available), nil
}

func intnOrDefault(intn func(int) int) func(int) int {
	if intn == nil {
		return rand.IntN
	}
	return intn
}

// RandomSelector picks uniformly among the top available tier.
type RandomSelector struct{ intn func(int) int }

// Pick selects a random available auth.
func (s *RandomSelector) Pick(ctx context.Context, provider, model string, _ cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	available, err := spreadCandidates(ctx, provider, model, auths, time.Now())
	if err != nil {
		return nil, err
	}
	return available[intnOrDefault(s.intn)(len(available))], nil
}

// LeastUsedSelector picks the available auth with the fewest requests started in the last minute; ties go to
// candidate (ID) order.
type LeastUsedSelector struct{}

// Pick selects the least used available auth.
func (s *LeastUsedSelector) Pick(ctx context.Context, provider, model string, _ cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	now := time.Now()
	available, err := spreadCandidates(ctx, provider, model, auths, now)
	if err != nil {
		return nil, err
	}
	best, bestN := 0, recentStarts(available[0].ID, model, now)
	for i := 1; i < len(available); i++ {
		if n := recentStarts(available[i].ID, model, now); n < bestN {
			best, bestN = i, n
		}
	}
	return available[best], nil
}

// P2CSelector draws two distinct available auths and picks the one with fewer requests started in the last
// minute; ties go to candidate (ID) order.
type P2CSelector struct{ intn func(int) int }

// Pick selects the less used of two random available auths.
func (s *P2CSelector) Pick(ctx context.Context, provider, model string, _ cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	now := time.Now()
	available, err := spreadCandidates(ctx, provider, model, auths, now)
	if err != nil {
		return nil, err
	}
	if len(available) == 1 {
		return available[0], nil
	}
	intn := intnOrDefault(s.intn)
	i, j := intn(len(available)), intn(len(available))
	for j == i {
		j = intn(len(available))
	}
	i, j = min(i, j), max(i, j)
	if recentStarts(available[j].ID, model, now) < recentStarts(available[i].ID, model, now) {
		return available[j], nil
	}
	return available[i], nil
}

// ProviderStrategySelector dispatches on the provider: ByProvider when it has an entry, Default otherwise. The
// manager picks with provider "mixed"; then the candidates' provider decides when they all share one.
type ProviderStrategySelector struct {
	Default    Selector
	ByProvider map[string]Selector
}

// Pick delegates to the provider's selector.
func (s *ProviderStrategySelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	return s.selectorFor(provider, auths).Pick(ctx, provider, model, opts, auths)
}

func (s *ProviderStrategySelector) selectorFor(provider string, auths []*Auth) Selector {
	key := canonicalSchedulingProvider(provider)
	if key == "mixed" {
		key = ""
		for i, a := range auths {
			p := canonicalSchedulingProvider(executorKeyFromAuth(a))
			if i > 0 && p != key {
				return s.Default
			}
			key = p
		}
	}
	if sel, ok := s.ByProvider[key]; ok && sel != nil {
		return sel
	}
	return s.Default
}

// RoutingStrategyName returns the canonical name of a routing strategy, accepting the historical aliases.
func RoutingStrategyName(name string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "round-robin", "roundrobin", "rr":
		return "round-robin", true
	case "weighted-round-robin", "weightedroundrobin", "wrr":
		return "weighted-round-robin", true
	case "fill-first", "fillfirst", "ff":
		return "fill-first", true
	case "random":
		return "random", true
	case "least-used", "leastused":
		return "least-used", true
	case "p2c":
		return "p2c", true
	}
	return "", false
}

// SelectorForStrategy returns a new selector for a routing strategy name.
func SelectorForStrategy(name string) (Selector, bool) {
	canonical, ok := RoutingStrategyName(name)
	if !ok {
		return nil, false
	}
	switch canonical {
	case "weighted-round-robin":
		return &WeightedRoundRobinSelector{}, true
	case "fill-first":
		return &FillFirstSelector{}, true
	case "random":
		return &RandomSelector{}, true
	case "least-used":
		return &LeastUsedSelector{}, true
	case "p2c":
		return &P2CSelector{}, true
	}
	return &RoundRobinSelector{}, true
}
