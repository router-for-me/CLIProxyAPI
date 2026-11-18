package helps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
)

func TestClaudeThreadBetaStoreRestoresWireProfile(t *testing.T) {
	for _, betas := range []string{"", "message-threads-2026-08-12,effort-2025-11-24"} {
		t.Run(betas, func(t *testing.T) {
			store := &ClaudeThreadBetaStore{}
			first, ok, _ := store.Bind("caller-a", "", http.Header{"Anthropic-Beta": []string{betas}}, ClaudeThreadBetaPolicy{})
			if !ok {
				t.Fatal("create was rejected")
			}
			first.Commit("message-a")
			for _, scope := range []string{"caller-b", "other-credential"} {
				if _, found, _ := store.Bind(scope, "message-a", http.Header{}, ClaudeThreadBetaPolicy{}); found {
					t.Fatalf("profile leaked into scope %q", scope)
				}
			}
			header := http.Header{"Anthropic-Beta": []string{"derived-beta"}, "anthropic-beta": []string{"custom-beta"}}
			second, found, _ := store.Bind("caller-a", "message-a", header, ClaudeThreadBetaPolicy{})
			if !found || header.Get("Anthropic-Beta") != betas {
				t.Fatalf("restored header = %v, found=%v, want %q", header, found, betas)
			}
			if _, duplicate := header["anthropic-beta"]; duplicate {
				t.Fatal("restoring retained a duplicate wire-cased header")
			}
			second.Commit("message-b")
			thirdHeader := http.Header{"Anthropic-Beta": []string{"new-body-beta"}}
			if _, foundThird, _ := store.Bind("caller-a", "message-b", thirdHeader, ClaudeThreadBetaPolicy{}); !foundThird || thirdHeader.Get("Anthropic-Beta") != betas {
				t.Fatalf("third-turn profile = %v, found=%v", thirdHeader, foundThird)
			}
		})
	}
}

func TestClaudeThreadBetaStoreEvictsOldestMessage(t *testing.T) {
	store := &ClaudeThreadBetaStore{}
	for i := range claudeThreadBetaLimit + 1 {
		request, _, _ := store.Bind("scope", "", http.Header{"Anthropic-Beta": []string{"stable-beta"}}, ClaudeThreadBetaPolicy{})
		request.Commit(fmt.Sprintf("message-%d", i))
	}
	if _, found, _ := store.Bind("scope", "message-0", http.Header{}, ClaudeThreadBetaPolicy{}); found {
		t.Fatal("oldest profile was not evicted")
	}
	if _, found, _ := store.Bind("scope", fmt.Sprintf("message-%d", claudeThreadBetaLimit), http.Header{}, ClaudeThreadBetaPolicy{}); !found {
		t.Fatal("newest profile was evicted")
	}
}

func TestClaudeThreadBetaStoreConcurrentCallers(t *testing.T) {
	store := &ClaudeThreadBetaStore{}
	var wg sync.WaitGroup
	for caller := range 16 {
		wg.Go(func() {
			scope := fmt.Sprintf("caller-%d", caller)
			request, _, _ := store.Bind(scope, "", http.Header{"Anthropic-Beta": []string{scope}}, ClaudeThreadBetaPolicy{})
			request.Commit("shared-message-id")
			for range 10 {
				header := http.Header{}
				if _, found, _ := store.Bind(scope, "shared-message-id", header, ClaudeThreadBetaPolicy{}); !found || header.Get("Anthropic-Beta") != scope {
					t.Errorf("profile crossed callers: %v, scope=%s", header, scope)
				}
			}
		})
	}
	wg.Wait()
}

func TestClaudeThreadOversizedCompletionDoesNotPublishState(t *testing.T) {
	const start = "data: {\"type\":\"message_start\",\"message\":{\"id\":\"message-a\"}}\n\n"
	const terminal = "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n"
	const stop = "data: {\"type\":\"message_stop\"}\n\n"
	const pingPrefix = "data: {\"type\":\"ping\",\"padding\":\""
	const pingSuffix = "\"}\n\n"
	// Each event fits, but message_stop pushes the pending completion over its cap.
	pingSize := (claudeThreadEventLimit - 1 - len(terminal)) / 2
	ping := pingPrefix + strings.Repeat("x", pingSize-len(pingPrefix)-len(pingSuffix)) + pingSuffix
	body := io.MultiReader(strings.NewReader(start+terminal), strings.NewReader(ping), strings.NewReader(ping), strings.NewReader(stop))
	store := &ClaudeThreadBetaStore{}
	request, _, _ := store.Bind("scope", "", http.Header{"Anthropic-Beta": []string{"profile"}}, ClaudeThreadBetaPolicy{})
	if _, errRead := io.Copy(io.Discard, request.StreamReader(body, nil)); errRead == nil {
		t.Fatal("oversized completion was accepted")
	}
	if _, found, _ := store.Bind("scope", "message-a", http.Header{}, ClaudeThreadBetaPolicy{}); found {
		t.Fatal("rejected oversized completion published Thread state")
	}
}

func TestClaudeThreadStreamNeverCommitsFailedTurn(t *testing.T) {
	const start = "data: {\"type\":\"message_start\",\"message\":{\"id\":\"message-a\"}}\n\n"
	const terminal = "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n"
	const stop = "data: {\"type\":\"message_stop\"}\n\n"
	for _, test := range []struct {
		name string
		body io.Reader
	}{
		{name: "truncated", body: strings.NewReader(start + terminal)},
		{name: "malformed stop", body: strings.NewReader(start + terminal + "data: {\"type\":\"message_stop\",broken}\n\n")},
		{name: "error after terminal", body: strings.NewReader(start + terminal + "data: {\"type\":\"error\",\"error\":{\"message\":\"failed\"}}\n\n")},
		{name: "policy refusal", body: strings.NewReader(start + "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"refusal\",\"stop_details\":{\"type\":\"refusal\",\"category\":\"cyber\"}}}\n\n" + stop)},
		{name: "canceled", body: io.MultiReader(strings.NewReader(start), iotest.ErrReader(context.Canceled))},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &ClaudeThreadBetaStore{}
			request, _, _ := store.Bind("scope", "", http.Header{"Anthropic-Beta": []string{"profile"}}, ClaudeThreadBetaPolicy{})
			data, errRead := io.ReadAll(request.StreamReader(test.body, nil))
			if test.name == "canceled" && !errors.Is(errRead, context.Canceled) {
				t.Fatalf("cancellation error = %v", errRead)
			}
			if test.name == "truncated" && (errRead == nil || strings.Contains(string(data), "message_delta")) {
				t.Fatalf("truncated turn released completion: data=%s, error=%v", data, errRead)
			}
			if _, found, _ := store.Bind("scope", "message-a", http.Header{}, ClaudeThreadBetaPolicy{}); found {
				t.Fatal("failed turn published continuation state")
			}
		})
	}
}
