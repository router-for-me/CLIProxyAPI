package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func rankedCandidateOptions(candidates ...cliproxyexecutor.AuthSelectionCandidate) cliproxyexecutor.Options {
	return cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.AuthSelectionCandidatesMetadataKey: candidates,
	}}
}

func newRankedCandidateTestManager(t *testing.T, selector Selector, auths ...*Auth) *Manager {
	t.Helper()
	manager := NewManager(nil, selector, nil)
	providers := make(map[string]struct{}, len(auths))
	for _, auth := range auths {
		if _, seen := providers[auth.Provider]; !seen {
			providers[auth.Provider] = struct{}{}
			manager.executors[auth.Provider] = schedulerTestExecutor{provider: auth.Provider}
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, errRegister)
		}
	}
	return manager
}

type rankedHomeTestDispatcher struct {
	mu    sync.Mutex
	calls int
}

func (d *rankedHomeTestDispatcher) HeartbeatOK() bool { return true }

func (d *rankedHomeTestDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	return nil, errors.New("ranked home dispatch must never happen")
}

func (d *rankedHomeTestDispatcher) RPopAuthWithPolicy(ctx context.Context, model string, sessionID string, headers http.Header, count int, policy string) ([]byte, error) {
	return d.RPopAuth(ctx, model, sessionID, headers, count)
}

func (*rankedHomeTestDispatcher) AbortAmbiguousDispatch() {}

func (d *rankedHomeTestDispatcher) dispatchCalls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func TestAuthSelectionCandidatesFromMetadata(t *testing.T) {
	tests := []struct {
		name      string
		meta      map[string]any
		wantSet   bool
		wantRanks []uint32
		wantErr   bool
	}{
		{
			name: "absent key keeps current behavior",
			meta: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: "auth-a"},
		},
		{
			name:    "untyped nil value is rejected",
			meta:    map[string]any{cliproxyexecutor.AuthSelectionCandidatesMetadataKey: nil},
			wantErr: true,
		},
		{
			name:    "foreign type is rejected",
			meta:    map[string]any{cliproxyexecutor.AuthSelectionCandidatesMetadataKey: []string{"auth-a"}},
			wantErr: true,
		},
		{
			name:    "empty list is rejected",
			meta:    map[string]any{cliproxyexecutor.AuthSelectionCandidatesMetadataKey: []cliproxyexecutor.AuthSelectionCandidate{}},
			wantErr: true,
		},
		{
			name: "blank auth id is rejected",
			meta: map[string]any{cliproxyexecutor.AuthSelectionCandidatesMetadataKey: []cliproxyexecutor.AuthSelectionCandidate{
				{AuthID: "  "},
			}},
			wantErr: true,
		},
		{
			name: "duplicate auth id is rejected",
			meta: map[string]any{cliproxyexecutor.AuthSelectionCandidatesMetadataKey: []cliproxyexecutor.AuthSelectionCandidate{
				{AuthID: "auth-a", StableOrder: 0},
				{AuthID: " auth-a ", StableOrder: 1},
			}},
			wantErr: true,
		},
		{
			name: "duplicate stable order inside one rank is rejected",
			meta: map[string]any{cliproxyexecutor.AuthSelectionCandidatesMetadataKey: []cliproxyexecutor.AuthSelectionCandidate{
				{AuthID: "auth-a", PriorityRank: 1, StableOrder: 7},
				{AuthID: "auth-b", PriorityRank: 1, StableOrder: 7},
			}},
			wantErr: true,
		},
		{
			name: "repeated stable order across ranks is accepted",
			meta: map[string]any{cliproxyexecutor.AuthSelectionCandidatesMetadataKey: []cliproxyexecutor.AuthSelectionCandidate{
				{AuthID: "auth-a", PriorityRank: 0, StableOrder: 7},
				{AuthID: "auth-b", PriorityRank: 1, StableOrder: 7},
			}},
			wantSet:   true,
			wantRanks: []uint32{0, 1},
		},
		{
			name: "ranks are normalized ascending",
			meta: map[string]any{cliproxyexecutor.AuthSelectionCandidatesMetadataKey: []cliproxyexecutor.AuthSelectionCandidate{
				{AuthID: "auth-c", PriorityRank: 5, StableOrder: 0},
				{AuthID: "auth-a", PriorityRank: 1, StableOrder: 0},
				{AuthID: "auth-b", PriorityRank: 1, StableOrder: 1},
			}},
			wantSet:   true,
			wantRanks: []uint32{1, 5},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set, errCandidates := authSelectionCandidatesFromMetadata(tt.meta)
			if tt.wantErr {
				var authErr *Error
				if !errors.As(errCandidates, &authErr) || authErr.Code != "invalid_auth_selection_candidates" {
					t.Fatalf("authSelectionCandidatesFromMetadata() error = %#v, want invalid_auth_selection_candidates", errCandidates)
				}
				if authErr.HTTPStatus != http.StatusBadRequest {
					t.Fatalf("authSelectionCandidatesFromMetadata() status = %d, want %d", authErr.HTTPStatus, http.StatusBadRequest)
				}
				if set != nil {
					t.Fatalf("authSelectionCandidatesFromMetadata() set = %#v, want nil", set)
				}
				return
			}
			if errCandidates != nil {
				t.Fatalf("authSelectionCandidatesFromMetadata() error = %v", errCandidates)
			}
			if !tt.wantSet {
				if set != nil {
					t.Fatalf("authSelectionCandidatesFromMetadata() set = %#v, want nil", set)
				}
				return
			}
			if set == nil {
				t.Fatal("authSelectionCandidatesFromMetadata() set = nil, want candidates")
			}
			if len(set.ranks) != len(tt.wantRanks) {
				t.Fatalf("ranks = %v, want %v", set.ranks, tt.wantRanks)
			}
			for index := range tt.wantRanks {
				if set.ranks[index] != tt.wantRanks[index] {
					t.Fatalf("ranks = %v, want %v", set.ranks, tt.wantRanks)
				}
			}
		})
	}
}

func TestRankedCandidateSetAllowsAtRank(t *testing.T) {
	set, errCandidates := authSelectionCandidatesFromMetadata(map[string]any{
		cliproxyexecutor.AuthSelectionCandidatesMetadataKey: []cliproxyexecutor.AuthSelectionCandidate{
			{AuthID: "auth-a", PriorityRank: 0, StableOrder: 0},
			{AuthID: "auth-b", PriorityRank: 1, StableOrder: 0},
		},
	})
	if errCandidates != nil {
		t.Fatalf("authSelectionCandidatesFromMetadata() error = %v", errCandidates)
	}

	tests := []struct {
		name       string
		authID     string
		rank       uint32
		rankActive bool
		want       bool
	}{
		{name: "listed auth without active rank", authID: "auth-b", want: true},
		{name: "unlisted auth without active rank", authID: "auth-z", want: false},
		{name: "listed auth inside active rank", authID: "auth-a", rank: 0, rankActive: true, want: true},
		{name: "listed auth outside active rank", authID: "auth-b", rank: 0, rankActive: true, want: false},
		{name: "unlisted auth inside active rank", authID: "auth-z", rank: 0, rankActive: true, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := set.allowsAtRank(tt.authID, tt.rank, tt.rankActive); got != tt.want {
				t.Fatalf("allowsAtRank(%q) = %v, want %v", tt.authID, got, tt.want)
			}
		})
	}

	var absent *rankedCandidateSet
	if !absent.allowsAtRank("anything", 0, true) {
		t.Fatal("nil candidate set must not restrict selection")
	}
}

func TestManagerPickNextRestrictsSelectionToRankedCandidates(t *testing.T) {
	manager := newRankedCandidateTestManager(t, &RoundRobinSelector{},
		&Auth{ID: "auth-a", Provider: "gemini"},
		&Auth{ID: "auth-b", Provider: "gemini"},
		&Auth{ID: "auth-c", Provider: "gemini"},
	)
	opts := rankedCandidateOptions(
		cliproxyexecutor.AuthSelectionCandidate{AuthID: "auth-a", PriorityRank: 0, StableOrder: 0},
		cliproxyexecutor.AuthSelectionCandidate{AuthID: "auth-c", PriorityRank: 0, StableOrder: 1},
	)

	counts := make(map[string]int)
	for index := 0; index < 30; index++ {
		selected, _, errPick := manager.pickNext(context.Background(), "gemini", "", opts, nil)
		if errPick != nil {
			t.Fatalf("pickNext() #%d error = %v", index, errPick)
		}
		counts[selected.ID]++
	}
	if counts["auth-b"] != 0 {
		t.Fatalf("unlisted auth selected %d times, want 0; all=%#v", counts["auth-b"], counts)
	}
	// Round-robin still runs unchanged inside the winning rank.
	if counts["auth-a"] != 15 || counts["auth-c"] != 15 {
		t.Fatalf("ranked picks = %#v, want auth-a:auth-c=15:15", counts)
	}

	// The filtered auth never consumed scheduler state, so unrestricted rotation stays even.
	unrestricted := make(map[string]int)
	for index := 0; index < 30; index++ {
		selected, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("unrestricted pickNext() #%d error = %v", index, errPick)
		}
		unrestricted[selected.ID]++
	}
	for _, authID := range []string{"auth-a", "auth-b", "auth-c"} {
		if unrestricted[authID] != 10 {
			t.Fatalf("unrestricted picks = %#v, want 10 each", unrestricted)
		}
	}
}

func TestManagerPickNextUsesLowestEligibleCandidateRank(t *testing.T) {
	tests := []struct {
		name             string
		lowestRankAuth   *Auth
		wantSelectedAuth string
	}{
		{
			name:             "lowest rank wins while it is eligible",
			lowestRankAuth:   &Auth{ID: "auth-low-rank", Provider: "gemini", Attributes: map[string]string{"priority": "0"}},
			wantSelectedAuth: "auth-low-rank",
		},
		{
			name:             "higher rank is used only once the lowest rank has nothing eligible",
			lowestRankAuth:   &Auth{ID: "auth-low-rank", Provider: "gemini", Attributes: map[string]string{"priority": "0"}, Disabled: true},
			wantSelectedAuth: "auth-high-rank",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The higher rank also carries the higher persistent priority, so a rank ladder that
			// leaked into persistent priority tiering would pick the wrong credential.
			manager := newRankedCandidateTestManager(t, &RoundRobinSelector{},
				&Auth{ID: "auth-high-rank", Provider: "gemini", Attributes: map[string]string{"priority": "10"}},
				tt.lowestRankAuth,
			)
			opts := rankedCandidateOptions(
				cliproxyexecutor.AuthSelectionCandidate{AuthID: "auth-high-rank", PriorityRank: 7, StableOrder: 0},
				cliproxyexecutor.AuthSelectionCandidate{AuthID: "auth-low-rank", PriorityRank: 1, StableOrder: 0},
			)

			selected, _, errPick := manager.pickNext(context.Background(), "gemini", "", opts, nil)
			if errPick != nil {
				t.Fatalf("pickNext() error = %v", errPick)
			}
			if selected == nil || selected.ID != tt.wantSelectedAuth {
				t.Fatalf("pickNext() auth = %#v, want %q", selected, tt.wantSelectedAuth)
			}

			manager.mu.RLock()
			defer manager.mu.RUnlock()
			for _, authID := range []string{"auth-high-rank", "auth-low-rank"} {
				want := "10"
				if authID == "auth-low-rank" {
					want = "0"
				}
				if got := manager.auths[authID].Attributes["priority"]; got != want {
					t.Fatalf("auth %q priority attribute = %q, want %q", authID, got, want)
				}
			}
		})
	}
}

func TestManagerRankedCandidatesFailClosedWhenNothingIsEligible(t *testing.T) {
	manager := newRankedCandidateTestManager(t, &RoundRobinSelector{},
		&Auth{ID: "auth-a", Provider: "gemini"},
		&Auth{ID: "auth-b", Provider: "gemini"},
	)
	opts := rankedCandidateOptions(cliproxyexecutor.AuthSelectionCandidate{AuthID: "auth-unknown"})

	selected, _, errPick := manager.pickNext(context.Background(), "gemini", "", opts, nil)
	if selected != nil {
		t.Fatalf("pickNext() auth = %#v, want nil", selected)
	}
	var authErr *Error
	if !errors.As(errPick, &authErr) || authErr.Code != "auth_not_found" {
		t.Fatalf("pickNext() error = %#v, want auth_not_found", errPick)
	}
}

func TestManagerPinnedAuthOutsideRankedCandidatesFailsClosed(t *testing.T) {
	manager := newRankedCandidateTestManager(t, &RoundRobinSelector{},
		&Auth{ID: "auth-a", Provider: "gemini"},
		&Auth{ID: "auth-b", Provider: "gemini"},
	)
	opts := rankedCandidateOptions(cliproxyexecutor.AuthSelectionCandidate{AuthID: "auth-a"})
	opts.Metadata[cliproxyexecutor.PinnedAuthMetadataKey] = "auth-b"

	selected, _, errPick := manager.pickNext(context.Background(), "gemini", "", opts, nil)
	if selected != nil {
		t.Fatalf("pickNext() auth = %#v, want nil", selected)
	}
	var authErr *Error
	if !errors.As(errPick, &authErr) || authErr.Code != "auth_not_found" {
		t.Fatalf("pickNext() error = %#v, want auth_not_found", errPick)
	}
}

func TestManagerPinnedAuthInsideRankedCandidatesIsSelected(t *testing.T) {
	manager := newRankedCandidateTestManager(t, &RoundRobinSelector{},
		&Auth{ID: "auth-a", Provider: "gemini"},
		&Auth{ID: "auth-b", Provider: "gemini"},
	)
	opts := rankedCandidateOptions(
		cliproxyexecutor.AuthSelectionCandidate{AuthID: "auth-a", PriorityRank: 0},
		cliproxyexecutor.AuthSelectionCandidate{AuthID: "auth-b", PriorityRank: 1},
	)
	opts.Metadata[cliproxyexecutor.PinnedAuthMetadataKey] = "auth-b"

	selected, _, errPick := manager.pickNext(context.Background(), "gemini", "", opts, nil)
	if errPick != nil {
		t.Fatalf("pickNext() error = %v", errPick)
	}
	if selected == nil || selected.ID != "auth-b" {
		t.Fatalf("pickNext() auth = %#v, want auth-b", selected)
	}
}

func TestManagerRankedCandidatesRejectInvalidMetadata(t *testing.T) {
	manager := newRankedCandidateTestManager(t, &RoundRobinSelector{},
		&Auth{ID: "auth-a", Provider: "gemini"},
	)
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.AuthSelectionCandidatesMetadataKey: []cliproxyexecutor.AuthSelectionCandidate{},
	}}

	tests := []struct {
		name string
		pick func() error
	}{
		{
			name: "single provider",
			pick: func() error {
				_, _, errPick := manager.pickNext(context.Background(), "gemini", "", opts, nil)
				return errPick
			},
		},
		{
			name: "mixed provider",
			pick: func() error {
				_, _, _, errPick := manager.pickNextMixed(context.Background(), []string{"gemini"}, "", opts, nil)
				return errPick
			},
		},
		{
			name: "public selection entry point",
			pick: func() error {
				_, errSelect := manager.SelectAuth(context.Background(), "gemini", "", opts)
				return errSelect
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var authErr *Error
			errPick := tt.pick()
			if !errors.As(errPick, &authErr) || authErr.Code != "invalid_auth_selection_candidates" {
				t.Fatalf("pick error = %#v, want invalid_auth_selection_candidates", errPick)
			}
			if authErr.HTTPStatus != http.StatusBadRequest {
				t.Fatalf("pick error status = %d, want %d", authErr.HTTPStatus, http.StatusBadRequest)
			}
		})
	}
}

func TestManagerLegacySelectionPresentsOnlyRankedCandidates(t *testing.T) {
	selector := &trackingSelector{}
	manager := newRankedCandidateTestManager(t, selector,
		&Auth{ID: "auth-a", Provider: "gemini"},
		&Auth{ID: "auth-b", Provider: "gemini"},
		&Auth{ID: "auth-c", Provider: "gemini"},
		&Auth{ID: "auth-unlisted", Provider: "gemini"},
	)
	opts := rankedCandidateOptions(
		cliproxyexecutor.AuthSelectionCandidate{AuthID: "auth-c", PriorityRank: 0, StableOrder: 1},
		cliproxyexecutor.AuthSelectionCandidate{AuthID: "auth-a", PriorityRank: 0, StableOrder: 0},
		cliproxyexecutor.AuthSelectionCandidate{AuthID: "auth-b", PriorityRank: 3, StableOrder: 0},
	)

	selected, _, errPick := manager.pickNextLegacy(context.Background(), "gemini", "", opts, nil)
	if errPick != nil {
		t.Fatalf("pickNextLegacy() error = %v", errPick)
	}
	if selector.calls != 1 {
		t.Fatalf("selector.calls = %d, want 1", selector.calls)
	}
	// The guarantee is set membership, not order: exactly the lowest-rank listed
	// credentials are presented. auth-b is listed but ranks higher, so its rank is
	// never reached; nothing unlisted may appear at all.
	if len(selector.lastAuthID) != 2 {
		t.Fatalf("selector candidates = %v, want exactly 2 candidates", selector.lastAuthID)
	}
	if got, want := authIDSet(selector.lastAuthID), map[string]struct{}{"auth-a": {}, "auth-c": {}}; !sameAuthIDSet(got, want) {
		t.Fatalf("selector candidates = %v, want exactly [auth-a auth-c] in any order", selector.lastAuthID)
	}
	for _, authID := range []string{"auth-b", "auth-unlisted"} {
		if _, presented := authIDSet(selector.lastAuthID)[authID]; presented {
			t.Fatalf("selector candidates = %v, want %q withheld", selector.lastAuthID, authID)
		}
	}
	if selected == nil {
		t.Fatal("pickNextLegacy() auth = nil, want a lowest-rank candidate")
	}
	if selected.ID != "auth-a" && selected.ID != "auth-c" {
		t.Fatalf("pickNextLegacy() auth = %q, want one of [auth-a auth-c]", selected.ID)
	}
}

// TestManagerRankedCandidatesStableOrderDoesNotAffectPresentedOrder pins the documented
// contract of AuthSelectionCandidate.StableOrder: it is an audit key, not a preference.
// Selection order inside a rank belongs to the configured scheduler, which observes its
// own ordering, so inverting StableOrder against auth ID order changes nothing. This is
// current, intended behavior - a future reader must not read it as a bug.
func TestManagerRankedCandidatesStableOrderDoesNotAffectPresentedOrder(t *testing.T) {
	tests := []struct {
		name       string
		candidates []cliproxyexecutor.AuthSelectionCandidate
	}{
		{
			name: "stable order agrees with auth id order",
			candidates: []cliproxyexecutor.AuthSelectionCandidate{
				{AuthID: "auth-a", PriorityRank: 0, StableOrder: 0},
				{AuthID: "auth-c", PriorityRank: 0, StableOrder: 1},
			},
		},
		{
			name: "stable order is inverted against auth id order",
			candidates: []cliproxyexecutor.AuthSelectionCandidate{
				{AuthID: "auth-c", PriorityRank: 0, StableOrder: 0},
				{AuthID: "auth-a", PriorityRank: 0, StableOrder: 1},
			},
		},
	}

	var presented [][]string
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selector := &trackingSelector{}
			manager := newRankedCandidateTestManager(t, selector,
				&Auth{ID: "auth-a", Provider: "gemini"},
				&Auth{ID: "auth-c", Provider: "gemini"},
			)

			if _, _, errPick := manager.pickNextLegacy(context.Background(), "gemini", "", rankedCandidateOptions(tt.candidates...), nil); errPick != nil {
				t.Fatalf("pickNextLegacy() error = %v", errPick)
			}
			got := append([]string(nil), selector.lastAuthID...)
			presented = append(presented, got)
			if len(got) != 2 {
				t.Fatalf("selector candidates = %v, want 2 candidates", got)
			}
		})
	}

	if len(presented) != 2 {
		t.Fatalf("presented candidate lists = %d, want 2", len(presented))
	}
	if !equalAuthIDs(presented[0], presented[1]) {
		t.Fatalf("presented order with inverted stable order = %v, want %v (StableOrder must not affect order)", presented[1], presented[0])
	}
}

// authIDSet collects auth IDs into a set for order-independent assertions.
func authIDSet(authIDs []string) map[string]struct{} {
	set := make(map[string]struct{}, len(authIDs))
	for _, authID := range authIDs {
		set[authID] = struct{}{}
	}
	return set
}

// sameAuthIDSet reports whether two auth ID sets hold exactly the same members.
func sameAuthIDSet(got, want map[string]struct{}) bool {
	if len(got) != len(want) {
		return false
	}
	for authID := range want {
		if _, present := got[authID]; !present {
			return false
		}
	}
	return true
}

// equalAuthIDs reports whether two auth ID slices match element for element.
func equalAuthIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func TestManagerMixedSelectionRestrictsToRankedCandidates(t *testing.T) {
	manager := newRankedCandidateTestManager(t, &RoundRobinSelector{},
		&Auth{ID: "gemini-a", Provider: "gemini"},
		&Auth{ID: "codex-a", Provider: "codex"},
		&Auth{ID: "codex-b", Provider: "codex"},
	)
	opts := rankedCandidateOptions(
		cliproxyexecutor.AuthSelectionCandidate{AuthID: "codex-b", PriorityRank: 0, StableOrder: 0},
		cliproxyexecutor.AuthSelectionCandidate{AuthID: "gemini-a", PriorityRank: 4, StableOrder: 0},
	)

	for index := 0; index < 10; index++ {
		selected, _, provider, errPick := manager.pickNextMixed(context.Background(), []string{"gemini", "codex"}, "", opts, nil)
		if errPick != nil {
			t.Fatalf("pickNextMixed() #%d error = %v", index, errPick)
		}
		if selected == nil || selected.ID != "codex-b" || provider != "codex" {
			t.Fatalf("pickNextMixed() = %#v provider %q, want codex-b on codex", selected, provider)
		}
	}
}

func TestManagerMixedLegacySelectionRestrictsToRankedCandidates(t *testing.T) {
	selector := &trackingSelector{}
	manager := newRankedCandidateTestManager(t, selector,
		&Auth{ID: "gemini-a", Provider: "gemini"},
		&Auth{ID: "codex-a", Provider: "codex"},
		&Auth{ID: "codex-b", Provider: "codex"},
	)
	opts := rankedCandidateOptions(
		cliproxyexecutor.AuthSelectionCandidate{AuthID: "codex-a", PriorityRank: 2, StableOrder: 0},
		cliproxyexecutor.AuthSelectionCandidate{AuthID: "gemini-a", PriorityRank: 2, StableOrder: 1},
		cliproxyexecutor.AuthSelectionCandidate{AuthID: "codex-b", PriorityRank: 9, StableOrder: 0},
	)

	selected, _, provider, errPick := manager.pickNextMixedLegacy(context.Background(), []string{"gemini", "codex"}, "", opts, nil)
	if errPick != nil {
		t.Fatalf("pickNextMixedLegacy() error = %v", errPick)
	}
	if len(selector.lastAuthID) != 2 || selector.lastAuthID[0] != "codex-a" || selector.lastAuthID[1] != "gemini-a" {
		t.Fatalf("selector candidates = %v, want [codex-a gemini-a] in stable order", selector.lastAuthID)
	}
	if selected == nil || selected.ID != "gemini-a" || provider != "gemini" {
		t.Fatalf("pickNextMixedLegacy() = %#v provider %q, want gemini-a on gemini", selected, provider)
	}
}

func TestManagerPluginSchedulerReceivesOnlyRankedCandidates(t *testing.T) {
	tests := []struct {
		name string
		resp pluginapi.SchedulerPickResponse
		want string
	}{
		{
			name: "plugin picks inside the winning rank",
			resp: pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-a"},
			want: "auth-a",
		},
		{
			name: "plugin delegates back to the built-in scheduler",
			resp: pluginapi.SchedulerPickResponse{Handled: true, DelegateBuiltin: pluginapi.SchedulerBuiltinRoundRobin},
			want: "auth-a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := newRankedCandidateTestManager(t, &RoundRobinSelector{},
				&Auth{ID: "auth-a", Provider: "gemini"},
				&Auth{ID: "auth-b", Provider: "gemini"},
				&Auth{ID: "auth-c", Provider: "gemini"},
			)
			scheduler := &fakePluginScheduler{resp: tt.resp, handled: true}
			manager.SetPluginScheduler(scheduler)
			opts := rankedCandidateOptions(
				cliproxyexecutor.AuthSelectionCandidate{AuthID: "auth-a", PriorityRank: 0, StableOrder: 0},
				cliproxyexecutor.AuthSelectionCandidate{AuthID: "auth-c", PriorityRank: 6, StableOrder: 0},
			)

			selected, _, errPick := manager.pickNext(context.Background(), "gemini", "", opts, nil)
			if errPick != nil {
				t.Fatalf("pickNext() error = %v", errPick)
			}
			if selected == nil || selected.ID != tt.want {
				t.Fatalf("pickNext() auth = %#v, want %q", selected, tt.want)
			}
			if len(scheduler.requests) != 1 {
				t.Fatalf("len(scheduler.requests) = %d, want 1", len(scheduler.requests))
			}
			if len(scheduler.requests[0].Candidates) != 1 || scheduler.requests[0].Candidates[0].ID != "auth-a" {
				t.Fatalf("plugin candidates = %#v, want only auth-a", scheduler.requests[0].Candidates)
			}
		})
	}
}

func TestManagerRankedCandidatesComposeWithAuthKindAndPolicy(t *testing.T) {
	newManager := func(t *testing.T) *Manager {
		t.Helper()
		return newRankedCandidateTestManager(t, &RoundRobinSelector{},
			&Auth{ID: "codex-api-key", Provider: "codex", Attributes: map[string]string{AttributeAPIKey: "test-key"}},
			&Auth{ID: "codex-oauth", Provider: "codex", Metadata: map[string]any{"access_token": "test-token"}},
		)
	}

	t.Run("candidate list narrows the required kind", func(t *testing.T) {
		manager := newManager(t)
		opts := rankedCandidateOptions(
			cliproxyexecutor.AuthSelectionCandidate{AuthID: "codex-api-key", PriorityRank: 0, StableOrder: 0},
			cliproxyexecutor.AuthSelectionCandidate{AuthID: "codex-oauth", PriorityRank: 0, StableOrder: 1},
		)
		selected, errSelect := manager.SelectAuthByKind(context.Background(), "codex", "", AuthKindOAuth, opts)
		if errSelect != nil {
			t.Fatalf("SelectAuthByKind() error = %v", errSelect)
		}
		if selected == nil || selected.ID != "codex-oauth" {
			t.Fatalf("SelectAuthByKind() auth = %#v, want codex-oauth", selected)
		}
	})

	t.Run("required kind cannot escape the candidate list", func(t *testing.T) {
		manager := newManager(t)
		opts := rankedCandidateOptions(cliproxyexecutor.AuthSelectionCandidate{AuthID: "codex-api-key"})
		selected, errSelect := manager.SelectAuthByKind(context.Background(), "codex", "", AuthKindOAuth, opts)
		if selected != nil {
			t.Fatalf("SelectAuthByKind() auth = %#v, want nil", selected)
		}
		var authErr *Error
		if !errors.As(errSelect, &authErr) || authErr.Code != "auth_not_found" {
			t.Fatalf("SelectAuthByKind() error = %#v, want auth_not_found", errSelect)
		}
	})

	t.Run("credential policy cannot escape the candidate list", func(t *testing.T) {
		manager := newManager(t)
		opts := rankedCandidateOptions(cliproxyexecutor.AuthSelectionCandidate{AuthID: "codex-api-key"})
		selected, errSelect := manager.SelectAuthWithCredentialPolicy(context.Background(), "codex", "", CredentialPolicyCodexAlphaSearchV1, opts)
		if selected != nil {
			t.Fatalf("SelectAuthWithCredentialPolicy() auth = %#v, want nil", selected)
		}
		var authErr *Error
		if !errors.As(errSelect, &authErr) || authErr.Code != "auth_not_found" {
			t.Fatalf("SelectAuthWithCredentialPolicy() error = %#v, want auth_not_found", errSelect)
		}
	})
}

func TestManagerSelectionWithoutCandidateMetadataIsUnchanged(t *testing.T) {
	t.Run("built-in scheduler rotation", func(t *testing.T) {
		manager := newRankedCandidateTestManager(t, &RoundRobinSelector{},
			&Auth{ID: "auth-a", Provider: "gemini"},
			&Auth{ID: "auth-b", Provider: "gemini"},
			&Auth{ID: "auth-c", Provider: "gemini"},
		)
		counts := make(map[string]int)
		for index := 0; index < 30; index++ {
			selected, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
			if errPick != nil {
				t.Fatalf("pickNext() #%d error = %v", index, errPick)
			}
			counts[selected.ID]++
		}
		for _, authID := range []string{"auth-a", "auth-b", "auth-c"} {
			if counts[authID] != 10 {
				t.Fatalf("picks = %#v, want 10 each", counts)
			}
		}
	})

	t.Run("legacy selector still sees every candidate", func(t *testing.T) {
		selector := &trackingSelector{}
		manager := newRankedCandidateTestManager(t, selector,
			&Auth{ID: "auth-a", Provider: "gemini"},
			&Auth{ID: "auth-b", Provider: "gemini"},
			&Auth{ID: "auth-c", Provider: "gemini"},
		)
		if _, _, errPick := manager.pickNextLegacy(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil); errPick != nil {
			t.Fatalf("pickNextLegacy() error = %v", errPick)
		}
		if len(selector.lastAuthID) != 3 {
			t.Fatalf("selector candidates = %v, want all three auths", selector.lastAuthID)
		}
	})
}

func TestFindAllAntigravityCreditsCandidateAuthsHonoursRankedCandidates(t *testing.T) {
	manager := &Manager{
		auths: map[string]*Auth{
			"ranked-credits-a": {ID: "ranked-credits-a", Provider: "antigravity"},
			"ranked-credits-b": {ID: "ranked-credits-b", Provider: "antigravity"},
			"ranked-credits-c": {ID: "ranked-credits-c", Provider: "antigravity"},
		},
		executors: map[string]ProviderExecutor{
			"antigravity": schedulerTestExecutor{},
		},
	}

	opts := rankedCandidateOptions(
		cliproxyexecutor.AuthSelectionCandidate{AuthID: "ranked-credits-b", PriorityRank: 0, StableOrder: 0},
		cliproxyexecutor.AuthSelectionCandidate{AuthID: "ranked-credits-c", PriorityRank: 2, StableOrder: 0},
	)
	candidates, errCandidates := manager.findAllAntigravityCreditsCandidateAuths(context.Background(), "claude-sonnet-4-6", opts)
	if errCandidates != nil {
		t.Fatalf("findAllAntigravityCreditsCandidateAuths() error = %v", errCandidates)
	}
	if len(candidates) != 1 || candidates[0].auth.ID != "ranked-credits-b" {
		t.Fatalf("candidates = %#v, want only ranked-credits-b", candidates)
	}

	invalidOpts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.AuthSelectionCandidatesMetadataKey: "ranked-credits-b",
	}}
	if _, errInvalid := manager.findAllAntigravityCreditsCandidateAuths(context.Background(), "claude-sonnet-4-6", invalidOpts); errInvalid == nil {
		t.Fatal("findAllAntigravityCreditsCandidateAuths(invalid) error = nil, want invalid_auth_selection_candidates")
	}
}

func TestFindAllAntigravityCreditsCandidateAuths_WalksPastExhaustedLowerRank(t *testing.T) {
	manager := &Manager{
		auths: map[string]*Auth{
			"ranked-credits-exhausted": {ID: "ranked-credits-exhausted", Provider: "antigravity"},
			"ranked-credits-available": {ID: "ranked-credits-available", Provider: "antigravity"},
		},
		executors: map[string]ProviderExecutor{
			"antigravity": schedulerTestExecutor{},
		},
	}
	SetAntigravityCreditsHint("ranked-credits-exhausted", AntigravityCreditsHint{Known: true, Available: false, UpdatedAt: time.Now()})
	SetAntigravityCreditsHint("ranked-credits-available", AntigravityCreditsHint{Known: true, Available: true, UpdatedAt: time.Now()})

	opts := rankedCandidateOptions(
		cliproxyexecutor.AuthSelectionCandidate{AuthID: "ranked-credits-exhausted", PriorityRank: 0, StableOrder: 0},
		cliproxyexecutor.AuthSelectionCandidate{AuthID: "ranked-credits-available", PriorityRank: 1, StableOrder: 0},
	)
	candidates, errCandidates := manager.findAllAntigravityCreditsCandidateAuths(context.Background(), "claude-sonnet-4-6", opts)
	if errCandidates != nil {
		t.Fatalf("findAllAntigravityCreditsCandidateAuths() error = %v", errCandidates)
	}
	if len(candidates) != 1 || candidates[0].auth.ID != "ranked-credits-available" {
		t.Fatalf("candidates = %#v, want available candidate from next rank", candidates)
	}
}

func TestManagerHomeRejectsRankedCandidatesBeforeDispatch(t *testing.T) {
	dispatcher := &rankedHomeTestDispatcher{}
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{provider: "gemini"}
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)

	opts := rankedCandidateOptions(cliproxyexecutor.AuthSelectionCandidate{AuthID: "auth-a"})

	tests := []struct {
		name   string
		invoke func() error
	}{
		{
			name: "home selection funnel",
			invoke: func() error {
				_, _, _, errPick := manager.pickNextViaHome(context.Background(), "", opts, nil)
				return errPick
			},
		},
		{
			name: "home dispatch selection",
			invoke: func() error {
				_, errSelection := manager.pickHomeDispatchSelection(context.Background(), "", opts)
				return errSelection
			},
		},
		{
			name: "select home auth by kind",
			invoke: func() error {
				_, errSelect := manager.SelectHomeAuthByKind(context.Background(), "gemini", "", AuthKindOAuth, opts)
				return errSelect
			},
		},
		{
			name: "select home auth with credential policy",
			invoke: func() error {
				_, errSelect := manager.SelectHomeAuthWithCredentialPolicy(context.Background(), "gemini", "", CredentialPolicyCodexAlphaSearchV1, opts)
				return errSelect
			},
		},
		{
			name: "home execution",
			invoke: func() error {
				_, errExecute := manager.Execute(context.Background(), []string{"gemini"}, cliproxyexecutor.Request{Model: "test"}, opts)
				return errExecute
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var authErr *Error
			errInvoke := tt.invoke()
			if !errors.As(errInvoke, &authErr) || authErr.Code != "ranked_candidates_unsupported_in_home" {
				t.Fatalf("error = %#v, want ranked_candidates_unsupported_in_home", errInvoke)
			}
			if authErr.HTTPStatus != http.StatusServiceUnavailable {
				t.Fatalf("error status = %d, want %d", authErr.HTTPStatus, http.StatusServiceUnavailable)
			}
		})
	}

	if calls := dispatcher.dispatchCalls(); calls != 0 {
		t.Fatalf("home dispatch calls = %d, want 0", calls)
	}
}

func TestManagerRankedCandidatesAreIsolatedBetweenConcurrentRequests(t *testing.T) {
	manager := newRankedCandidateTestManager(t, &RoundRobinSelector{},
		&Auth{ID: "auth-a", Provider: "gemini"},
		&Auth{ID: "auth-b", Provider: "gemini"},
		&Auth{ID: "auth-c", Provider: "gemini"},
	)

	var waitGroup sync.WaitGroup
	errCh := make(chan error, 64)
	for worker := 0; worker < 8; worker++ {
		waitGroup.Add(1)
		ranked := worker%2 == 0
		go func() {
			defer waitGroup.Done()
			opts := cliproxyexecutor.Options{}
			if ranked {
				opts = rankedCandidateOptions(cliproxyexecutor.AuthSelectionCandidate{AuthID: "auth-a", PriorityRank: 0, StableOrder: 0})
			}
			for index := 0; index < 50; index++ {
				selected, _, errPick := manager.pickNext(context.Background(), "gemini", "", opts, nil)
				if errPick != nil {
					errCh <- errPick
					return
				}
				if ranked && selected.ID != "auth-a" {
					errCh <- errors.New("ranked request selected " + selected.ID)
					return
				}
			}
		}()
	}
	waitGroup.Wait()
	close(errCh)
	for errWorker := range errCh {
		t.Fatalf("concurrent selection error = %v", errWorker)
	}
}
