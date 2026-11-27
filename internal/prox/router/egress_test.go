package router

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/quota"
)

func TestProxyPollErrorIsSanitizedAndBaseURLIsSkipped(t *testing.T) {
	h := newHarness(t)
	h.r.SyncRoster([]RosterEntry{
		{ID: "proxy", AuthIndex: "proxy-index", Provider: "claude"},
		{ID: "custom", AuthIndex: "custom-index", Provider: "claude", BaseURL: "https://custom.test"},
	})
	tasks := h.r.DuePolls()
	if len(tasks) != 1 || tasks[0].AuthID != "proxy" {
		t.Fatal("base_url skip changed")
	}
	f := quota.NewHTTPFetcher()
	defer f.Close()
	snap, err := f.Fetch(context.Background(), quota.Credential{
		Type: "claude", AccessToken: "token", ProxyURL: "http://secret-user:secret-password@%zz",
	})
	h.r.ApplyPoll("proxy", snap, "", err)
	if got := h.r.accounts["proxy"].PollError; got != "invalid_proxy_url" {
		t.Fatalf("unexpected poll_error: %q", got)
	}
}
