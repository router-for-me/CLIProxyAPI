package usage

func cloneStatisticsSnapshot(src StatisticsSnapshot) StatisticsSnapshot {
	dst := StatisticsSnapshot{
		TotalRequests: src.TotalRequests,
		SuccessCount:  src.SuccessCount,
		FailureCount:  src.FailureCount,
		TotalTokens:   src.TotalTokens,
		APIs:          make(map[string]APISnapshot, len(src.APIs)),
		RequestsByDay: cloneStringInt64Map(src.RequestsByDay),
		RequestsByHour: cloneStringInt64Map(
			src.RequestsByHour,
		),
		TokensByDay: cloneStringInt64Map(src.TokensByDay),
		TokensByHour: cloneStringInt64Map(
			src.TokensByHour,
		),
	}
	for apiName, apiSnapshot := range src.APIs {
		copiedAPI := APISnapshot{
			TotalRequests: apiSnapshot.TotalRequests,
			TotalTokens:   apiSnapshot.TotalTokens,
			Models:        make(map[string]ModelSnapshot, len(apiSnapshot.Models)),
		}
		for modelName, modelSnapshot := range apiSnapshot.Models {
			copiedModel := ModelSnapshot{
				TotalRequests:  modelSnapshot.TotalRequests,
				TotalTokens:    modelSnapshot.TotalTokens,
				TokenBreakdown: modelSnapshot.TokenBreakdown,
				Latency:        modelSnapshot.Latency,
			}
			if len(modelSnapshot.Details) > 0 {
				copiedModel.Details = append([]RequestDetail(nil), modelSnapshot.Details...)
			}
			copiedAPI.Models[modelName] = copiedModel
		}
		dst.APIs[apiName] = copiedAPI
	}
	return dst
}

func mergeStatisticsSnapshots(base, extra StatisticsSnapshot) StatisticsSnapshot {
	if len(base.APIs) == 0 &&
		len(base.RequestsByDay) == 0 &&
		len(base.RequestsByHour) == 0 &&
		len(base.TokensByDay) == 0 &&
		len(base.TokensByHour) == 0 &&
		base.TotalRequests == 0 &&
		base.SuccessCount == 0 &&
		base.FailureCount == 0 &&
		base.TotalTokens == 0 {
		return cloneStatisticsSnapshot(extra)
	}
	if len(extra.APIs) == 0 &&
		len(extra.RequestsByDay) == 0 &&
		len(extra.RequestsByHour) == 0 &&
		len(extra.TokensByDay) == 0 &&
		len(extra.TokensByHour) == 0 &&
		extra.TotalRequests == 0 &&
		extra.SuccessCount == 0 &&
		extra.FailureCount == 0 &&
		extra.TotalTokens == 0 {
		return cloneStatisticsSnapshot(base)
	}

	dst := cloneStatisticsSnapshot(base)
	dst.TotalRequests += extra.TotalRequests
	dst.SuccessCount += extra.SuccessCount
	dst.FailureCount += extra.FailureCount
	dst.TotalTokens += extra.TotalTokens

	if dst.APIs == nil {
		dst.APIs = make(map[string]APISnapshot)
	}
	for apiName, apiSnapshot := range extra.APIs {
		current := dst.APIs[apiName]
		if current.Models == nil {
			current.Models = make(map[string]ModelSnapshot)
		}
		current.TotalRequests += apiSnapshot.TotalRequests
		current.TotalTokens += apiSnapshot.TotalTokens
		for modelName, modelSnapshot := range apiSnapshot.Models {
			modelCurrent := current.Models[modelName]
			modelCurrent.TotalRequests += modelSnapshot.TotalRequests
			modelCurrent.TotalTokens += modelSnapshot.TotalTokens
			mergeTokenStats(&modelCurrent.TokenBreakdown, modelSnapshot.TokenBreakdown)
			mergeLatencyStats(&modelCurrent.Latency, modelSnapshot.Latency)
			if len(modelSnapshot.Details) > 0 {
				modelCurrent.Details = append(modelCurrent.Details, modelSnapshot.Details...)
			}
			current.Models[modelName] = modelCurrent
		}
		dst.APIs[apiName] = current
	}

	if dst.RequestsByDay == nil {
		dst.RequestsByDay = make(map[string]int64)
	}
	for day, count := range extra.RequestsByDay {
		dst.RequestsByDay[day] += count
	}
	if dst.RequestsByHour == nil {
		dst.RequestsByHour = make(map[string]int64)
	}
	for hourKey, count := range extra.RequestsByHour {
		dst.RequestsByHour[hourKey] += count
	}
	if dst.TokensByDay == nil {
		dst.TokensByDay = make(map[string]int64)
	}
	for day, count := range extra.TokensByDay {
		dst.TokensByDay[day] += count
	}
	if dst.TokensByHour == nil {
		dst.TokensByHour = make(map[string]int64)
	}
	for hourKey, count := range extra.TokensByHour {
		dst.TokensByHour[hourKey] += count
	}

	return dst
}
