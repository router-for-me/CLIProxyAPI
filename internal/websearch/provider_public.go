package websearch

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// publicEngineOrder lists the credential-free engines the public aggregate
// fans out to. The order is fixed so ranking is deterministic.
var publicEngineOrder = []string{
	ProviderStartpage,
	ProviderGoogle,
	ProviderDuckDuckGo,
	ProviderEcosia,
	ProviderMojeek,
}

// publicProvider aggregates the credential-free engines into one ranked
// result set. It is explicit-only: the automatic chain never selects it,
// matching OMP where the aggregate fans out on demand.
type publicProvider struct{}

func (publicProvider) ID() string    { return ProviderPublic }
func (publicProvider) Label() string { return "Public Web" }

func (publicProvider) Available(_ Config, explicit bool) bool { return explicit }

func (publicProvider) Search(ctx context.Context, cfg Config, req SearchRequest, parsed ParsedQuery) (SearchResponse, error) {
	return runPublicFanout(ctx, cfg, req, parsed)
}

// publicResult is one engine's settled outcome.
type publicResult struct {
	engine   string
	response SearchResponse
	err      error
}

// publicEngines resolves the fan-out set minus configured exclusions.
func publicEngines(cfg Config) []Provider {
	engines := make([]Provider, 0, len(publicEngineOrder))
	for _, id := range publicEngineOrder {
		if cfg.Excluded(id) {
			continue
		}
		if provider := lookupProvider(id); provider != nil {
			engines = append(engines, provider)
		}
	}
	return engines
}

// runPublicFanout races every engine under a deadline and consolidates the
// survivors. It returns at the earliest of: all engines settled, the soft
// deadline with at least one success, or the hard cap. Stragglers are
// abandoned through the context.
func runPublicFanout(ctx context.Context, cfg Config, req SearchRequest, parsed ParsedQuery) (SearchResponse, error) {
	engines := publicEngines(cfg)
	if len(engines) == 0 {
		return SearchResponse{}, &ProviderError{Provider: "Public Web", Message: "no public engines available", Status: 204}
	}
	engineTimeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	hardCtx, cancelHard := context.WithTimeout(ctx, time.Duration(cfg.PublicFanoutHardSeconds)*time.Second)
	defer cancelHard()
	softDeadline := time.NewTimer(time.Duration(cfg.PublicFanoutSoftSeconds) * time.Second)
	defer softDeadline.Stop()

	results := make(chan publicResult, len(engines))
	var wg sync.WaitGroup
	for _, engine := range engines {
		wg.Add(1)
		go func(engine Provider) {
			defer wg.Done()
			engineCtx, cancel := context.WithTimeout(hardCtx, engineTimeout)
			defer cancel()
			response, errSearch := engine.Search(engineCtx, cfg, req, parsed)
			if errSearch == nil && !response.HasRenderableContent() {
				errSearch = &ProviderError{Provider: engine.Label(), Message: "no renderable results", Status: 204}
			}
			response.Provider = engine.ID()
			results <- publicResult{engine: engine.ID(), response: response, err: errSearch}
		}(engine)
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	settled := make([]publicResult, 0, len(engines))
	var succeeded bool
	for settledCount := 0; settledCount < len(engines); {
		select {
		case result, ok := <-results:
			if !ok {
				return consolidatePublic(settled, cfg, req)
			}
			settled = append(settled, result)
			settledCount++
			if result.err == nil {
				succeeded = true
			}
		case <-softDeadline.C:
			if succeeded {
				return consolidatePublic(settled, cfg, req)
			}
		case <-hardCtx.Done():
			return consolidatePublic(settled, cfg, req)
		}
	}
	return consolidatePublic(settled, cfg, req)
}

// consolidatePublic dedupes, ranks, and clamps the surviving results. It
// fails only when every engine failed.
func consolidatePublic(settled []publicResult, cfg Config, req SearchRequest) (SearchResponse, error) {
	// Merge in engine-priority order, not settlement order, so the ranking
	// tiebreaks stay deterministic for a given query.
	settled = append([]publicResult(nil), settled...)
	sort.SliceStable(settled, func(i, j int) bool {
		return publicEngineIndex(settled[i].engine) < publicEngineIndex(settled[j].engine)
	})

	type mergedEntry struct {
		source     Source
		engines    int
		bestRank   int
		snippetLen int
	}
	merged := map[string]*mergedEntry{}
	var order []string
	for _, result := range settled {
		if result.err != nil {
			continue
		}
		filtered, _ := ApplyQueryConstraints(result.response.Sources, ParseSearchQuery(req.Query))
		for rank, source := range filtered {
			key := canonicalURLKey(source.URL)
			existing, seen := merged[key]
			if !seen {
				// Seed snippetLen so a later, longer snippet still wins.
				merged[key] = &mergedEntry{source: source, engines: 1, bestRank: rank, snippetLen: len(source.Snippet)}
				order = append(order, key)
				continue
			}
			existing.engines++
			// A better-ranked engine also brings its own title and URL.
			if rank < existing.bestRank {
				existing.bestRank = rank
				existing.source.Title = source.Title
				existing.source.URL = source.URL
			}
			// Keep the most informative snippet regardless of which engine
			// ranked it best.
			if len(source.Snippet) > existing.snippetLen {
				existing.source.Snippet = source.Snippet
				existing.snippetLen = len(source.Snippet)
			}
			if existing.source.Published == "" {
				existing.source.Published = source.Published
			}
		}
	}
	if len(merged) == 0 {
		lastErr := "all public engines failed"
		if len(settled) > 0 && settled[len(settled)-1].err != nil {
			lastErr = settled[len(settled)-1].err.Error()
		}
		return SearchResponse{}, &ProviderError{Provider: "Public Web", Message: lastErr, Status: http.StatusServiceUnavailable}
	}
	ordered := make([]*mergedEntry, 0, len(order))
	for _, key := range order {
		ordered = append(ordered, merged[key])
	}
	// Rank by cross-engine consensus, then by best per-engine rank. The sort
	// is stable and `ordered` is built in first-seen order, so entries equal
	// on both keys keep their deterministic engine-priority ordering.
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].engines != ordered[j].engines {
			return ordered[i].engines > ordered[j].engines
		}
		return ordered[i].bestRank < ordered[j].bestRank
	})
	// The aggregate clamps the request's own result count to its wider
	// window: consensus needs breadth, so the default exceeds a single
	// engine's.
	count := clampCount(req.ResultCount(), 1, MaxPublicLimit, DefaultPublicLimit)
	sources := make([]Source, 0, min(len(ordered), count))
	for _, item := range ordered {
		if len(sources) >= count {
			break
		}
		sources = append(sources, item.source)
	}
	return SearchResponse{Sources: sources, AuthMode: "keyless"}, nil
}

// publicEngineIndex returns an engine's position in the fan-out order so
// the merge is deterministic.
func publicEngineIndex(engine string) int {
	for index, id := range publicEngineOrder {
		if id == engine {
			return index
		}
	}
	return len(publicEngineOrder)
}

// canonicalURLKey normalizes a URL for dedupe: case-normalized host
// without a leading www., path without a trailing slash, query
// preserved, fragment dropped. Engines disagree on exactly these
// variations for the same page, so a URL that will not parse is kept
// verbatim rather than dropped.
func canonicalURLKey(raw string) string {
	trimmed := strings.TrimSpace(raw)
	parsed, errParse := url.Parse(trimmed)
	if errParse != nil || parsed.Hostname() == "" {
		return trimmed
	}
	host := strings.ToLower(strings.TrimPrefix(parsed.Hostname(), "www."))
	// WHATWG URL normalizes an empty path to "/"; Go leaves it empty, so do
	// it here or `https://a.example` and `https://www.a.example/` would
	// produce different keys for the same page.
	path := parsed.Path
	if path == "" {
		path = "/"
	} else if len(path) > 1 && strings.HasSuffix(path, "/") {
		path = path[:len(path)-1]
	}
	return host + path + parsed.RawQuery
}
