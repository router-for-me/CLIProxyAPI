package kiro

import (
	"sync"
	"testing"
	"time"
)

func TestNewTokenScorer(t *testing.T) {
	s := NewTokenScorer()
	if s == nil {
		t.Fatal("expected non-nil TokenScorer")
	}
	if s.metrics == nil {
		t.Error("expected non-nil metrics map")
	}
	if s.successRateWeight != 0.4 {
		t.Errorf("expected successRateWeight 0.4, got %f", s.successRateWeight)
	}
	if s.quotaWeight != 0.25 {
		t.Errorf("expected quotaWeight 0.25, got %f", s.quotaWeight)
	}
}

func TestRecordRequest_Success(t *testing.T) {
	s := NewTokenScorer()
	s.RecordRequest("token1", true, 100*time.Millisecond)

	m := s.GetMetrics("token1")
	if m == nil {
		t.Fatal("expected non-nil metrics")
	}
	if m.TotalRequests != 1 {
		t.Errorf("expected TotalRequests 1, got %d", m.TotalRequests)
	}
	if m.SuccessRate != 1.0 {
		t.Errorf("expected SuccessRate 1.0, got %f", m.SuccessRate)
	}
	if m.FailCount != 0 {
		t.Errorf("expected FailCount 0, got %d", m.FailCount)
	}
	if m.AvgLatency != 100 {
		t.Errorf("expected AvgLatency 100, got %f", m.AvgLatency)
	}
}

func TestRecordRequest_Failure(t *testing.T) {
	s := NewTokenScorer()
	s.RecordRequest("token1", false, 200*time.Millisecond)

	m := s.GetMetrics("token1")
	if m.SuccessRate != 0.0 {
		t.Errorf("expected SuccessRate 0.0, got %f", m.SuccessRate)
	}
	if m.FailCount != 1 {
		t.Errorf("expected FailCount 1, got %d", m.FailCount)
	}
}

func TestRecordRequest_MixedResults(t *testing.T) {
	s := NewTokenScorer()
	s.RecordRequest("token1", true, 100*time.Millisecond)
	s.RecordRequest("token1", true, 100*time.Millisecond)
	s.RecordRequest("token1", false, 100*time.Millisecond)
	s.RecordRequest("token1", true, 100*time.Millisecond)

	m := s.GetMetrics("token1")
	if m.TotalRequests != 4 {
		t.Errorf("expected TotalRequests 4, got %d", m.TotalRequests)
	}
	if m.SuccessRate != 0.75 {
		t.Errorf("expected SuccessRate 0.75, got %f", m.SuccessRate)
	}
	if m.FailCount != 0 {
		t.Errorf("expected FailCount 0 (reset on success), got %d", m.FailCount)
	}
}

func TestRecordRequest_ConsecutiveFailures(t *testing.T) {
	s := NewTokenScorer()
	s.RecordRequest("token1", true, 100*time.Millisecond)
	s.RecordRequest("token1", false, 100*time.Millisecond)
	s.RecordRequest("token1", false, 100*time.Millisecond)
	s.RecordRequest("token1", false, 100*time.Millisecond)

	m := s.GetMetrics("token1")
	if m.FailCount != 3 {
		t.Errorf("expected FailCount 3, got %d", m.FailCount)
	}
}

func TestSetQuotaRemaining(t *testing.T) {
	s := NewTokenScorer()
	s.SetQuotaRemaining("token1", 0.5)

	m := s.GetMetrics("token1")
	if m.QuotaRemaining != 0.5 {
		t.Errorf("expected QuotaRemaining 0.5, got %f", m.QuotaRemaining)
	}
}

func TestGetMetrics_NonExistent(t *testing.T) {
	s := NewTokenScorer()
	m := s.GetMetrics("nonexistent")
	if m != nil {
		t.Error("expected nil metrics for non-existent token")
	}
}

func TestGetMetrics_ReturnsCopy(t *testing.T) {
	s := NewTokenScorer()
	s.RecordRequest("token1", true, 100*time.Millisecond)

	m1 := s.GetMetrics("token1")
	m1.TotalRequests = 999

	m2 := s.GetMetrics("token1")
	if m2.TotalRequests == 999 {
		t.Error("GetMetrics should return a copy")
	}
}

func TestCalculateScore_NewToken(t *testing.T) {
	s := NewTokenScorer()
	score := s.CalculateScore("newtoken")
	if score != 1.0 {
		t.Errorf("expected score 1.0 for new token, got %f", score)
	}
}

func TestCalculateScore_PerfectToken(t *testing.T) {
	s := NewTokenScorer()
	s.RecordRequest("token1", true, 50*time.Millisecond)
	s.SetQuotaRemaining("token1", 1.0)

	time.Sleep(100 * time.Millisecond)
	score := s.CalculateScore("token1")
	if score < 0.5 || score > 1.0 {
		t.Errorf("expected high score for perfect token, got %f", score)
	}
}

func TestCalculateScore_FailedToken(t *testing.T) {
	s := NewTokenScorer()
	for i := 0; i < 5; i++ {
		s.RecordRequest("token1", false, 1000*time.Millisecond)
	}
	s.SetQuotaRemaining("token1", 0.1)

	score := s.CalculateScore("token1")
	if score > 0.5 {
		t.Errorf("expected low score for failed token, got %f", score)
	}
}

func TestCalculateScore_FailPenalty(t *testing.T) {
	s := NewTokenScorer()
	s.RecordRequest("token1", true, 100*time.Millisecond)
	scoreNoFail := s.CalculateScore("token1")

	s.RecordRequest("token1", false, 100*time.Millisecond)
	s.RecordRequest("token1", false, 100*time.Millisecond)
	scoreWithFail := s.CalculateScore("token1")

	if scoreWithFail >= scoreNoFail {
		t.Errorf("expected lower score with consecutive failures: noFail=%f, withFail=%f", scoreNoFail, scoreWithFail)
	}
}

func TestSelectBestToken_Empty(t *testing.T) {
	s := NewTokenScorer()
	best := s.SelectBestToken([]string{})
	if best != "" {
		t.Errorf("expected empty string for empty tokens, got %s", best)
	}
}

func TestSelectBestToken_SingleToken(t *testing.T) {
	s := NewTokenScorer()
	best := s.SelectBestToken([]string{"token1"})
	if best != "token1" {
		t.Errorf("expected token1, got %s", best)
	}
}

func TestSelectBestToken_MultipleTokens(t *testing.T) {
	s := NewTokenScorer()

	s.RecordRequest("bad", false, 1000*time.Millisecond)
	s.RecordRequest("bad", false, 1000*time.Millisecond)
	s.SetQuotaRemaining("bad", 0.1)

	s.RecordRequest("good", true, 50*time.Millisecond)
	s.SetQuotaRemaining("good", 0.9)

	time.Sleep(50 * time.Millisecond)

	best := s.SelectBestToken([]string{"bad", "good"})
	if best != "good" {
		t.Errorf("expected good token to be selected, got %s", best)
	}
}

func TestResetMetrics(t *testing.T) {
	s := NewTokenScorer()
	s.RecordRequest("token1", true, 100*time.Millisecond)
	s.ResetMetrics("token1")

	m := s.GetMetrics("token1")
	if m != nil {
		t.Error("expected nil metrics after reset")
	}
}

func TestResetAllMetrics(t *testing.T) {
	s := NewTokenScorer()
	s.RecordRequest("token1", true, 100*time.Millisecond)
	s.RecordRequest("token2", true, 100*time.Millisecond)
	s.RecordRequest("token3", true, 100*time.Millisecond)

	s.ResetAllMetrics()

	if s.GetMetrics("token1") != nil {
		t.Error("expected nil metrics for token1 after reset all")
	}
	if s.GetMetrics("token2") != nil {
		t.Error("expected nil metrics for token2 after reset all")
	}
}

func TestTokenScorer_ConcurrentAccess(t *testing.T) {
	s := NewTokenScorer()
	const numGoroutines = 50
	const numOperations = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			tokenKey := "token" + string(rune('a'+id%10))
			for j := 0; j < numOperations; j++ {
				switch j % 6 {
				case 0:
					s.RecordRequest(tokenKey, j%2 == 0, time.Duration(j)*time.Millisecond)
				case 1:
					s.SetQuotaRemaining(tokenKey, float64(j%100)/100)
				case 2:
					s.GetMetrics(tokenKey)
				case 3:
					s.CalculateScore(tokenKey)
				case 4:
					s.SelectBestToken([]string{tokenKey, "token_x", "token_y"})
				case 5:
					if j%20 == 0 {
						s.ResetMetrics(tokenKey)
					}
				}
			}
		}(i)
	}

	wg.Wait()
}

func TestAvgLatencyCalculation(t *testing.T) {
	s := NewTokenScorer()
	s.RecordRequest("token1", true, 100*time.Millisecond)
	s.RecordRequest("token1", true, 200*time.Millisecond)
	s.RecordRequest("token1", true, 300*time.Millisecond)

	m := s.GetMetrics("token1")
	if m.AvgLatency != 200 {
		t.Errorf("expected AvgLatency 200, got %f", m.AvgLatency)
	}
}

func TestLastUsedUpdated(t *testing.T) {
	s := NewTokenScorer()
	before := time.Now()
	s.RecordRequest("token1", true, 100*time.Millisecond)

	m := s.GetMetrics("token1")
	if m.LastUsed.Before(before) {
		t.Error("expected LastUsed to be after test start time")
	}
	if m.LastUsed.After(time.Now()) {
		t.Error("expected LastUsed to be before or equal to now")
	}
}

func TestDefaultQuotaForNewToken(t *testing.T) {
	s := NewTokenScorer()
	s.RecordRequest("token1", true, 100*time.Millisecond)

	m := s.GetMetrics("token1")
	if m.QuotaRemaining != 1.0 {
		t.Errorf("expected default QuotaRemaining 1.0, got %f", m.QuotaRemaining)
	}
}
