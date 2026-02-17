package auth

import (
	"testing"
	"time"
)

func TestGetProviderResetPattern(t *testing.T) {
	tests := []struct {
		provider string
		expected QuotaResetPattern
	}{
		{"anthropic", QuotaResetPatternHourly},
		{"claude", QuotaResetPatternHourly},
		{"Anthropic", QuotaResetPatternHourly},
		{"kimi", QuotaResetPatternMonthly},
		{"Kimi", QuotaResetPatternMonthly},
		{"minimax", QuotaResetPatternMonthly},
		{"gemini", QuotaResetPatternDaily},
		{"openrouter", QuotaResetPatternHourly},
		{"glm", QuotaResetPatternDaily},
		{"openai", QuotaResetPatternHourly},
		{"unknown", QuotaResetPatternUnknown},
		{"", QuotaResetPatternUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			got := GetProviderResetPattern(tt.provider)
			if got != tt.expected {
				t.Errorf("GetProviderResetPattern(%q) = %v, want %v", tt.provider, got, tt.expected)
			}
		})
	}
}

func TestGetProviderResetTimeZone(t *testing.T) {
	tests := []struct {
		provider string
		expected string
	}{
		{"anthropic", "UTC"},
		{"kimi", "Asia/Shanghai"},
		{"gemini", "UTC"},
		{"minimax", "Asia/Shanghai"},
		{"unknown", "UTC"},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			got := GetProviderResetTimeZone(tt.provider)
			if got != tt.expected {
				t.Errorf("GetProviderResetTimeZone(%q) = %v, want %v", tt.provider, got, tt.expected)
			}
		})
	}
}

func TestPredictQuotaResetTime_Hourly(t *testing.T) {
	now := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	predicted := PredictQuotaResetTime("anthropic", now)

	// Anthropic uses 5-hour rolling window
	expected := now.Add(5 * time.Hour)
	if !predicted.Equal(expected) {
		t.Errorf("PredictQuotaResetTime(anthropic) = %v, want %v", predicted, expected)
	}
}

func TestPredictQuotaResetTime_Daily(t *testing.T) {
	// Test Gemini daily reset at midnight UTC
	now := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	predicted := PredictQuotaResetTime("gemini", now)

	expected := time.Date(2024, 1, 16, 0, 0, 0, 0, time.UTC)
	if !predicted.Equal(expected) {
		t.Errorf("PredictQuotaResetTime(gemini) = %v, want %v", predicted, expected)
	}
}

func TestPredictQuotaResetTime_Monthly(t *testing.T) {
	// Test Kimi monthly reset on 1st of next month (Asia/Shanghai)
	now := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	predicted := PredictQuotaResetTime("kimi", now)

	// Feb 1st 00:00 in Asia/Shanghai = Jan 31 16:00 UTC
	expected := time.Date(2024, 1, 31, 16, 0, 0, 0, time.UTC)
	if !predicted.Equal(expected) {
		t.Errorf("PredictQuotaResetTime(kimi) = %v, want %v", predicted, expected)
	}
}

func TestPredictQuotaResetTime_MonthlyYearBoundary(t *testing.T) {
	// Test year boundary for monthly reset
	now := time.Date(2024, 12, 15, 10, 30, 0, 0, time.UTC)
	predicted := PredictQuotaResetTime("kimi", now)

	// Jan 1st 2025 00:00 in Asia/Shanghai = Dec 31 2024 16:00 UTC
	expected := time.Date(2024, 12, 31, 16, 0, 0, 0, time.UTC)
	if !predicted.Equal(expected) {
		t.Errorf("PredictQuotaResetTime(kimi year boundary) = %v, want %v", predicted, expected)
	}
}

func TestQuotaState_QuotaUtilizationRate(t *testing.T) {
	tests := []struct {
		name      string
		used      int64
		max       int64
		expected  float64
	}{
		{"empty quota", 0, 0, -1},
		{"full quota", 1000, 1000, 1.0},
		{"half quota", 500, 1000, 0.5},
		{"no max", 500, 0, -1},
		{"over quota", 1500, 1000, 1.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qs := &QuotaState{UsedTokens: tt.used, MaxTokens: tt.max}
			got := qs.QuotaUtilizationRate()
			if got != tt.expected {
				t.Errorf("QuotaUtilizationRate() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestQuotaState_HasRemainingQuota(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name     string
		exceeded bool
		recoverAt time.Time
		expected bool
	}{
		{"not exceeded", false, time.Time{}, true},
		{"exceeded future", true, now.Add(time.Hour), false},
		{"exceeded past", true, now.Add(-time.Hour), true},
		{"exceeded zero", true, time.Time{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qs := &QuotaState{
				Exceeded:      tt.exceeded,
				NextRecoverAt: tt.recoverAt,
			}
			got := qs.HasRemainingQuota()
			if got != tt.expected {
				t.Errorf("HasRemainingQuota() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestQuotaState_GetQuotaPriorityScore(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name     string
		qs       *QuotaState
		expected int
		checkMin bool
		minScore int
	}{
		{
			name:     "nil quota",
			qs:       nil,
			expected: 100,
		},
		{
			name:     "not exceeded 0% used",
			qs:       &QuotaState{Exceeded: false, UsedTokens: 0, MaxTokens: 1000},
			expected: 200,
		},
		{
			name:     "not exceeded 50% used",
			qs:       &QuotaState{Exceeded: false, UsedTokens: 500, MaxTokens: 1000},
			expected: 100,
		},
		{
			name:     "exceeded recover in 30 min",
			qs:       &QuotaState{Exceeded: true, NextRecoverAt: now.Add(30 * time.Minute)},
			expected: -30,
		},
		{
			name:     "exceeded recover in 2 hours",
			qs:       &QuotaState{Exceeded: true, NextRecoverAt: now.Add(2 * time.Hour)},
			expected: -120,
		},
		{
			name:     "exceeded past recovery",
			qs:       &QuotaState{Exceeded: true, NextRecoverAt: now.Add(-time.Hour)},
			expected: 50,
		},
		{
			name:     "exceeded no recovery time",
			qs:       &QuotaState{Exceeded: true, NextRecoverAt: time.Time{}},
			expected: -1000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.qs.GetQuotaPriorityScore(now)
			if tt.checkMin {
				if got < tt.minScore {
					t.Errorf("GetQuotaPriorityScore() = %v, want at least %v", got, tt.minScore)
				}
			} else if got != tt.expected {
				t.Errorf("GetQuotaPriorityScore() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestQuotaState_IsQuotaNearExhaustion(t *testing.T) {
	tests := []struct {
		name     string
		used     int64
		max      int64
		expected bool
	}{
		{"no quota info", 0, 0, false},
		{"0% used", 0, 1000, false},
		{"50% used", 500, 1000, false},
		{"80% used", 800, 1000, false},
		{"81% used", 810, 1000, true},
		{"100% used", 1000, 1000, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qs := &QuotaState{UsedTokens: tt.used, MaxTokens: tt.max}
			got := qs.IsQuotaNearExhaustion()
			if got != tt.expected {
				t.Errorf("IsQuotaNearExhaustion() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestQuotaState_RecordUsage(t *testing.T) {
	qs := &QuotaState{UsedTokens: 100}
	qs.RecordUsage(50)
	if qs.UsedTokens != 150 {
		t.Errorf("RecordUsage(50) resulted in UsedTokens=%d, want 150", qs.UsedTokens)
	}
}

func TestQuotaState_ResetUsage(t *testing.T) {
	qs := &QuotaState{
		Exceeded:      true,
		Reason:        "quota",
		BackoffLevel:  3,
		UsedTokens:    1000,
		NextRecoverAt: time.Now(),
	}
	qs.ResetUsage()

	if qs.Exceeded {
		t.Error("ResetUsage() did not clear Exceeded")
	}
	if qs.Reason != "" {
		t.Error("ResetUsage() did not clear Reason")
	}
	if qs.BackoffLevel != 0 {
		t.Error("ResetUsage() did not clear BackoffLevel")
	}
	if qs.UsedTokens != 0 {
		t.Error("ResetUsage() did not clear UsedTokens")
	}
	if !qs.NextRecoverAt.IsZero() {
		t.Error("ResetUsage() did not clear NextRecoverAt")
	}
}

func TestQuotaState_IsRecoveringSoon(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name     string
		exceeded bool
		recoverAt time.Time
		within   time.Duration
		expected bool
	}{
		{"not exceeded", false, time.Time{}, time.Hour, true},
		{"recovering in 30 min within 1 hour", true, now.Add(30 * time.Minute), time.Hour, true},
		{"recovering in 2 hours within 1 hour", true, now.Add(2 * time.Hour), time.Hour, false},
		{"no recovery time", true, time.Time{}, time.Hour, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qs := &QuotaState{
				Exceeded:      tt.exceeded,
				NextRecoverAt: tt.recoverAt,
			}
			got := qs.IsRecoveringSoon(tt.within, now)
			if got != tt.expected {
				t.Errorf("IsRecoveringSoon() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestAuth_HasAllowance(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name     string
		auth     *Auth
		model    string
		expected bool
	}{
		{
			name:     "nil auth",
			auth:     nil,
			expected: false,
		},
		{
			name:     "not exceeded",
			auth:     &Auth{Quota: QuotaState{Exceeded: false}},
			expected: true,
		},
		{
			name:     "exceeded future recovery",
			auth:     &Auth{Quota: QuotaState{Exceeded: true, NextRecoverAt: now.Add(time.Hour)}},
			expected: false,
		},
		{
			name:     "exceeded past recovery",
			auth:     &Auth{Quota: QuotaState{Exceeded: true, NextRecoverAt: now.Add(-time.Hour)}},
			expected: true,
		},
		{
			name: "model-specific quota available",
			auth: &Auth{
				ModelStates: map[string]*ModelState{
					"gpt-4": {Quota: QuotaState{Exceeded: false}},
				},
			},
			model:    "gpt-4",
			expected: true,
		},
		{
			name: "model-specific quota exceeded",
			auth: &Auth{
				ModelStates: map[string]*ModelState{
					"gpt-4": {Quota: QuotaState{Exceeded: true, NextRecoverAt: now.Add(time.Hour)}},
				},
			},
			model:    "gpt-4",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.auth.HasAllowance(tt.model)
			if got != tt.expected {
				t.Errorf("HasAllowance() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestAuth_UpdateUsage(t *testing.T) {
	auth := &Auth{
		Quota: QuotaState{UsedTokens: 100},
		ModelStates: map[string]*ModelState{
			"gpt-4": {Quota: QuotaState{UsedTokens: 50}},
		},
	}

	auth.UpdateUsage("gpt-4", 25)

	if auth.Quota.UsedTokens != 125 {
		t.Errorf("auth.Quota.UsedTokens = %d, want 125", auth.Quota.UsedTokens)
	}
	if auth.ModelStates["gpt-4"].Quota.UsedTokens != 75 {
		t.Errorf("model Quota.UsedTokens = %d, want 75", auth.ModelStates["gpt-4"].Quota.UsedTokens)
	}
}

func TestAuth_GetQuotaInfo(t *testing.T) {
	future := time.Now().Add(time.Hour)
	auth := &Auth{
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "quota",
			NextRecoverAt: future,
			BackoffLevel:  2,
			UsedTokens:    800,
			MaxTokens:     1000,
			ResetPattern:  "daily",
			ResetTimeZone: "UTC",
		},
	}

	info := auth.GetQuotaInfo("")

	if info["exceeded"] != true {
		t.Error("GetQuotaInfo exceeded mismatch")
	}
	if info["reason"] != "quota" {
		t.Error("GetQuotaInfo reason mismatch")
	}
	if info["backoff_level"] != 2 {
		t.Error("GetQuotaInfo backoff_level mismatch")
	}
	if info["used_tokens"] != int64(800) {
		t.Error("GetQuotaInfo used_tokens mismatch")
	}
	if info["max_tokens"] != int64(1000) {
		t.Error("GetQuotaInfo max_tokens mismatch")
	}
	if info["reset_pattern"] != "daily" {
		t.Error("GetQuotaInfo reset_pattern mismatch")
	}
	if info["reset_timezone"] != "UTC" {
		t.Error("GetQuotaInfo reset_timezone mismatch")
	}
	if info["has_remaining"] != false {
		t.Errorf("GetQuotaInfo has_remaining = %v, want false", info["has_remaining"])
	}
	if info["near_exhaustion"] != true {
		t.Errorf("GetQuotaInfo near_exhaustion = %v, want true", info["near_exhaustion"])
	}
}

func TestGetQuotaPriorityScore(t *testing.T) {
	now := time.Now()

	auth := &Auth{
		ID: "test-auth",
		Quota: QuotaState{
			Exceeded: false,
			UsedTokens: 0,
			MaxTokens: 1000,
		},
		ModelStates: map[string]*ModelState{
			"gpt-4": {
				Quota: QuotaState{
					Exceeded: false,
					UsedTokens: 500,
					MaxTokens: 1000,
				},
			},
		},
	}

	// Test auth-level score
	authScore := getQuotaPriorityScore(auth, "unknown-model", now)
	if authScore != 200 { // 0% used = 200 score
		t.Errorf("getQuotaPriorityScore(auth-level) = %d, want 200", authScore)
	}

	// Test model-specific score
	modelScore := getQuotaPriorityScore(auth, "gpt-4", now)
	if modelScore != 100 { // 50% used = 100 score
		t.Errorf("getQuotaPriorityScore(model-level) = %d, want 100", modelScore)
	}
}

func TestGetNextResetTime(t *testing.T) {
	future := time.Now().Add(time.Hour)

	auth := &Auth{
		ID: "test-auth",
		Quota: QuotaState{
			NextRecoverAt: future,
		},
		ModelStates: map[string]*ModelState{
			"gpt-4": {
				Quota: QuotaState{
					NextRecoverAt: future.Add(time.Hour),
				},
			},
		},
	}

	// Test auth-level reset time
	authReset := getNextResetTime(auth, "unknown-model")
	if !authReset.Equal(future) {
		t.Errorf("getNextResetTime(auth-level) = %v, want %v", authReset, future)
	}

	// Test model-specific reset time
	modelReset := getNextResetTime(auth, "gpt-4")
	expected := future.Add(time.Hour)
	if !modelReset.Equal(expected) {
		t.Errorf("getNextResetTime(model-level) = %v, want %v", modelReset, expected)
	}
}
