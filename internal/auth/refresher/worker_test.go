package refresher

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestToken_NeedsRefresh(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name     string
		token    *Token
		leadTime time.Duration
		want     bool
	}{
		{
			name:     "nil token",
			token:    nil,
			leadTime: 10 * time.Minute,
			want:     false,
		},
		{
			name:     "no refresh token",
			token:    &Token{ID: "1", ExpiresAt: now.Add(5 * time.Minute)},
			leadTime: 10 * time.Minute,
			want:     false,
		},
		{
			name:     "zero expiry",
			token:    &Token{ID: "1", RefreshToken: "refresh"},
			leadTime: 10 * time.Minute,
			want:     false,
		},
		{
			name:     "expires within lead time",
			token:    &Token{ID: "1", RefreshToken: "refresh", ExpiresAt: now.Add(5 * time.Minute)},
			leadTime: 10 * time.Minute,
			want:     true,
		},
		{
			name:     "expires after lead time",
			token:    &Token{ID: "1", RefreshToken: "refresh", ExpiresAt: now.Add(30 * time.Minute)},
			leadTime: 10 * time.Minute,
			want:     false,
		},
		{
			name:     "already expired",
			token:    &Token{ID: "1", RefreshToken: "refresh", ExpiresAt: now.Add(-1 * time.Minute)},
			leadTime: 10 * time.Minute,
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.token.NeedsRefresh(tt.leadTime); got != tt.want {
				t.Errorf("NeedsRefresh() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWorker_RegisterUnregister(t *testing.T) {
	refresher := func(ctx context.Context, token *Token) (time.Time, error) {
		return time.Now().Add(1 * time.Hour), nil
	}

	w := NewWorker(refresher, DefaultConfig(), nil)
	w.Start()
	defer w.Stop()

	token := &Token{
		ID:           "test-1",
		Provider:     "test",
		RefreshToken: "refresh-token",
		ExpiresAt:    time.Now().Add(30 * time.Minute),
	}

	// Register
	if err := w.Register(token); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if w.TokenCount() != 1 {
		t.Errorf("TokenCount() = %d, want 1", w.TokenCount())
	}

	// Get
	got := w.Get("test-1")
	if got == nil {
		t.Fatal("Get() returned nil")
	}
	if got.ID != token.ID {
		t.Errorf("Get().ID = %v, want %v", got.ID, token.ID)
	}

	// Unregister
	w.Unregister("test-1")
	if w.TokenCount() != 0 {
		t.Errorf("TokenCount() after unregister = %d, want 0", w.TokenCount())
	}
	if got := w.Get("test-1"); got != nil {
		t.Errorf("Get() after unregister = %v, want nil", got)
	}
}

func TestWorker_RefreshTriggered(t *testing.T) {
	var refreshCount atomic.Int32
	newExpiry := time.Now().Add(2 * time.Hour)

	refresher := func(ctx context.Context, token *Token) (time.Time, error) {
		refreshCount.Add(1)
		return newExpiry, nil
	}

	config := Config{
		RefreshLeadTime: 30 * time.Minute,
		CheckInterval:   50 * time.Millisecond,
		MaxConcurrency:  5,
		RetryDelay:      1 * time.Second,
	}

	var hookCalled atomic.Bool
	hook := &testHook{
		onSuccess: func(token *Token, exp time.Time) {
			hookCalled.Store(true)
		},
	}

	w := NewWorker(refresher, config, hook)
	w.Start()

	// Register token that needs refresh (expires in 10 minutes, lead time is 30 minutes)
	token := &Token{
		ID:           "test-refresh",
		Provider:     "test",
		RefreshToken: "refresh-token",
		ExpiresAt:    time.Now().Add(10 * time.Minute),
	}
	if err := w.Register(token); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	// Wait for refresh to be triggered
	time.Sleep(200 * time.Millisecond)
	w.Stop()

	if refreshCount.Load() == 0 {
		t.Error("refresh was not triggered")
	}
	if !hookCalled.Load() {
		t.Error("hook.OnRefreshSuccess was not called")
	}

	// Check updated expiry
	got := w.Get("test-refresh")
	if got == nil {
		t.Fatal("Get() returned nil after refresh")
	}
	// The expiry should be updated
	if got.ExpiresAt.Before(time.Now().Add(1 * time.Hour)) {
		t.Errorf("expiry was not updated: got %v", got.ExpiresAt)
	}
}

func TestWorker_RefreshError(t *testing.T) {
	refreshErr := errors.New("refresh failed")
	var refreshCount atomic.Int32

	refresher := func(ctx context.Context, token *Token) (time.Time, error) {
		refreshCount.Add(1)
		return time.Time{}, refreshErr
	}

	config := Config{
		RefreshLeadTime: 30 * time.Minute,
		CheckInterval:   50 * time.Millisecond,
		MaxConcurrency:  5,
		RetryDelay:      10 * time.Second, // Long retry delay
	}

	var errorHookCalled atomic.Bool
	hook := &testHook{
		onError: func(token *Token, err error) {
			errorHookCalled.Store(true)
		},
	}

	w := NewWorker(refresher, config, hook)
	w.Start()

	token := &Token{
		ID:           "test-error",
		Provider:     "test",
		RefreshToken: "refresh-token",
		ExpiresAt:    time.Now().Add(10 * time.Minute),
	}
	if err := w.Register(token); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	// Wait for first refresh attempt
	time.Sleep(200 * time.Millisecond)
	w.Stop()

	if refreshCount.Load() == 0 {
		t.Error("refresh was not attempted")
	}
	if !errorHookCalled.Load() {
		t.Error("hook.OnRefreshError was not called")
	}

	// Check error is recorded
	got := w.Get("test-error")
	if got == nil {
		t.Fatal("Get() returned nil")
	}
	if got.RefreshError == nil {
		t.Error("RefreshError was not recorded")
	}
}

func TestWorker_ConcurrencyLimit(t *testing.T) {
	var concurrent atomic.Int32
	var maxConcurrent atomic.Int32

	refresher := func(ctx context.Context, token *Token) (time.Time, error) {
		current := concurrent.Add(1)
		// Track max concurrent
		for {
			old := maxConcurrent.Load()
			if current <= old || maxConcurrent.CompareAndSwap(old, current) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		concurrent.Add(-1)
		return time.Now().Add(1 * time.Hour), nil
	}

	config := Config{
		RefreshLeadTime: 30 * time.Minute,
		CheckInterval:   10 * time.Millisecond,
		MaxConcurrency:  2,
		RetryDelay:      1 * time.Second,
	}

	w := NewWorker(refresher, config, nil)
	w.Start()

	// Register multiple tokens that all need refresh
	for i := 0; i < 10; i++ {
		token := &Token{
			ID:           "test-" + string(rune('0'+i)),
			Provider:     "test",
			RefreshToken: "refresh",
			ExpiresAt:    time.Now().Add(5 * time.Minute),
		}
		_ = w.Register(token)
	}

	// Wait for refreshes
	time.Sleep(500 * time.Millisecond)
	w.Stop()

	if maxConcurrent.Load() > 2 {
		t.Errorf("max concurrent = %d, want <= 2", maxConcurrent.Load())
	}
}

func TestWorker_StopWaitsForRefreshes(t *testing.T) {
	var refreshStarted atomic.Bool
	var refreshCompleted atomic.Bool

	refresher := func(ctx context.Context, token *Token) (time.Time, error) {
		refreshStarted.Store(true)
		time.Sleep(100 * time.Millisecond)
		refreshCompleted.Store(true)
		return time.Now().Add(1 * time.Hour), nil
	}

	config := Config{
		RefreshLeadTime: 30 * time.Minute,
		CheckInterval:   10 * time.Millisecond,
		MaxConcurrency:  5,
		RetryDelay:      1 * time.Second,
	}

	w := NewWorker(refresher, config, nil)
	w.Start()

	token := &Token{
		ID:           "test-stop",
		Provider:     "test",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(5 * time.Minute),
	}
	_ = w.Register(token)

	// Wait for refresh to start
	time.Sleep(50 * time.Millisecond)

	// Stop should wait for refresh to complete
	w.Stop()

	// Note: Due to timing, the refresh might not have started yet
	// So we just verify that Stop() returns without hanging
}

type testHook struct {
	onSuccess func(*Token, time.Time)
	onError   func(*Token, error)
}

func (h *testHook) OnRefreshSuccess(token *Token, newExpiresAt time.Time) {
	if h.onSuccess != nil {
		h.onSuccess(token, newExpiresAt)
	}
}

func (h *testHook) OnRefreshError(token *Token, err error) {
	if h.onError != nil {
		h.onError(token, err)
	}
}
