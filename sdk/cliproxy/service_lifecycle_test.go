package cliproxy

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestShutdownTimeoutReadsConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  *config.Config
		want time.Duration
	}{
		{name: "no config", cfg: nil, want: 30 * time.Second},
		{name: "unset", cfg: &config.Config{}, want: 30 * time.Second},
		{name: "negative", cfg: &config.Config{ShutdownTimeoutSeconds: -1}, want: 30 * time.Second},
		{name: "set", cfg: &config.Config{ShutdownTimeoutSeconds: 400}, want: 400 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{cfg: tc.cfg}
			if got := s.shutdownTimeout(); got != tc.want {
				t.Fatalf("shutdownTimeout() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestServerStopContextKeepsCallerDeadline(t *testing.T) {
	want := time.Now().Add(10 * time.Minute)
	parent, cancelParent := context.WithDeadline(context.Background(), want)
	defer cancelParent()

	ctx, cancel := serverStopContext(parent, 30*time.Second)
	defer cancel()

	got, ok := ctx.Deadline()
	if !ok || !got.Equal(want) {
		t.Fatalf("deadline = %v (set %t), want the caller's %v", got, ok, want)
	}
}

func TestServerStopContextAddsTimeoutWithoutDeadline(t *testing.T) {
	before := time.Now()
	ctx, cancel := serverStopContext(context.Background(), 30*time.Second)
	defer cancel()
	after := time.Now()

	got, ok := ctx.Deadline()
	if !ok {
		t.Fatal("serverStopContext returned a context without a deadline")
	}
	if got.Before(before.Add(30*time.Second)) || got.After(after.Add(30*time.Second)) {
		t.Fatalf("deadline = %v, want 30s after the call", got)
	}
}
