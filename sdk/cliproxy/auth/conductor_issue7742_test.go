package auth

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestMergeRequestHeaders_DeletesExistingKeysCaseInsensitively_Issue7742(t *testing.T) {
	t.Run("replaces lowercase current key with mixed-case update key", func(t *testing.T) {
		current := http.Header{
			"x-session-id": []string{"old"},
		}
		updates := http.Header{
			"X-Session-ID": []string{"new"},
		}

		got := mergeRequestHeaders(current, updates, nil)

		if len(got) != 1 {
			t.Fatalf("len(mergeRequestHeaders) = %d, want 1; headers = %#v", len(got), got)
		}
		if _, stale := got["x-session-id"]; stale {
			t.Fatalf("stale non-canonical key x-session-id retained: %#v", got)
		}
		if want := []string{"new"}; !reflect.DeepEqual(got.Values("X-Session-ID"), want) {
			t.Fatalf("X-Session-ID values = %#v, want %#v; headers = %#v", got.Values("X-Session-ID"), want, got)
		}
		if want := []string{"old"}; !reflect.DeepEqual(current["x-session-id"], want) {
			t.Fatalf("caller current headers mutated: %#v", current)
		}
	})

	t.Run("replaces non-canonical uppercase current key with lowercase update key", func(t *testing.T) {
		current := http.Header{
			"X-Session-ID": []string{"old"},
		}
		updates := http.Header{
			"x-session-id": []string{"new"},
		}

		got := mergeRequestHeaders(current, updates, nil)

		if len(got) != 1 {
			t.Fatalf("len(mergeRequestHeaders) = %d, want 1; headers = %#v", len(got), got)
		}
		if _, stale := got["X-Session-ID"]; stale {
			t.Fatalf("stale non-canonical key X-Session-ID retained: %#v", got)
		}
		if want := []string{"new"}; !reflect.DeepEqual(got.Values("X-Session-ID"), want) {
			t.Fatalf("X-Session-ID values = %#v, want %#v; headers = %#v", got.Values("X-Session-ID"), want, got)
		}
	})

	t.Run("removes all case variants while preserving unrelated headers and multi-values", func(t *testing.T) {
		current := http.Header{
			"user-agent": []string{"old-lower"},
			"USER-AGENT": []string{"old-upper"},
			"x-keep":     []string{"stay"},
		}
		updates := http.Header{
			"User-Agent": []string{"new-a", "new-b"},
		}

		got := mergeRequestHeaders(current, updates, nil)

		if len(got) != 2 {
			t.Fatalf("len(mergeRequestHeaders) = %d, want 2; headers = %#v", len(got), got)
		}
		if _, stale := got["user-agent"]; stale {
			t.Fatalf("stale key user-agent retained: %#v", got)
		}
		if _, stale := got["USER-AGENT"]; stale {
			t.Fatalf("stale key USER-AGENT retained: %#v", got)
		}
		if want := []string{"new-a", "new-b"}; !reflect.DeepEqual(got.Values("User-Agent"), want) {
			t.Fatalf("User-Agent values = %#v, want %#v; headers = %#v", got.Values("User-Agent"), want, got)
		}
		if want := []string{"stay"}; !reflect.DeepEqual(got["x-keep"], want) {
			t.Fatalf("unrelated header x-keep = %#v, want %#v", got["x-keep"], want)
		}
	})

	t.Run("empty update value slice removes stale non-canonical key", func(t *testing.T) {
		current := http.Header{
			"x-session-id": []string{"old"},
		}
		updates := http.Header{
			"X-Session-ID": {},
		}

		got := mergeRequestHeaders(current, updates, nil)

		if len(got) != 0 {
			t.Fatalf("len(mergeRequestHeaders) = %d, want 0; headers = %#v", len(got), got)
		}
	})
}

func TestPrepareRequestAfterAuth_UpdatesNonCanonicalHeadersWithoutDuplicates_Issue7742(t *testing.T) {
	req := cliproxyexecutor.Request{
		Model: "gpt-5.6-luna",
	}
	opts := cliproxyexecutor.Options{
		Headers: http.Header{
			"x-session-id": []string{"old-session"},
			"user-agent":   []string{"old-agent"},
		},
		RequestAfterAuthInterceptor: func(context.Context, cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
			return cliproxyexecutor.RequestAfterAuthInterceptResponse{
				Headers: http.Header{
					"X-Session-ID": []string{"new-session"},
					"User-Agent":   []string{"new-agent"},
				},
			}
		},
	}

	_, finalOpts, errPrepare := prepareRequestAfterAuth(context.Background(), nil, nil, "openai", req, opts, "gpt-5.6-luna")
	if errPrepare != nil {
		t.Fatalf("prepareRequestAfterAuth error = %v", errPrepare)
	}
	if len(finalOpts.Headers) != 2 {
		t.Fatalf("len(finalOpts.Headers) = %d, want 2; headers = %#v", len(finalOpts.Headers), finalOpts.Headers)
	}
	if _, stale := finalOpts.Headers["x-session-id"]; stale {
		t.Fatalf("stale x-session-id retained: %#v", finalOpts.Headers)
	}
	if _, stale := finalOpts.Headers["user-agent"]; stale {
		t.Fatalf("stale user-agent retained: %#v", finalOpts.Headers)
	}
	if got := finalOpts.Headers.Get("X-Session-ID"); got != "new-session" {
		t.Fatalf("X-Session-ID = %q, want new-session", got)
	}
	if got := finalOpts.Headers.Get("User-Agent"); got != "new-agent" {
		t.Fatalf("User-Agent = %q, want new-agent", got)
	}
	if got := finalOpts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey]; got != "header:new-session" {
		t.Fatalf("CanonicalSessionIDMetadataKey = %v, want header:new-session", got)
	}
}
