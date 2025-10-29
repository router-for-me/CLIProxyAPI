package auth

import (
    "context"
    "math"
    "sort"
    "strings"
    "sync"
    "time"

    cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
    log "github.com/sirupsen/logrus"
)

// IntelligentSelector implements intelligent model routing based on token usage,
// quota availability, and request characteristics.
type IntelligentSelector struct {
    mu sync.RWMutex

    // Track token usage per auth per model
    authUsage map[string]map[string]*authUsageStats

    // Model priority configuration
    modelPriorities map[string]int

    // Request complexity threshold
    complexityThresholds map[string]int
}

// authUsageStats tracks usage statistics for an auth credential
type authUsageStats struct {
    TotalTokens      int64
    TotalRequests    int64
    LastUsedAt       time.Time
    AvgResponseTime  time.Duration
    SuccessRate      float64
    LastError        error
    LastErrorAt      time.Time
    ConsecutiveErrors int
}

// NewIntelligentSelector creates a new intelligent selector instance
func NewIntelligentSelector() *IntelligentSelector {
    return &IntelligentSelector{
        authUsage:            make(map[string]map[string]*authUsageStats),
        modelPriorities:      make(map[string]int),
        complexityThresholds: initComplexityThresholds(),
    }
}

// initComplexityThresholds initializes default complexity thresholds for different models
func initComplexityThresholds() map[string]int {
    return map[string]int{
        "gpt-5":                     4000,
        "gpt-5-codex":              4000,
        "claude-opus":              8000,
        "claude-sonnet":            4000,
        "claude-haiku":             2000,
        "gemini-2.5-pro":           8000,
        "gemini-2.5-flash":         4000,
        "gemini-2.5-flash-lite":    2000,
        "qwen3-coder-plus":         4000,
        "qwen3-max":                8000,
        "deepseek-v3.2":            8000,
        "deepseek-r1":              8000,
        "kimi-k2":                  8000,
    }
}

// Pick selects the most suitable auth based on intelligent routing criteria
func (s *IntelligentSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
    if len(auths) == 0 {
        return nil, &Error{Code: "auth_not_found", Message: "no auth candidates"}
    }

    now := time.Now()
    
    // Filter out blocked auths
    candidates := s.filterAvailableAuths(auths, model, now)
    if len(candidates) == 0 {
        return s.handleNoAvailableAuths(auths, model, provider, now)
    }

    // Calculate request complexity from options
    complexity := s.estimateComplexity(opts)

    // Score each candidate
    scored := s.scoreAuthCandidates(candidates, model, complexity, now)
    if len(scored) == 0 {
        return nil, &Error{Code: "auth_unavailable", Message: "no suitable auth available"}
    }

    // Select the best candidate
    bestAuth := scored[0].auth
    
    // Update usage tracking
    s.recordAuthSelection(bestAuth.ID, model, now)

    log.Debugf("intelligent selector: chose auth %s for model %s (score: %.2f, complexity: %d)", 
        bestAuth.ID, model, scored[0].score, complexity)

    return bestAuth, nil
}

// filterAvailableAuths removes blocked/unavailable auths
func (s *IntelligentSelector) filterAvailableAuths(auths []*Auth, model string, now time.Time) []*Auth {
    available := make([]*Auth, 0, len(auths))
    for _, auth := range auths {
        blocked, _, _ := isAuthBlockedForModel(auth, model, now)
        if !blocked {
            available = append(available, auth)
        }
    }
    return available
}

// handleNoAvailableAuths returns appropriate error when no auths are available
func (s *IntelligentSelector) handleNoAvailableAuths(auths []*Auth, model, provider string, now time.Time) (*Auth, error) {
    cooldownCount := 0
    var earliest time.Time
    
    for _, auth := range auths {
        blocked, reason, next := isAuthBlockedForModel(auth, model, now)
        if blocked && reason == blockReasonCooldown {
            cooldownCount++
            if !next.IsZero() && (earliest.IsZero() || next.Before(earliest)) {
                earliest = next
            }
        }
    }

    if cooldownCount == len(auths) && !earliest.IsZero() {
        resetIn := earliest.Sub(now)
        if resetIn < 0 {
            resetIn = 0
        }
        return nil, newModelCooldownError(model, provider, resetIn)
    }

    return nil, &Error{Code: "auth_unavailable", Message: "no auth available"}
}

// estimateComplexity calculates request complexity from options
func (s *IntelligentSelector) estimateComplexity(opts cliproxyexecutor.Options) int {
    complexity := 1000 // default estimate

    // Try to extract max tokens from metadata
    if opts.Metadata != nil {
        if maxTokens, ok := opts.Metadata["max_tokens"].(int); ok && maxTokens > 0 {
            complexity = maxTokens
        } else if maxTokens, ok := opts.Metadata["max_completion_tokens"].(int); ok && maxTokens > 0 {
            complexity = maxTokens
        }
    }

    // Try to estimate from original request if available
    if opts.OriginalRequest != nil && len(opts.OriginalRequest) > 0 {
        // Rough estimate: assume 4 chars per token
        estimatedTokens := len(opts.OriginalRequest) / 4
        if estimatedTokens > complexity {
            complexity = estimatedTokens
        }
    }

    // Additional complexity factors could be added here
    // - presence of tools/functions
    // - multi-turn conversations
    // - image/multimodal inputs

    return complexity
}

// scoredAuth holds an auth with its calculated score
type scoredAuth struct {
    auth  *Auth
    score float64
}

// scoreAuthCandidates calculates a score for each auth based on multiple factors
func (s *IntelligentSelector) scoreAuthCandidates(candidates []*Auth, model string, complexity int, now time.Time) []scoredAuth {
    s.mu.RLock()
    defer s.mu.RUnlock()

    scored := make([]scoredAuth, 0, len(candidates))

    for _, auth := range candidates {
        score := s.calculateAuthScore(auth, model, complexity, now)
        scored = append(scored, scoredAuth{auth: auth, score: score})
    }

    // Sort by score (higher is better)
    sort.Slice(scored, func(i, j int) bool {
        return scored[i].score > scored[j].score
    })

    return scored
}

// calculateAuthScore computes a composite score for an auth credential
func (s *IntelligentSelector) calculateAuthScore(auth *Auth, model string, complexity int, now time.Time) float64 {
    score := 100.0 // base score

    // Factor 1: Quota state (most important)
    quotaScore := s.scoreQuotaState(auth, model)
    score *= quotaScore

    // Factor 2: Usage history
    usageScore := s.scoreUsageHistory(auth.ID, model, now)
    score *= usageScore

    // Factor 3: Error rate
    errorScore := s.scoreErrorRate(auth, model, now)
    score *= errorScore

    // Factor 4: Freshness (prefer recently successful auths)
    freshnessScore := s.scoreFreshness(auth.ID, model, now)
    score *= freshnessScore

    // Factor 5: Complexity matching
    complexityScore := s.scoreComplexityMatch(auth, model, complexity)
    score *= complexityScore

    return score
}

// scoreQuotaState evaluates the auth's quota availability
func (s *IntelligentSelector) scoreQuotaState(auth *Auth, model string) float64 {
    // Check model-specific quota first
    if len(auth.ModelStates) > 0 {
        if state, ok := auth.ModelStates[model]; ok && state != nil {
            if state.Quota.Exceeded {
                return 0.1 // very low score for exceeded quota
            }
        }
    }

    // Check global quota
    if auth.Quota.Exceeded {
        return 0.2
    }

    // Check if auth is unavailable
    if auth.Unavailable {
        return 0.3
    }

    return 1.0 // full score for available auth
}

// scoreUsageHistory evaluates based on past usage patterns
func (s *IntelligentSelector) scoreUsageHistory(authID, model string, now time.Time) float64 {
    authModels, ok := s.authUsage[authID]
    if !ok {
        return 0.9 // slightly prefer untested auths
    }

    stats, ok := authModels[model]
    if !ok {
        return 0.9
    }

    // Prefer auths with moderate usage (not overused, not underused)
    if stats.TotalRequests == 0 {
        return 0.9
    }

    // Calculate usage intensity (requests per hour)
    timeSinceFirstUse := now.Sub(stats.LastUsedAt)
    if timeSinceFirstUse <= 0 {
        timeSinceFirstUse = time.Hour
    }

    hoursActive := float64(timeSinceFirstUse.Hours())
    if hoursActive < 1 {
        hoursActive = 1
    }

    requestsPerHour := float64(stats.TotalRequests) / hoursActive

    // Optimal range: 10-50 requests per hour
    if requestsPerHour < 10 {
        return 0.95 // lightly used, good
    } else if requestsPerHour <= 50 {
        return 1.0 // optimal usage
    } else if requestsPerHour <= 100 {
        return 0.8 // heavy usage, might hit limits soon
    } else {
        return 0.6 // very heavy usage
    }
}

// scoreErrorRate penalizes auths with recent errors
func (s *IntelligentSelector) scoreErrorRate(auth *Auth, model string, now time.Time) float64 {
    s.mu.RLock()
    authModels, ok := s.authUsage[auth.ID]
    s.mu.RUnlock()

    if !ok {
        return 1.0
    }

    stats, ok := authModels[model]
    if !ok {
        return 1.0
    }

    // Recent error penalty
    if stats.LastError != nil {
        timeSinceError := now.Sub(stats.LastErrorAt)
        if timeSinceError < 5*time.Minute {
            // Strong penalty for very recent errors
            return 0.3
        } else if timeSinceError < 15*time.Minute {
            // Moderate penalty
            return 0.6
        } else if timeSinceError < time.Hour {
            // Light penalty
            return 0.8
        }
    }

    // Consecutive errors penalty
    if stats.ConsecutiveErrors > 0 {
        penalty := math.Pow(0.8, float64(stats.ConsecutiveErrors))
        if penalty < 0.3 {
            penalty = 0.3
        }
        return penalty
    }

    // Success rate evaluation
    if stats.TotalRequests >= 10 {
        if stats.SuccessRate >= 0.95 {
            return 1.1 // bonus for high success rate
        } else if stats.SuccessRate >= 0.80 {
            return 1.0
        } else if stats.SuccessRate >= 0.60 {
            return 0.7
        } else {
            return 0.4
        }
    }

    return 1.0
}

// scoreFreshness prefers recently successful auths
func (s *IntelligentSelector) scoreFreshness(authID, model string, now time.Time) float64 {
    s.mu.RLock()
    authModels, ok := s.authUsage[authID]
    s.mu.RUnlock()

    if !ok {
        return 0.95
    }

    stats, ok := authModels[model]
    if !ok || stats.LastUsedAt.IsZero() {
        return 0.95
    }

    timeSinceLast := now.Sub(stats.LastUsedAt)

    // Prefer recently used auths (within last hour)
    if timeSinceLast < 5*time.Minute {
        return 1.1 // bonus for very recent use
    } else if timeSinceLast < 30*time.Minute {
        return 1.05
    } else if timeSinceLast < time.Hour {
        return 1.0
    } else if timeSinceLast < 6*time.Hour {
        return 0.95
    } else {
        return 0.9 // slight penalty for idle auths
    }
}

// scoreComplexityMatch evaluates if the auth is suitable for the request complexity
func (s *IntelligentSelector) scoreComplexityMatch(auth *Auth, model string, complexity int) float64 {
    // Check if we have a threshold for this model
    threshold, ok := s.complexityThresholds[model]
    if !ok {
        // Try to match by model prefix
        for prefix, thresh := range s.complexityThresholds {
            if strings.HasPrefix(strings.ToLower(model), strings.ToLower(prefix)) {
                threshold = thresh
                ok = true
                break
            }
        }
    }

    if !ok {
        return 1.0 // no threshold, neutral score
    }

    // If complexity is within threshold, prefer this auth
    if complexity <= threshold {
        return 1.0
    }

    // For high complexity requests, slightly penalize but still allow
    ratio := float64(threshold) / float64(complexity)
    if ratio < 0.5 {
        ratio = 0.5 // minimum score
    }

    return ratio
}

// recordAuthSelection updates usage tracking when an auth is selected
func (s *IntelligentSelector) recordAuthSelection(authID, model string, now time.Time) {
    s.mu.Lock()
    defer s.mu.Unlock()

    if s.authUsage[authID] == nil {
        s.authUsage[authID] = make(map[string]*authUsageStats)
    }

    stats := s.authUsage[authID][model]
    if stats == nil {
        stats = &authUsageStats{}
        s.authUsage[authID][model] = stats
    }

    stats.TotalRequests++
    stats.LastUsedAt = now
}

// RecordAuthSuccess records a successful request for usage tracking
func (s *IntelligentSelector) RecordAuthSuccess(authID, model string, tokens int64, responseTime time.Duration) {
    s.mu.Lock()
    defer s.mu.Unlock()

    if s.authUsage[authID] == nil {
        s.authUsage[authID] = make(map[string]*authUsageStats)
    }

    stats := s.authUsage[authID][model]
    if stats == nil {
        stats = &authUsageStats{}
        s.authUsage[authID][model] = stats
    }

    stats.TotalTokens += tokens
    stats.ConsecutiveErrors = 0
    stats.LastError = nil

    // Update success rate
    totalRequests := float64(stats.TotalRequests)
    if totalRequests > 0 {
        successCount := totalRequests * stats.SuccessRate
        stats.SuccessRate = (successCount + 1) / (totalRequests + 1)
    } else {
        stats.SuccessRate = 1.0
    }

    // Update average response time
    if stats.AvgResponseTime == 0 {
        stats.AvgResponseTime = responseTime
    } else {
        stats.AvgResponseTime = (stats.AvgResponseTime + responseTime) / 2
    }
}

// RecordAuthError records a failed request for usage tracking
func (s *IntelligentSelector) RecordAuthError(authID, model string, err error) {
    s.mu.Lock()
    defer s.mu.Unlock()

    if s.authUsage[authID] == nil {
        s.authUsage[authID] = make(map[string]*authUsageStats)
    }

    stats := s.authUsage[authID][model]
    if stats == nil {
        stats = &authUsageStats{}
        s.authUsage[authID][model] = stats
    }

    stats.LastError = err
    stats.LastErrorAt = time.Now()
    stats.ConsecutiveErrors++

    // Update success rate
    totalRequests := float64(stats.TotalRequests)
    if totalRequests > 0 {
        successCount := totalRequests * stats.SuccessRate
        stats.SuccessRate = successCount / (totalRequests + 1)
    }
}

// GetAuthUsageStats returns usage statistics for an auth
func (s *IntelligentSelector) GetAuthUsageStats(authID string) map[string]*authUsageStats {
    s.mu.RLock()
    defer s.mu.RUnlock()

    stats, ok := s.authUsage[authID]
    if !ok {
        return nil
    }

    // Return a copy to avoid concurrent modification
    result := make(map[string]*authUsageStats)
    for model, stat := range stats {
        statCopy := *stat
        result[model] = &statCopy
    }

    return result
}

// CleanupOldStats removes usage stats older than the specified duration
func (s *IntelligentSelector) CleanupOldStats(maxAge time.Duration) {
    s.mu.Lock()
    defer s.mu.Unlock()

    now := time.Now()
    for authID, models := range s.authUsage {
        for model, stats := range models {
            if !stats.LastUsedAt.IsZero() && now.Sub(stats.LastUsedAt) > maxAge {
                delete(models, model)
            }
        }
        if len(models) == 0 {
            delete(s.authUsage, authID)
        }
    }
}
