package auth

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/MoeForge/moego/json"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type issue7733StructMetadata struct {
	Tags   []string
	hidden string
}

type issue7733MutatingSelector struct {
	mu                sync.Mutex
	order             []string
	seenBefore        []map[string]any
	sameAttemptAlias  bool
	reslicedEmptyElem any
	pickCounter       int
}

func (s *issue7733MutatingSelector) Pick(_ context.Context, _ string, _ string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seenBefore = append(s.seenBefore, cloneRequestMetadata(opts.Metadata))
	idx := s.pickCounter
	s.pickCounter++
	if idx == 0 && opts.Metadata != nil {
		opts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey] = "attempt-1-canonical"
		opts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey] = "attempt-1-parent"
		opts.Metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey] = "attempt-1-lcp"
		opts.Metadata[cliproxyexecutor.IsForkMetadataKey] = true
		opts.Metadata[cliproxyexecutor.NodeKindMetadataKey] = "fork"
		if nested, ok := opts.Metadata["routing"].(map[string]any); ok {
			nested["node"] = "mutated-by-attempt-1"
		}
		if tags, ok := opts.Metadata["tags"].([]string); ok && len(tags) > 0 {
			tags[0] = "mutated-tag-1"
		}
		if excludedBool, ok := opts.Metadata["excluded_bool"].(map[string]bool); ok {
			excludedBool["mutated-bool"] = true
		}
		if excludedSet, ok := opts.Metadata["excluded_set"].(map[string]struct{}); ok {
			excludedSet["mutated-set"] = struct{}{}
		}
		if routingTags, ok := opts.Metadata["routing_tags"].(map[string][]string); ok {
			if list := routingTags["tags"]; len(list) > 0 {
				list[0] = "mutated-routing-tag"
			}
			routingTags["extra"] = []string{"leaked"}
		}
		if arrTags, ok := opts.Metadata["array_tags"].([1][]string); ok && len(arrTags[0]) > 0 {
			arrTags[0][0] = "mutated-array-tag"
		}
		if stTags, ok := opts.Metadata["struct_tags"].(issue7733StructMetadata); ok && len(stTags.Tags) > 0 {
			stTags.Tags[0] = "mutated-struct-tag"
		}
		if sharedA, ok := opts.Metadata["shared_bool_a"].(map[string]bool); ok {
			sharedA["within_attempt"] = true
			if sharedB, okB := opts.Metadata["shared_bool_b"].(map[string]bool); okB && sharedB["within_attempt"] {
				s.sameAttemptAlias = true
			}
		}
		if emptyBuf, ok := opts.Metadata["empty_cap_buf"].([]any); ok && len(emptyBuf) == 0 && cap(emptyBuf) == 1 {
			grown := emptyBuf[:1]
			s.reslicedEmptyElem = grown[0]
			grown[0] = "mutated-spare-slot"
		}
	}
	if idx < len(s.order) {
		targetID := s.order[idx]
		for _, candidate := range auths {
			if candidate != nil && candidate.ID == targetID {
				return candidate, nil
			}
		}
	}
	for _, candidate := range auths {
		if candidate != nil {
			return candidate, nil
		}
	}
	return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
}

type issue7733ResultRecorderHook struct {
	NoopHook
	mu      sync.Mutex
	results []Result
}

func (h *issue7733ResultRecorderHook) OnResult(_ context.Context, result Result) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.results = append(h.results, result)
}

func (h *issue7733ResultRecorderHook) snapshot() []Result {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Result(nil), h.results...)
}

type issue7733HomeDispatcher struct {
	mu      sync.Mutex
	replies []homeAuthDispatchResponse
	calls   int
}

func (*issue7733HomeDispatcher) HeartbeatOK() bool { return true }

func (d *issue7733HomeDispatcher) RPopAuth(_ context.Context, _ string, _ string, _ http.Header, _ int) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.calls >= len(d.replies) {
		return nil, &Error{Code: "auth_not_found", Message: "no more home dispatches"}
	}
	reply := d.replies[d.calls]
	d.calls++
	return json.Marshal(reply)
}

func (*issue7733HomeDispatcher) AbortAmbiguousDispatch() {}

func TestIssue7733MixedLoopsIsolatePerAttemptMetadataOnSkip(t *testing.T) {
	for _, mode := range []string{"execute", "count", "stream"} {
		t.Run(mode, func(t *testing.T) {
			selector := &issue7733MutatingSelector{
				order: []string{"issue7733-skip-a", "issue7733-skip-b"},
			}
			manager := NewManager(nil, selector, nil)
			executor := &callbackSchedulerTestExecutor{
				schedulerTestExecutor: schedulerTestExecutor{provider: "issue7733-skip"},
				execute: func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
				},
				countTokens: func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					return cliproxyexecutor.Response{Payload: []byte(`{"input_tokens":1}`)}, nil
				},
				executeStream: func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
					chunks := make(chan cliproxyexecutor.StreamChunk, 1)
					chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {}\n\n")}
					close(chunks)
					return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
				},
			}
			manager.RegisterExecutor(executor)
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient("issue7733-skip-a", "issue7733-skip", []*registry.ModelInfo{{ID: "route-model"}})
			reg.RegisterClient("issue7733-skip-b", "issue7733-skip", []*registry.ModelInfo{{ID: "route-model"}})
			t.Cleanup(func() {
				reg.UnregisterClient("issue7733-skip-a")
				reg.UnregisterClient("issue7733-skip-b")
			})
			if _, errRegister := manager.Register(context.Background(), &Auth{
				ID:       "issue7733-skip-a",
				Provider: "issue7733-skip",
				Status:   StatusActive,
				Attributes: map[string]string{
					homeUpstreamModelAttributeKey: "disabled-upstream",
				},
				ModelStates: map[string]*ModelState{
					"disabled-upstream": {Status: StatusDisabled},
				},
			}); errRegister != nil {
				t.Fatalf("register skipped auth: %v", errRegister)
			}
			if _, errRegister := manager.Register(context.Background(), &Auth{
				ID:       "issue7733-skip-b",
				Provider: "issue7733-skip",
				Status:   StatusActive,
			}); errRegister != nil {
				t.Fatalf("register healthy auth: %v", errRegister)
			}

			callerRouting := map[string]any{"node": "base"}
			callerTags := []string{"base-tag"}
			callerExcludedBool := map[string]bool{"base-bool": true}
			callerExcludedSet := map[string]struct{}{"base-set": {}}
			callerRoutingTags := map[string][]string{"tags": {"base"}}
			callerArrayTags := [1][]string{{"base-array"}}
			callerStructTags := issue7733StructMetadata{Tags: []string{"base-struct"}, hidden: "keep"}
			callerSharedBool := map[string]bool{"base": true}
			callerEmptyCapBacking := []any{"spare-base"}
			callerEmptyCapBuf := callerEmptyCapBacking[:0]
			opts := cliproxyexecutor.Options{
				Stream: mode == "stream",
				Metadata: map[string]any{
					"caller_marker": "base",
					"routing":       callerRouting,
					"tags":          callerTags,
					"excluded_bool": callerExcludedBool,
					"excluded_set":  callerExcludedSet,
					"routing_tags":  callerRoutingTags,
					"array_tags":    callerArrayTags,
					"struct_tags":   callerStructTags,
					"shared_bool_a": callerSharedBool,
					"shared_bool_b": callerSharedBool,
					"empty_cap_buf": callerEmptyCapBuf,
				},
			}
			req := cliproxyexecutor.Request{Model: "route-model"}
			var errRun error
			switch mode {
			case "execute":
				_, errRun = manager.Execute(context.Background(), []string{"issue7733-skip"}, req, opts)
			case "count":
				_, errRun = manager.ExecuteCount(context.Background(), []string{"issue7733-skip"}, req, opts)
			case "stream":
				var stream *cliproxyexecutor.StreamResult
				stream, errRun = manager.ExecuteStream(context.Background(), []string{"issue7733-skip"}, req, opts)
				if errRun == nil {
					for range stream.Chunks {
					}
				}
			}
			if errRun != nil {
				t.Fatalf("%s failed: %v", mode, errRun)
			}
			if len(selector.seenBefore) != 2 {
				t.Fatalf("selector pick count = %d, want 2", len(selector.seenBefore))
			}
			secondMeta := selector.seenBefore[1]
			if secondMeta["caller_marker"] != "base" {
				t.Fatalf("second attempt lost base metadata: %#v", secondMeta)
			}
			if routing, ok := secondMeta["routing"].(map[string]any); !ok || routing["node"] != "base" {
				t.Fatalf("second attempt observed mutated nested map: %#v", secondMeta["routing"])
			}
			if tags, ok := secondMeta["tags"].([]string); !ok || len(tags) != 1 || tags[0] != "base-tag" {
				t.Fatalf("second attempt observed mutated slice: %#v", secondMeta["tags"])
			}
			if callerRouting["node"] != "base" {
				t.Fatalf("caller nested map was mutated: %#v", callerRouting)
			}
			if callerTags[0] != "base-tag" {
				t.Fatalf("caller slice was mutated: %#v", callerTags)
			}
			if excludedBool, ok := secondMeta["excluded_bool"].(map[string]bool); !ok || len(excludedBool) != 1 || !excludedBool["base-bool"] {
				t.Fatalf("second attempt observed mutated map[string]bool: %#v", secondMeta["excluded_bool"])
			}
			if excludedSet, ok := secondMeta["excluded_set"].(map[string]struct{}); !ok || len(excludedSet) != 1 {
				t.Fatalf("second attempt observed mutated map[string]struct{}: %#v", secondMeta["excluded_set"])
			}
			if _, mutated := callerExcludedBool["mutated-bool"]; mutated {
				t.Fatalf("caller map[string]bool was mutated: %#v", callerExcludedBool)
			}
			if _, mutated := callerExcludedSet["mutated-set"]; mutated {
				t.Fatalf("caller map[string]struct{} was mutated: %#v", callerExcludedSet)
			}
			if routingTags, ok := secondMeta["routing_tags"].(map[string][]string); !ok || len(routingTags) != 1 || len(routingTags["tags"]) != 1 || routingTags["tags"][0] != "base" {
				t.Fatalf("second attempt observed mutated map[string][]string: %#v", secondMeta["routing_tags"])
			}
			if len(callerRoutingTags) != 1 || len(callerRoutingTags["tags"]) != 1 || callerRoutingTags["tags"][0] != "base" {
				t.Fatalf("caller map[string][]string was mutated: %#v", callerRoutingTags)
			}
			if arrTags, ok := secondMeta["array_tags"].([1][]string); !ok || len(arrTags[0]) != 1 || arrTags[0][0] != "base-array" {
				t.Fatalf("second attempt observed mutated [1][]string: %#v", secondMeta["array_tags"])
			}
			if len(callerArrayTags[0]) != 1 || callerArrayTags[0][0] != "base-array" {
				t.Fatalf("caller [1][]string was mutated: %#v", callerArrayTags)
			}
			if stTags, ok := secondMeta["struct_tags"].(issue7733StructMetadata); !ok || len(stTags.Tags) != 1 || stTags.Tags[0] != "base-struct" || stTags.hidden != "keep" {
				t.Fatalf("second attempt observed mutated struct: %#v", secondMeta["struct_tags"])
			}
			if len(callerStructTags.Tags) != 1 || callerStructTags.Tags[0] != "base-struct" {
				t.Fatalf("caller struct slice was mutated: %#v", callerStructTags)
			}
			if !selector.sameAttemptAlias {
				t.Fatalf("shared map[string]bool keys lost intra-attempt aliasing after clone")
			}
			if _, mutated := callerSharedBool["within_attempt"]; mutated {
				t.Fatalf("caller shared map[string]bool was mutated: %#v", callerSharedBool)
			}
			if secondShared, ok := secondMeta["shared_bool_b"].(map[string]bool); !ok || secondShared["within_attempt"] || !secondShared["base"] {
				t.Fatalf("second attempt observed mutated shared map[string]bool: %#v", secondMeta["shared_bool_b"])
			}
			if selector.reslicedEmptyElem != "spare-base" {
				t.Fatalf("first attempt empty-len non-zero-cap reslice = %#v, want spare-base", selector.reslicedEmptyElem)
			}
			if callerEmptyCapBacking[0] != "spare-base" {
				t.Fatalf("caller spare slice capacity element was mutated: %#v", callerEmptyCapBacking)
			}
			if secondBuf, ok := secondMeta["empty_cap_buf"].([]any); !ok || len(secondBuf) != 0 || cap(secondBuf) != 1 || secondBuf[:1][0] != "spare-base" {
				t.Fatalf("second attempt empty_cap_buf = %#v (len=%d cap=%d), want len=0 cap=1 spare-base", secondMeta["empty_cap_buf"], len(secondBuf), cap(secondBuf))
			}
			for _, leakedKey := range []string{
				cliproxyexecutor.SelectedAuthMetadataKey,
				cliproxyexecutor.SelectedAuthIndexMetadataKey,
				cliproxyexecutor.CanonicalSessionIDMetadataKey,
				cliproxyexecutor.ParentSessionIDMetadataKey,
				cliproxyexecutor.LCPAffinitySessionIDMetadataKey,
				cliproxyexecutor.IsForkMetadataKey,
				cliproxyexecutor.NodeKindMetadataKey,
			} {
				if val, exists := secondMeta[leakedKey]; exists {
					t.Fatalf("second attempt inherited mutated metadata key %q = %#v from skipped attempt", leakedKey, val)
				}
			}
		})
	}
}

func TestIssue7733MixedLoopsIsolateSelectedAuthMetadataOnFailover(t *testing.T) {
	for _, mode := range []string{"execute", "count", "stream"} {
		t.Run(mode, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			hook := &issue7733ResultRecorderHook{}
			removeHook := manager.AddHook(hook)
			t.Cleanup(removeHook)

			scheduler := &fakePluginScheduler{
				pick: func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
					if len(req.Candidates) == 0 {
						return pluginapi.SchedulerPickResponse{}, false, nil
					}
					for _, candidate := range req.Candidates {
						if candidate.ID == "issue7733-fail-a" {
							return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "issue7733-fail-a"}, true, nil
						}
					}
					return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "issue7733-fail-b"}, true, nil
				},
			}
			manager.SetPluginScheduler(scheduler)

			retryableErr := &Error{
				Code:       "upstream_unavailable",
				Message:    "temporary failure",
				Retryable:  true,
				HTTPStatus: http.StatusServiceUnavailable,
			}
			executor := &callbackSchedulerTestExecutor{
				schedulerTestExecutor: schedulerTestExecutor{provider: "issue7733-fail"},
				execute: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					if auth.ID == "issue7733-fail-a" {
						return cliproxyexecutor.Response{}, retryableErr
					}
					return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
				},
				countTokens: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					if auth.ID == "issue7733-fail-a" {
						return cliproxyexecutor.Response{}, retryableErr
					}
					return cliproxyexecutor.Response{Payload: []byte(`{"input_tokens":1}`)}, nil
				},
				executeStream: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
					if auth.ID == "issue7733-fail-a" {
						return nil, retryableErr
					}
					chunks := make(chan cliproxyexecutor.StreamChunk, 1)
					chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {}\n\n")}
					close(chunks)
					return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
				},
			}
			manager.RegisterExecutor(executor)
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient("issue7733-fail-a", "issue7733-fail", []*registry.ModelInfo{{ID: "route-model"}})
			reg.RegisterClient("issue7733-fail-b", "issue7733-fail", []*registry.ModelInfo{{ID: "route-model"}})
			t.Cleanup(func() {
				reg.UnregisterClient("issue7733-fail-a")
				reg.UnregisterClient("issue7733-fail-b")
			})
			if _, errRegister := manager.Register(context.Background(), &Auth{
				ID:       "issue7733-fail-a",
				Provider: "issue7733-fail",
				Status:   StatusActive,
			}); errRegister != nil {
				t.Fatalf("register first auth: %v", errRegister)
			}
			if _, errRegister := manager.Register(context.Background(), &Auth{
				ID:       "issue7733-fail-b",
				Provider: "issue7733-fail",
				Status:   StatusActive,
			}); errRegister != nil {
				t.Fatalf("register second auth: %v", errRegister)
			}

			opts := cliproxyexecutor.Options{
				Stream: mode == "stream",
				Metadata: map[string]any{
					"caller_marker": "base",
				},
			}
			req := cliproxyexecutor.Request{Model: "route-model"}
			var errRun error
			switch mode {
			case "execute":
				_, errRun = manager.Execute(context.Background(), []string{"issue7733-fail"}, req, opts)
			case "count":
				_, errRun = manager.ExecuteCount(context.Background(), []string{"issue7733-fail"}, req, opts)
			case "stream":
				var stream *cliproxyexecutor.StreamResult
				stream, errRun = manager.ExecuteStream(context.Background(), []string{"issue7733-fail"}, req, opts)
				if errRun == nil {
					for range stream.Chunks {
					}
				}
			}
			if errRun != nil {
				t.Fatalf("%s failed: %v", mode, errRun)
			}
			if len(scheduler.requests) != 2 {
				t.Fatalf("scheduler PickAuth calls = %d, want 2", len(scheduler.requests))
			}
			firstPickID, _ := scheduler.requests[0].Options.Metadata[cliproxyexecutor.SelectionIDMetadataKey].(string)
			secondPickMeta := scheduler.requests[1].Options.Metadata
			secondPickID, _ := secondPickMeta[cliproxyexecutor.SelectionIDMetadataKey].(string)
			if firstPickID == "" || secondPickID == "" || firstPickID == secondPickID {
				t.Fatalf("selection IDs = (%q, %q), want distinct non-empty values", firstPickID, secondPickID)
			}
			// Non-Home mid-round re-picks retain SelectedAuthMetadataKey for scheduler plugins (#7434).
			if got := secondPickMeta[cliproxyexecutor.SelectedAuthMetadataKey]; got != "issue7733-fail-a" {
				t.Fatalf("second PickAuth SelectedAuthMetadataKey = %#v, want issue7733-fail-a", got)
			}
			if val, exists := secondPickMeta[cliproxyexecutor.SelectedAuthIndexMetadataKey]; exists {
				t.Fatalf("second PickAuth inherited %q = %#v from failed first attempt", cliproxyexecutor.SelectedAuthIndexMetadataKey, val)
			}
			results := hook.snapshot()
			if len(results) != 2 {
				t.Fatalf("recorded results = %d, want 2", len(results))
			}
			if got := results[0].Options.Metadata[cliproxyexecutor.SelectedAuthMetadataKey]; got != "issue7733-fail-a" {
				t.Fatalf("first result selected auth metadata = %#v, want issue7733-fail-a", got)
			}
			if got := results[0].Options.Metadata[cliproxyexecutor.SelectionIDMetadataKey]; got != firstPickID {
				t.Fatalf("first result selection_id = %#v, want %q", got, firstPickID)
			}
			if got := results[1].Options.Metadata[cliproxyexecutor.SelectedAuthMetadataKey]; got != "issue7733-fail-b" {
				t.Fatalf("second result selected auth metadata = %#v, want issue7733-fail-b", got)
			}
			if got := results[1].Options.Metadata[cliproxyexecutor.SelectionIDMetadataKey]; got != secondPickID {
				t.Fatalf("second result selection_id = %#v, want %q", got, secondPickID)
			}
		})
	}
}

func TestIssue7733ExecuteHomeOnceClonesMetadataPerAttempt(t *testing.T) {
	for _, mode := range []string{"execute", "count", "stream"} {
		t.Run(mode, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			hook := &issue7733ResultRecorderHook{}
			removeHook := manager.AddHook(hook)
			t.Cleanup(removeHook)

			dispatcher := &issue7733HomeDispatcher{
				replies: []homeAuthDispatchResponse{
					{
						SelectionID: "home-sel-1",
						Auth: Auth{
							ID:       "home-first",
							Provider: "issue7733-home",
							Status:   StatusActive,
						},
					},
					{
						SelectionID: "home-sel-2",
						Auth: Auth{
							ID:       "home-second",
							Provider: "issue7733-home",
							Status:   StatusActive,
						},
					},
				},
			}
			manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)

			retryableErr := &Error{
				Code:       "upstream_unavailable",
				Message:    "temporary home failure",
				Retryable:  true,
				HTTPStatus: http.StatusServiceUnavailable,
			}
			executor := &callbackSchedulerTestExecutor{
				schedulerTestExecutor: schedulerTestExecutor{provider: "issue7733-home"},
				execute: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					if auth.ID == "home-first" {
						return cliproxyexecutor.Response{}, retryableErr
					}
					return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
				},
				countTokens: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					if auth.ID == "home-first" {
						return cliproxyexecutor.Response{}, retryableErr
					}
					return cliproxyexecutor.Response{Payload: []byte(`{"input_tokens":1}`)}, nil
				},
				executeStream: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
					if auth.ID == "home-first" {
						return nil, retryableErr
					}
					chunks := make(chan cliproxyexecutor.StreamChunk, 1)
					chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {}\n\n")}
					close(chunks)
					return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
				},
			}
			manager.RegisterExecutor(executor)

			callerMeta := map[string]any{"caller_marker": "base"}
			opts := cliproxyexecutor.Options{Metadata: callerMeta}
			req := cliproxyexecutor.Request{Model: "model-a"}
			var errRun error
			switch mode {
			case "execute":
				_, errRun = manager.Execute(context.Background(), []string{"issue7733-home"}, req, opts)
			case "count":
				_, errRun = manager.ExecuteCount(context.Background(), []string{"issue7733-home"}, req, opts)
			case "stream":
				var stream *cliproxyexecutor.StreamResult
				stream, errRun = manager.ExecuteStream(context.Background(), []string{"issue7733-home"}, req, opts)
				if errRun == nil {
					for range stream.Chunks {
					}
				}
			}
			if errRun != nil {
				t.Fatalf("%s failed: %v", mode, errRun)
			}

			results := hook.snapshot()
			if len(results) != 2 {
				t.Fatalf("recorded results = %d, want 2", len(results))
			}
			if got := results[0].Options.Metadata[cliproxyexecutor.SelectedAuthMetadataKey]; got != "home-first" {
				t.Fatalf("first result SelectedAuthMetadataKey mutated to %#v, want home-first", got)
			}
			if got := results[0].Options.Metadata[cliproxyexecutor.SelectionIDMetadataKey]; got != "home-sel-1" {
				t.Fatalf("first result SelectionIDMetadataKey mutated to %#v, want home-sel-1", got)
			}
			if got := results[1].Options.Metadata[cliproxyexecutor.SelectedAuthMetadataKey]; got != "home-second" {
				t.Fatalf("second result SelectedAuthMetadataKey = %#v, want home-second", got)
			}
			if got := results[1].Options.Metadata[cliproxyexecutor.SelectionIDMetadataKey]; got != "home-sel-2" {
				t.Fatalf("second result SelectionIDMetadataKey = %#v, want home-sel-2", got)
			}
			if _, mutated := callerMeta[cliproxyexecutor.SelectedAuthMetadataKey]; mutated {
				t.Fatalf("caller metadata was mutated: %#v", callerMeta)
			}
		})
	}
}

type issue7733PreparingHomeStreamExecutor struct {
	callbackSchedulerTestExecutor
	prepareErrByID map[string]error
}

func (e *issue7733PreparingHomeStreamExecutor) ShouldPrepareRequestAuth(auth *Auth) bool {
	return auth != nil && e.prepareErrByID[auth.ID] != nil
}

func (e *issue7733PreparingHomeStreamExecutor) PrepareRequestAuth(_ context.Context, auth *Auth) (*Auth, error) {
	if auth != nil {
		if errPrepare := e.prepareErrByID[auth.ID]; errPrepare != nil {
			return nil, errPrepare
		}
	}
	return auth, nil
}

func TestIssue7733HomeStreamPrepareFailurePublishesAttemptMetadata(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	hook := &issue7733ResultRecorderHook{}
	removeHook := manager.AddHook(hook)
	t.Cleanup(removeHook)

	firstAuth := Auth{
		ID:       "home-prep-first",
		Provider: "issue7733-home-prep",
		Status:   StatusActive,
	}
	secondAuth := Auth{
		ID:       "home-prep-second",
		Provider: "issue7733-home-prep",
		Status:   StatusActive,
	}
	wantFirstIndex := (&Auth{ID: firstAuth.ID, Provider: firstAuth.Provider, Status: firstAuth.Status}).EnsureIndex()
	wantSecondIndex := (&Auth{ID: secondAuth.ID, Provider: secondAuth.Provider, Status: secondAuth.Status}).EnsureIndex()
	dispatcher := &issue7733HomeDispatcher{
		replies: []homeAuthDispatchResponse{
			{SelectionID: "home-prep-sel-1", Auth: firstAuth},
			{SelectionID: "home-prep-sel-2", Auth: secondAuth},
		},
	}
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)

	prepareErr := &Error{
		Code:       "upstream_unavailable",
		Message:    "temporary prepare failure",
		Retryable:  true,
		HTTPStatus: http.StatusServiceUnavailable,
	}
	executor := &issue7733PreparingHomeStreamExecutor{
		callbackSchedulerTestExecutor: callbackSchedulerTestExecutor{
			schedulerTestExecutor: schedulerTestExecutor{provider: "issue7733-home-prep"},
			executeStream: func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				chunks := make(chan cliproxyexecutor.StreamChunk, 1)
				chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {}\n\n")}
				close(chunks)
				return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
			},
		},
		prepareErrByID: map[string]error{"home-prep-first": prepareErr},
	}
	manager.RegisterExecutor(executor)

	var callbackMu sync.Mutex
	var callbackIDs []string
	var callbackIndices []string
	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.SelectedAuthMetadataKey:      "stale-caller-auth",
			cliproxyexecutor.SelectedAuthIndexMetadataKey: "stale-caller-index",
			cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(id string) {
				callbackMu.Lock()
				callbackIDs = append(callbackIDs, id)
				callbackMu.Unlock()
			},
			cliproxyexecutor.SelectedAuthIndexCallbackMetadataKey: func(idx string) {
				callbackMu.Lock()
				callbackIndices = append(callbackIndices, idx)
				callbackMu.Unlock()
			},
		},
	}
	stream, errStream := manager.ExecuteStream(context.Background(), []string{"issue7733-home-prep"}, cliproxyexecutor.Request{Model: "model-a"}, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream() failed: %v", errStream)
	}
	for range stream.Chunks {
	}

	results := hook.snapshot()
	if len(results) != 2 {
		t.Fatalf("recorded results = %d, want 2", len(results))
	}
	if got := results[0].Options.Metadata[cliproxyexecutor.SelectedAuthMetadataKey]; got != "home-prep-first" {
		t.Fatalf("prepare-failure result SelectedAuthMetadataKey = %#v, want home-prep-first", got)
	}
	if got := results[0].Options.Metadata[cliproxyexecutor.SelectedAuthIndexMetadataKey]; got != wantFirstIndex {
		t.Fatalf("prepare-failure result SelectedAuthIndexMetadataKey = %#v, want %q", got, wantFirstIndex)
	}
	if got := results[0].Options.Metadata[cliproxyexecutor.SelectionIDMetadataKey]; got != "home-prep-sel-1" {
		t.Fatalf("prepare-failure result SelectionIDMetadataKey = %#v, want home-prep-sel-1", got)
	}
	if got := results[1].Options.Metadata[cliproxyexecutor.SelectedAuthMetadataKey]; got != "home-prep-second" {
		t.Fatalf("second result SelectedAuthMetadataKey = %#v, want home-prep-second", got)
	}
	if got := results[1].Options.Metadata[cliproxyexecutor.SelectedAuthIndexMetadataKey]; got != wantSecondIndex {
		t.Fatalf("second result SelectedAuthIndexMetadataKey = %#v, want %q", got, wantSecondIndex)
	}
	if got := results[1].Options.Metadata[cliproxyexecutor.SelectionIDMetadataKey]; got != "home-prep-sel-2" {
		t.Fatalf("second result SelectionIDMetadataKey = %#v, want home-prep-sel-2", got)
	}
	callbackMu.Lock()
	gotIDs := slices.Clone(callbackIDs)
	gotIndices := slices.Clone(callbackIndices)
	callbackMu.Unlock()
	if !slices.Equal(gotIDs, []string{"home-prep-first", "home-prep-second"}) {
		t.Fatalf("selected auth callbacks = %v, want [home-prep-first home-prep-second]", gotIDs)
	}
	if !slices.Equal(gotIndices, []string{wantFirstIndex, wantSecondIndex}) {
		t.Fatalf("selected auth index callbacks = %v, want [%s %s]", gotIndices, wantFirstIndex, wantSecondIndex)
	}
}

func TestIssue7733MidRoundSelectedAuthIDTrimsWhitespace(t *testing.T) {
	selector := &issue7733MutatingSelector{
		order: []string{"  issue7733-trim-a  ", "issue7733-trim-b"},
	}
	scheduler := &fakePluginScheduler{
		pick: func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
			if len(req.Candidates) == 2 {
				return pluginapi.SchedulerPickResponse{}, false, nil
			}
			return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "issue7733-trim-b"}, true, nil
		},
	}
	manager := NewManager(nil, selector, nil)
	manager.SetPluginScheduler(scheduler)

	retryableErr := &Error{
		Code:       "upstream_unavailable",
		Message:    "temporary failure",
		Retryable:  true,
		HTTPStatus: http.StatusServiceUnavailable,
	}
	executor := &callbackSchedulerTestExecutor{
		schedulerTestExecutor: schedulerTestExecutor{provider: "issue7733-trim"},
		execute: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			if auth.ID == "  issue7733-trim-a  " {
				return cliproxyexecutor.Response{}, retryableErr
			}
			return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
		},
	}
	manager.RegisterExecutor(executor)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("issue7733-trim-a", "issue7733-trim", []*registry.ModelInfo{{ID: "route-model"}})
	reg.RegisterClient("issue7733-trim-b", "issue7733-trim", []*registry.ModelInfo{{ID: "route-model"}})
	t.Cleanup(func() {
		reg.UnregisterClient("issue7733-trim-a")
		reg.UnregisterClient("issue7733-trim-b")
	})

	if _, errReg := manager.Register(context.Background(), &Auth{ID: "  issue7733-trim-a  ", Provider: "issue7733-trim", Status: StatusActive}); errReg != nil {
		t.Fatalf("register first auth: %v", errReg)
	}
	if _, errReg := manager.Register(context.Background(), &Auth{ID: "issue7733-trim-b", Provider: "issue7733-trim", Status: StatusActive}); errReg != nil {
		t.Fatalf("register second auth: %v", errReg)
	}

	if _, errExec := manager.Execute(context.Background(), []string{"issue7733-trim"}, cliproxyexecutor.Request{Model: "route-model"}, cliproxyexecutor.Options{}); errExec != nil {
		t.Fatalf("Execute() failed: %v", errExec)
	}
	if len(scheduler.requests) != 2 {
		t.Fatalf("scheduler PickAuth calls = %d, want 2", len(scheduler.requests))
	}
	if got := scheduler.requests[1].Options.Metadata[cliproxyexecutor.SelectedAuthMetadataKey]; got != "issue7733-trim-a" {
		t.Fatalf("second PickAuth SelectedAuthMetadataKey = %#v, want trimmed issue7733-trim-a", got)
	}
}

func TestIssue7733CloneRequestMetadataHandlesCycles(t *testing.T) {
	root := map[string]any{"label": "root"}
	nestedMap := map[string]any{"name": "nested"}
	nestedMap["self"] = nestedMap
	nestedMap["parent"] = root
	cyclicSlice := make([]any, 2)
	cyclicSlice[0] = "item"
	cyclicSlice[1] = cyclicSlice
	root["self"] = root
	root["nested"] = nestedMap
	root["slice"] = cyclicSlice

	cloned := cloneRequestMetadata(root)
	if cloned["label"] != "root" {
		t.Fatalf("cloned label = %#v, want root", cloned["label"])
	}
	clonedSelf, ok := cloned["self"].(map[string]any)
	if !ok {
		t.Fatalf("cloned self type = %T, want map[string]any", cloned["self"])
	}
	clonedSelf["label"] = "mutated-clone"
	if root["label"] != "root" {
		t.Fatalf("mutating cyclic clone mutated original root map: %#v", root["label"])
	}
	if cloned["label"] != "mutated-clone" {
		t.Fatalf("cloned self reference did not point to cloned root")
	}

	clonedNested, ok := cloned["nested"].(map[string]any)
	if !ok {
		t.Fatalf("cloned nested type = %T, want map[string]any", cloned["nested"])
	}
	clonedNestedSelf, ok := clonedNested["self"].(map[string]any)
	if !ok {
		t.Fatalf("cloned nested self type = %T, want map[string]any", clonedNested["self"])
	}
	clonedNestedSelf["name"] = "mutated-nested"
	if nestedMap["name"] != "nested" {
		t.Fatalf("mutating cyclic nested clone mutated original nested map: %#v", nestedMap["name"])
	}
	if clonedNested["name"] != "mutated-nested" {
		t.Fatalf("cloned nested self reference did not point to cloned nested map")
	}

	clonedSlice, ok := cloned["slice"].([]any)
	if !ok || len(clonedSlice) != 2 {
		t.Fatalf("cloned slice = %#v, want len 2 []any", cloned["slice"])
	}
	clonedSliceSelf, ok := clonedSlice[1].([]any)
	if !ok || len(clonedSliceSelf) != 2 {
		t.Fatalf("cloned cyclic slice reference = %#v, want len 2 []any", clonedSlice[1])
	}
	clonedSliceSelf[0] = "mutated-item"
	if cyclicSlice[0] != "item" {
		t.Fatalf("mutating cyclic slice clone mutated original slice: %#v", cyclicSlice[0])
	}
	if clonedSlice[0] != "mutated-item" {
		t.Fatalf("cloned slice self reference did not point to cloned slice")
	}

	type cyclicTypedMap map[string]any
	typedCyclic := cyclicTypedMap{"k": "v"}
	typedCyclic["self"] = typedCyclic
	rootWithTyped := map[string]any{"typed": typedCyclic}
	clonedTypedRoot := cloneRequestMetadata(rootWithTyped)
	clonedTyped, ok := clonedTypedRoot["typed"].(cyclicTypedMap)
	if !ok {
		t.Fatalf("cloned typed cyclic map type = %T, want cyclicTypedMap", clonedTypedRoot["typed"])
	}
	clonedTyped["k"] = "changed"
	if typedCyclic["k"] != "v" {
		t.Fatalf("mutating cloned typed cyclic map mutated original: %#v", typedCyclic["k"])
	}
	if selfRef, okSelf := clonedTyped["self"].(cyclicTypedMap); !okSelf || selfRef["k"] != "changed" {
		t.Fatalf("cloned typed cyclic self reference broken: %#v", clonedTyped["self"])
	}

	rootTyped := cyclicTypedMap{"label": "root"}
	rootTyped["self"] = rootTyped
	clonedNamedRoot := cloneRequestMetadata(rootTyped)
	clonedNamedSelf, okNamed := clonedNamedRoot["self"].(cyclicTypedMap)
	if !okNamed {
		t.Fatalf("cloned named root self type = %T, want cyclicTypedMap", clonedNamedRoot["self"])
	}
	clonedNamedSelf["label"] = "mutated-named-root"
	if rootTyped["label"] != "root" {
		t.Fatalf("mutating cloned named root self mutated original: %#v", rootTyped["label"])
	}
	if clonedNamedRoot["label"] != "mutated-named-root" {
		t.Fatalf("cloned named root self did not alias cloned root: %#v", clonedNamedRoot["label"])
	}

	type cyclicTypedSlice []any
	namedSlice := make(cyclicTypedSlice, 2)
	namedSlice[0] = []any(namedSlice)
	namedSlice[1] = "orig"
	clonedNamedSliceRoot := cloneRequestMetadata(map[string]any{"s": namedSlice})
	clonedNamedSlice, okSlice := clonedNamedSliceRoot["s"].(cyclicTypedSlice)
	if !okSlice || len(clonedNamedSlice) != 2 {
		t.Fatalf("cloned named slice = %#v, want len 2 cyclicTypedSlice", clonedNamedSliceRoot["s"])
	}
	convertedView, okView := clonedNamedSlice[0].([]any)
	if !okView || len(convertedView) != 2 {
		t.Fatalf("cloned converted slice view = %#v, want len 2 []any", clonedNamedSlice[0])
	}
	convertedView[1] = "mutated-view"
	if namedSlice[1] != "orig" {
		t.Fatalf("mutating cloned converted slice view mutated original: %#v", namedSlice[1])
	}
	if clonedNamedSlice[1] != "mutated-view" {
		t.Fatalf("cloned converted slice view did not alias cloned named slice: %#v", clonedNamedSlice[1])
	}
}

func TestIssue7733FailThenSkipClearsMidRoundSelectedAuthSignal(t *testing.T) {
	for _, mode := range []string{"execute", "count", "stream"} {
		t.Run(mode, func(t *testing.T) {
			scheduler := &fakePluginScheduler{
				pick: func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
					for _, wanted := range []string{"issue7733-fs-a", "issue7733-fs-b", "issue7733-fs-c"} {
						for _, candidate := range req.Candidates {
							if candidate.ID == wanted {
								return pluginapi.SchedulerPickResponse{Handled: true, AuthID: wanted}, true, nil
							}
						}
					}
					return pluginapi.SchedulerPickResponse{}, false, nil
				},
			}
			manager := NewManager(nil, nil, nil)
			manager.SetPluginScheduler(scheduler)

			retryableErr := &Error{
				Code:       "upstream_unavailable",
				Message:    "temporary failure",
				Retryable:  true,
				HTTPStatus: http.StatusServiceUnavailable,
			}
			executor := &callbackSchedulerTestExecutor{
				schedulerTestExecutor: schedulerTestExecutor{provider: "issue7733-fs"},
				execute: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					if auth.ID == "issue7733-fs-a" {
						return cliproxyexecutor.Response{}, retryableErr
					}
					return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
				},
				countTokens: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					if auth.ID == "issue7733-fs-a" {
						return cliproxyexecutor.Response{}, retryableErr
					}
					return cliproxyexecutor.Response{Payload: []byte(`{"input_tokens":1}`)}, nil
				},
				executeStream: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
					if auth.ID == "issue7733-fs-a" {
						return nil, retryableErr
					}
					chunks := make(chan cliproxyexecutor.StreamChunk, 1)
					chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {}\n\n")}
					close(chunks)
					return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
				},
			}
			manager.RegisterExecutor(executor)
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient("issue7733-fs-a", "issue7733-fs", []*registry.ModelInfo{{ID: "route-model"}})
			reg.RegisterClient("issue7733-fs-b", "issue7733-fs", []*registry.ModelInfo{{ID: "route-model"}})
			reg.RegisterClient("issue7733-fs-c", "issue7733-fs", []*registry.ModelInfo{{ID: "route-model"}})
			t.Cleanup(func() {
				reg.UnregisterClient("issue7733-fs-a")
				reg.UnregisterClient("issue7733-fs-b")
				reg.UnregisterClient("issue7733-fs-c")
			})

			if _, errReg := manager.Register(context.Background(), &Auth{
				ID:       "issue7733-fs-a",
				Provider: "issue7733-fs",
				Status:   StatusActive,
			}); errReg != nil {
				t.Fatalf("register auth a: %v", errReg)
			}
			if _, errReg := manager.Register(context.Background(), &Auth{
				ID:       "issue7733-fs-b",
				Provider: "issue7733-fs",
				Status:   StatusActive,
				Attributes: map[string]string{
					homeUpstreamModelAttributeKey: "disabled-upstream",
				},
				ModelStates: map[string]*ModelState{
					"disabled-upstream": {Status: StatusDisabled},
				},
			}); errReg != nil {
				t.Fatalf("register skipped auth b: %v", errReg)
			}
			if _, errReg := manager.Register(context.Background(), &Auth{
				ID:       "issue7733-fs-c",
				Provider: "issue7733-fs",
				Status:   StatusActive,
			}); errReg != nil {
				t.Fatalf("register auth c: %v", errReg)
			}

			opts := cliproxyexecutor.Options{Stream: mode == "stream"}
			req := cliproxyexecutor.Request{Model: "route-model"}
			var errRun error
			switch mode {
			case "execute":
				_, errRun = manager.Execute(context.Background(), []string{"issue7733-fs"}, req, opts)
			case "count":
				_, errRun = manager.ExecuteCount(context.Background(), []string{"issue7733-fs"}, req, opts)
			case "stream":
				var stream *cliproxyexecutor.StreamResult
				stream, errRun = manager.ExecuteStream(context.Background(), []string{"issue7733-fs"}, req, opts)
				if errRun == nil {
					for range stream.Chunks {
					}
				}
			}
			if errRun != nil {
				t.Fatalf("%s failed: %v", mode, errRun)
			}
			if len(scheduler.requests) != 3 {
				t.Fatalf("scheduler PickAuth calls = %d, want 3", len(scheduler.requests))
			}
			if val, exists := scheduler.requests[0].Options.Metadata[cliproxyexecutor.SelectedAuthMetadataKey]; exists {
				t.Fatalf("first PickAuth unexpectedly saw SelectedAuthMetadataKey = %#v", val)
			}
			if got := scheduler.requests[1].Options.Metadata[cliproxyexecutor.SelectedAuthMetadataKey]; got != "issue7733-fs-a" {
				t.Fatalf("second PickAuth SelectedAuthMetadataKey = %#v, want issue7733-fs-a", got)
			}
			if val, exists := scheduler.requests[2].Options.Metadata[cliproxyexecutor.SelectedAuthMetadataKey]; exists {
				t.Fatalf("third PickAuth after skipped attempt inherited SelectedAuthMetadataKey = %#v", val)
			}
		})
	}
}
