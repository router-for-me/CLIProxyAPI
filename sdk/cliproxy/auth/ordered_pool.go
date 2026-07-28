package auth

import (
	"strings"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
)

// OrderedCandidate describes one step in an ordered candidate pool for a
// public alias. Pool position is significant: the conductor walks the chain
// in slice order and only advances on retryable pre-first-byte errors.
type OrderedCandidate struct {
	// Channel is the OAuth model-alias channel (vertex, claude, codex, ...).
	Channel string
	// UpstreamModel is the upstream model name this candidate maps to.
	UpstreamModel string
	// ConfigAlias is the client-visible alias that produced this candidate.
	ConfigAlias string
	// ForceMapping controls response rewriting for this candidate.
	ForceMapping bool
}

// orderedCandidatePool is the resolved chain for a single client-visible
// model within a single channel. It preserves config order and is the unit
// the conductor iterates when applying sequential failover.
type orderedCandidatePool struct {
	alias      string
	candidates []OrderedCandidate
}

// resolveOrderedCandidates resolves a client-visible requestedModel into an
// ordered candidate chain for the given channel. The chain preserves config
// order of repeated alias entries; an empty pool means no mapping exists and
// the caller should keep using requestedModel as-is.
//
// When the requested model carries a thinking suffix (e.g. "gpt-5(8192)"),
// the suffix is preserved on every resolved upstream model. If an upstream
// already carries its own suffix, that one wins for that candidate.
func (m *Manager) resolveOrderedCandidates(channel, requestedModel string) []OrderedCandidate {
	channel = strings.ToLower(strings.TrimSpace(channel))
	if channel == "" {
		return nil
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return nil
	}
	if m == nil {
		return nil
	}
	rawTable := m.oauthModelAlias.Load()
	table, _ := rawTable.(*oauthModelAliasTable)
	if table == nil || table.ordered == nil {
		return nil
	}
	orderedByAlias := table.ordered[channel]
	if len(orderedByAlias) == 0 {
		return nil
	}

	requestResult := thinking.ParseSuffix(requestedModel)
	candidates := []string{requestResult.ModelName}
	if requestResult.ModelName != requestedModel {
		candidates = append(candidates, requestedModel)
	}

	for _, requested := range candidates {
		key := strings.ToLower(strings.TrimSpace(requested))
		if key == "" {
			continue
		}
		entries, ok := orderedByAlias[key]
		if !ok || len(entries) == 0 {
			continue
		}
		out := make([]OrderedCandidate, 0, len(entries))
		for _, entry := range entries {
			upstream := resolveCandidateUpstreamModel(entry.upstreamModel, requestResult)
			if upstream == "" {
				continue
			}
			out = append(out, OrderedCandidate{
				Channel:       channel,
				UpstreamModel: upstream,
				ConfigAlias:   entry.configAlias,
				ForceMapping:  entry.forceMapping,
			})
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

func resolveCandidateUpstreamModel(upstream string, requestResult thinking.SuffixResult) string {
	upstream = strings.TrimSpace(upstream)
	if upstream == "" {
		return ""
	}
	if thinking.ParseSuffix(upstream).HasSuffix {
		return upstream
	}
	if requestResult.HasSuffix && requestResult.RawSuffix != "" {
		return upstream + "(" + requestResult.RawSuffix + ")"
	}
	return upstream
}

// compileOrderedCandidatesFromConfig is a pure helper for tests: it compiles a
// raw OAuthModelAlias map into an ordered candidate chain for one channel and
// requested model without requiring a Manager.
func compileOrderedCandidatesFromConfig(aliases map[string][]internalconfig.OAuthModelAlias, channel, requestedModel string) []OrderedCandidate {
	table := compileOAuthModelAliasTable(aliases)
	if table == nil || table.ordered == nil {
		return nil
	}
	channel = strings.ToLower(strings.TrimSpace(channel))
	if channel == "" {
		return nil
	}
	orderedByAlias := table.ordered[channel]
	if len(orderedByAlias) == 0 {
		return nil
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return nil
	}
	requestResult := thinking.ParseSuffix(requestedModel)
	candidates := []string{requestResult.ModelName}
	if requestResult.ModelName != requestedModel {
		candidates = append(candidates, requestedModel)
	}
	for _, requested := range candidates {
		key := strings.ToLower(strings.TrimSpace(requested))
		if key == "" {
			continue
		}
		entries, ok := orderedByAlias[key]
		if !ok || len(entries) == 0 {
			continue
		}
		out := make([]OrderedCandidate, 0, len(entries))
		for _, entry := range entries {
			upstream := resolveCandidateUpstreamModel(entry.upstreamModel, requestResult)
			if upstream == "" {
				continue
			}
			out = append(out, OrderedCandidate{
				Channel:       channel,
				UpstreamModel: upstream,
				ConfigAlias:   entry.configAlias,
				ForceMapping:  entry.forceMapping,
			})
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}
