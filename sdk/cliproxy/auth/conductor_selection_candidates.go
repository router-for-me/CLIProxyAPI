package auth

import (
	"context"
	"net/http"
	"sort"
	"strings"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// activeSelectionRankContextKey carries the ranked candidate list and the rank currently attempted.
type activeSelectionRankContextKey struct{}

// activeSelectionRank is the request-scoped rank a selection attempt is restricted to.
type activeSelectionRank struct {
	candidates *rankedCandidateSet
	rank       uint32
}

// rankedCandidateSet is the normalized, request-scoped auth candidate allow list.
// It is derived from Options.Metadata for one selection call and is never persisted.
type rankedCandidateSet struct {
	ranksByAuthID map[string]uint32
	ranks         []uint32
}

// allows reports whether an auth ID is part of the request-scoped candidate list.
func (s *rankedCandidateSet) allows(authID string) bool {
	if s == nil {
		return true
	}
	_, listed := s.rankFor(authID)
	return listed
}

// allowsAtRank reports whether an auth ID is listed and, when a rank is active, belongs to it.
func (s *rankedCandidateSet) allowsAtRank(authID string, rank uint32, rankActive bool) bool {
	if s == nil {
		return true
	}
	candidateRank, listed := s.rankFor(authID)
	if !listed {
		return false
	}
	return !rankActive || candidateRank == rank
}

// rankFor returns the request-scoped rank of an auth ID.
func (s *rankedCandidateSet) rankFor(authID string) (uint32, bool) {
	if s == nil {
		return 0, false
	}
	rank, listed := s.ranksByAuthID[strings.TrimSpace(authID)]
	return rank, listed
}

// lowestRankPresent returns the lowest listed rank across the supplied auth IDs.
func (s *rankedCandidateSet) lowestRankPresent(authIDs []string) (uint32, bool) {
	if s == nil {
		return 0, false
	}
	lowest := uint32(0)
	found := false
	for _, authID := range authIDs {
		rank, listed := s.rankFor(authID)
		if !listed {
			continue
		}
		if !found || rank < lowest {
			lowest = rank
			found = true
		}
	}
	return lowest, found
}

// authSelectionCandidatesPresent reports whether a request carries ranked candidate metadata.
func authSelectionCandidatesPresent(meta map[string]any) bool {
	if len(meta) == 0 {
		return false
	}
	_, present := meta[cliproxyexecutor.AuthSelectionCandidatesMetadataKey]
	return present
}

// authSelectionCandidatesFromMetadata parses the request-scoped ranked candidate list.
// It returns a nil set when the metadata key is absent and rejects every malformed shape,
// so a caller can never silently widen selection past the credentials it listed.
func authSelectionCandidatesFromMetadata(meta map[string]any) (*rankedCandidateSet, error) {
	if !authSelectionCandidatesPresent(meta) {
		return nil, nil
	}
	raw := meta[cliproxyexecutor.AuthSelectionCandidatesMetadataKey]
	candidates, okType := raw.([]cliproxyexecutor.AuthSelectionCandidate)
	if !okType {
		return nil, &Error{Code: "invalid_auth_selection_candidates", Message: "auth selection candidates must be []executor.AuthSelectionCandidate", HTTPStatus: http.StatusBadRequest}
	}
	if len(candidates) == 0 {
		return nil, &Error{Code: "invalid_auth_selection_candidates", Message: "auth selection candidates must not be empty", HTTPStatus: http.StatusBadRequest}
	}
	set := &rankedCandidateSet{
		ranksByAuthID: make(map[string]uint32, len(candidates)),
	}
	ordersByRank := make(map[uint32]map[uint32]struct{}, len(candidates))
	for _, candidate := range candidates {
		authID := strings.TrimSpace(candidate.AuthID)
		if authID == "" {
			return nil, &Error{Code: "invalid_auth_selection_candidates", Message: "auth selection candidate id must not be empty", HTTPStatus: http.StatusBadRequest}
		}
		if _, duplicate := set.ranksByAuthID[authID]; duplicate {
			return nil, &Error{Code: "invalid_auth_selection_candidates", Message: "duplicate auth selection candidate id: " + authID, HTTPStatus: http.StatusBadRequest}
		}
		orders := ordersByRank[candidate.PriorityRank]
		if orders == nil {
			orders = make(map[uint32]struct{}, len(candidates))
			ordersByRank[candidate.PriorityRank] = orders
			set.ranks = append(set.ranks, candidate.PriorityRank)
		}
		// StableOrder never influences which credential is selected; it is validated
		// only so a caller can record an unambiguous, replayable candidate list.
		if _, duplicate := orders[candidate.StableOrder]; duplicate {
			return nil, &Error{Code: "invalid_auth_selection_candidates", Message: "duplicate auth selection candidate stable order in one rank: " + authID, HTTPStatus: http.StatusBadRequest}
		}
		orders[candidate.StableOrder] = struct{}{}
		set.ranksByAuthID[authID] = candidate.PriorityRank
	}
	sort.Slice(set.ranks, func(i, j int) bool { return set.ranks[i] < set.ranks[j] })
	return set, nil
}

// withActiveSelectionRank restricts a selection attempt to one request-scoped candidate rank.
// The rank travels on the context because scheduler and plugin paths re-derive eligibility
// from the context and the request options rather than from a caller supplied argument.
func withActiveSelectionRank(ctx context.Context, candidates *rankedCandidateSet, rank uint32) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, activeSelectionRankContextKey{}, activeSelectionRank{candidates: candidates, rank: rank})
}

// activeSelectionRankFromContext reports the candidate rank a selection attempt is restricted to.
func activeSelectionRankFromContext(ctx context.Context) (activeSelectionRank, bool) {
	if ctx == nil {
		return activeSelectionRank{}, false
	}
	active, ok := ctx.Value(activeSelectionRankContextKey{}).(activeSelectionRank)
	return active, ok
}

// rankedSelectionPlan reports the rank ladder a selection funnel must walk, lowest rank first.
// It returns an empty ladder when the request carries no candidate metadata and when the
// context already drives a ladder, so a funnel delegating to another funnel never loops twice.
func rankedSelectionPlan(ctx context.Context, opts cliproxyexecutor.Options) ([]uint32, *rankedCandidateSet, error) {
	if _, active := activeSelectionRankFromContext(ctx); active {
		return nil, nil, nil
	}
	set, errCandidates := authSelectionCandidatesFromMetadata(opts.Metadata)
	if errCandidates != nil {
		return nil, nil, errCandidates
	}
	if set == nil {
		return nil, nil, nil
	}
	return set.ranks, set, nil
}

// errRankedCandidatesUnsupportedInHome reports that Home dispatch cannot honour ranked candidates.
// Home selection happens remotely and exposes no local candidate filter, so a ranked request
// fails closed before any remote dispatch instead of silently selecting outside the list.
func errRankedCandidatesUnsupportedInHome() *Error {
	return &Error{Code: "ranked_candidates_unsupported_in_home", Message: "ranked auth selection candidates are unsupported while Home is enabled", HTTPStatus: http.StatusServiceUnavailable}
}

// homeRankedCandidateGuard fails closed when a Home dispatch carries ranked candidate metadata.
func homeRankedCandidateGuard(opts cliproxyexecutor.Options) error {
	if authSelectionCandidatesPresent(opts.Metadata) {
		return errRankedCandidatesUnsupportedInHome()
	}
	return nil
}

// runRankedSingleSelection runs a single-provider selection once per candidate rank, lowest first.
// Only the lowest rank that still yields an eligible auth is used; a rank is abandoned only when
// the configured scheduler reports that nothing in it is selectable.
func runRankedSingleSelection(ctx context.Context, opts cliproxyexecutor.Options, pick func(context.Context) (*Auth, ProviderExecutor, error)) (*Auth, ProviderExecutor, error) {
	ranks, set, errPlan := rankedSelectionPlan(ctx, opts)
	if errPlan != nil {
		return nil, nil, errPlan
	}
	if len(ranks) == 0 {
		return pick(ctx)
	}
	var errLast error
	for _, rank := range ranks {
		auth, executor, errPick := pick(withActiveSelectionRank(ctx, set, rank))
		if errPick == nil {
			return auth, executor, nil
		}
		errLast = errPick
		if shouldRetrySchedulerPick(errPick) {
			continue
		}
		return nil, nil, errPick
	}
	return nil, nil, errLast
}

// runRankedMixedSelection runs a mixed-provider selection once per candidate rank, lowest first.
func runRankedMixedSelection(ctx context.Context, opts cliproxyexecutor.Options, pick func(context.Context) (*Auth, ProviderExecutor, string, error)) (*Auth, ProviderExecutor, string, error) {
	ranks, set, errPlan := rankedSelectionPlan(ctx, opts)
	if errPlan != nil {
		return nil, nil, "", errPlan
	}
	if len(ranks) == 0 {
		return pick(ctx)
	}
	var errLast error
	for _, rank := range ranks {
		auth, executor, provider, errPick := pick(withActiveSelectionRank(ctx, set, rank))
		if errPick == nil {
			return auth, executor, provider, nil
		}
		errLast = errPick
		if shouldRetrySchedulerPick(errPick) {
			continue
		}
		return nil, nil, "", errPick
	}
	return nil, nil, "", errLast
}
