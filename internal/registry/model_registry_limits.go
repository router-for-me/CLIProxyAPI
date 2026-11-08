package registry

// routeLimits holds the smallest positive token limits seen among the clients
// that serve one model.
type routeLimits struct {
	contextLength       int
	maxCompletionTokens int
	inputTokenLimit     int
	outputTokenLimit    int
}

func minPositive(current, value int) int {
	if value <= 0 {
		return current
	}
	if current <= 0 || value < current {
		return value
	}
	return current
}

func (l *routeLimits) add(info *ModelInfo) {
	if info == nil {
		return
	}
	l.contextLength = minPositive(l.contextLength, info.ContextLength)
	l.maxCompletionTokens = minPositive(l.maxCompletionTokens, info.MaxCompletionTokens)
	l.inputTokenLimit = minPositive(l.inputTokenLimit, info.InputTokenLimit)
	l.outputTokenLimit = minPositive(l.outputTokenLimit, info.OutputTokenLimit)
}

// apply writes the limits into info and reports whether anything changed. A
// limit no serving client declares becomes zero, so a removed client's value
// does not linger.
func (l routeLimits) apply(info *ModelInfo) bool {
	if info == nil {
		return false
	}
	changed := false
	set := func(field *int, value int) {
		if *field != value {
			*field = value
			changed = true
		}
	}
	set(&info.ContextLength, l.contextLength)
	set(&info.MaxCompletionTokens, l.maxCompletionTokens)
	set(&info.InputTokenLimit, l.inputTokenLimit)
	set(&info.OutputTokenLimit, l.outputTokenLimit)
	return changed
}

// reconcileModelLimitsLocked publishes, for each given model, the smallest token
// limits among the clients that currently serve it, overall and per provider.
// The selector can route a request to any of those clients, so the limits of
// whichever client registered last would overstate what the others accept when
// one alias covers routes with different windows. Callers must hold r.mutex.
func (r *ModelRegistry) reconcileModelLimitsLocked(modelIDs []string) {
	changed := false
	done := make(map[string]struct{}, len(modelIDs))
	for _, modelID := range modelIDs {
		if _, seen := done[modelID]; seen {
			continue
		}
		done[modelID] = struct{}{}
		registration := r.models[modelID]
		if registration == nil {
			continue
		}
		var all routeLimits
		served := false
		byProvider := make(map[string]*routeLimits)
		for clientID, infos := range r.clientModelInfos {
			info := infos[modelID]
			if info == nil {
				continue
			}
			served = true
			all.add(info)
			if provider := r.clientProviders[clientID]; provider != "" {
				limits := byProvider[provider]
				if limits == nil {
					limits = &routeLimits{}
					byProvider[provider] = limits
				}
				limits.add(info)
			}
		}
		if !served {
			continue
		}
		if all.apply(registration.Info) {
			changed = true
		}
		for provider, info := range registration.InfoByProvider {
			if limits := byProvider[provider]; limits != nil && limits.apply(info) {
				changed = true
			}
		}
	}
	if changed {
		r.invalidateAvailableModelsCacheLocked()
	}
}
