package registry

// When several clients publish the same model ID (for example one alias over a
// Claude route and a Gemini route), the selector can send a request to any of
// them, so the published limits must hold for every one. The registry keeps,
// per model and per model+provider, how many clients declare each distinct set
// of limits. Updating it costs O(models of the client) per registration, and
// publishing folds the distinct sets, of which a model has a handful.

// routeLimits is the token limits one client declares for one model.
type routeLimits struct {
	contextLength       int
	maxContextLength    int
	maxCompletionTokens int
	inputTokenLimit     int
	outputTokenLimit    int
}

func routeLimitsOf(info *ModelInfo) routeLimits {
	if info == nil {
		return routeLimits{}
	}
	return routeLimits{
		contextLength:       info.ContextLength,
		maxContextLength:    info.MaxContextLength,
		maxCompletionTokens: info.MaxCompletionTokens,
		inputTokenLimit:     info.InputTokenLimit,
		outputTokenLimit:    info.OutputTokenLimit,
	}
}

// countedRouteLimits records what a client contributed for one model.
type countedRouteLimits struct {
	provider string
	limits   routeLimits
}

// limitSet counts the clients behind each distinct routeLimits.
type limitSet map[routeLimits]int

func minPositive(current, value int) int {
	if value <= 0 {
		return current
	}
	if current <= 0 || value < current {
		return value
	}
	return current
}

// published returns the limits to publish for the clients in the set. One
// distinct set is published as declared. Otherwise every route is reduced to
// its effective window and output limit, whichever fields it uses (Gemini
// routes declare InputTokenLimit/OutputTokenLimit, others ContextLength/
// MaxCompletionTokens, configured overrides MaxContextLength), and the smallest
// of each is published in every field any route declares. When a route declares
// no window (or no output limit), its real limit is unknown and may be lower, so
// that limit is not published at all.
func (s limitSet) published() (routeLimits, bool) {
	if len(s) == 0 {
		return routeLimits{}, false
	}
	if len(s) == 1 {
		for limits := range s {
			return limits, true
		}
	}
	var window, output int
	windowUnknown, outputUnknown := false, false
	var declared routeLimits
	for limits := range s {
		routeWindow := minPositive(minPositive(limits.contextLength, limits.maxContextLength), limits.inputTokenLimit)
		routeOutput := minPositive(limits.maxCompletionTokens, limits.outputTokenLimit)
		windowUnknown = windowUnknown || routeWindow <= 0
		outputUnknown = outputUnknown || routeOutput <= 0
		window = minPositive(window, routeWindow)
		output = minPositive(output, routeOutput)
		declared.contextLength = minPositive(declared.contextLength, limits.contextLength)
		declared.maxContextLength = minPositive(declared.maxContextLength, limits.maxContextLength)
		declared.maxCompletionTokens = minPositive(declared.maxCompletionTokens, limits.maxCompletionTokens)
		declared.inputTokenLimit = minPositive(declared.inputTokenLimit, limits.inputTokenLimit)
		declared.outputTokenLimit = minPositive(declared.outputTokenLimit, limits.outputTokenLimit)
	}
	if windowUnknown {
		window = 0
	}
	if outputUnknown {
		output = 0
	}
	pick := func(declaredValue, value int) int {
		if declaredValue > 0 {
			return value
		}
		return 0
	}
	return routeLimits{
		contextLength:       pick(declared.contextLength, window),
		maxContextLength:    pick(declared.maxContextLength, window),
		maxCompletionTokens: pick(declared.maxCompletionTokens, output),
		inputTokenLimit:     pick(declared.inputTokenLimit, window),
		outputTokenLimit:    pick(declared.outputTokenLimit, output),
	}, true
}

// applyTo writes the limits into info and reports whether anything changed.
func (l routeLimits) applyTo(info *ModelInfo) bool {
	if info == nil || routeLimitsOf(info) == l {
		return false
	}
	info.ContextLength = l.contextLength
	info.MaxContextLength = l.maxContextLength
	info.MaxCompletionTokens = l.maxCompletionTokens
	info.InputTokenLimit = l.inputTokenLimit
	info.OutputTokenLimit = l.outputTokenLimit
	return true
}

func adjustLimitSet(sets map[string]limitSet, key string, limits routeLimits, delta int) {
	set := sets[key]
	if set == nil {
		if delta <= 0 {
			return
		}
		set = make(limitSet)
		sets[key] = set
	}
	set[limits] += delta
	if set[limits] <= 0 {
		delete(set, limits)
	}
	if len(set) == 0 {
		delete(sets, key)
	}
}

func (r *ModelRegistry) countRouteLimitsLocked(modelID string, counted countedRouteLimits, delta int) {
	adjustLimitSet(r.modelRouteLimits, modelID, counted.limits, delta)
	if counted.provider == "" {
		return
	}
	byProvider := r.providerRouteLimits[modelID]
	if byProvider == nil {
		if delta <= 0 {
			return
		}
		byProvider = make(map[string]limitSet)
		r.providerRouteLimits[modelID] = byProvider
	}
	adjustLimitSet(byProvider, counted.provider, counted.limits, delta)
	if len(byProvider) == 0 {
		delete(r.providerRouteLimits, modelID)
	}
}

// reconcileClientRouteLimitsLocked brings the counts in line with the client's
// current models and provider, then republishes the limits of every model the
// client served before or serves now. Registration rewrites registration.Info
// from the registering client, so republishing is needed even when the counts
// did not change. It is idempotent. Callers must hold r.mutex.
func (r *ModelRegistry) reconcileClientRouteLimitsLocked(clientID string) {
	if r.modelRouteLimits == nil {
		r.modelRouteLimits = make(map[string]limitSet)
	}
	if r.providerRouteLimits == nil {
		r.providerRouteLimits = make(map[string]map[string]limitSet)
	}
	if r.clientRouteLimits == nil {
		r.clientRouteLimits = make(map[string]map[string]countedRouteLimits)
	}

	counted := r.clientRouteLimits[clientID]
	current := r.clientModelInfos[clientID]
	provider := r.clientProviders[clientID]
	touched := make([]string, 0, len(counted)+len(current))
	for modelID, previous := range counted {
		touched = append(touched, modelID)
		if info := current[modelID]; info != nil && previous == (countedRouteLimits{provider: provider, limits: routeLimitsOf(info)}) {
			continue
		}
		r.countRouteLimitsLocked(modelID, previous, -1)
		delete(counted, modelID)
	}
	for modelID, info := range current {
		if info == nil {
			continue
		}
		if _, ok := counted[modelID]; ok {
			continue
		}
		touched = append(touched, modelID)
		entry := countedRouteLimits{provider: provider, limits: routeLimitsOf(info)}
		r.countRouteLimitsLocked(modelID, entry, 1)
		if counted == nil {
			counted = make(map[string]countedRouteLimits)
			r.clientRouteLimits[clientID] = counted
		}
		counted[modelID] = entry
	}
	if len(counted) == 0 {
		delete(r.clientRouteLimits, clientID)
	}

	changed := false
	for _, modelID := range touched {
		registration := r.models[modelID]
		if registration == nil {
			continue
		}
		if limits, ok := r.modelRouteLimits[modelID].published(); ok && limits.applyTo(registration.Info) {
			changed = true
		}
		for providerKey, info := range registration.InfoByProvider {
			if limits, ok := r.providerRouteLimits[modelID][providerKey].published(); ok && limits.applyTo(info) {
				changed = true
			}
		}
	}
	if changed {
		r.invalidateAvailableModelsCacheLocked()
	}
}
