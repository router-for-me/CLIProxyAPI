package pluginhost

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type mockPluginScheduler struct{}

func (m *mockPluginScheduler) PickAuth(ctx context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
	return pluginapi.SchedulerPickResponse{}, false, nil
}

func TestHostAffinityLookupCallback_Contract(t *testing.T) {
	host := New()
	manager := coreauth.NewManager(nil, nil, nil)
	selector := coreauth.NewSessionAffinitySelector(nil)
	manager.SetSelector(selector)
	host.SetAuthManager(manager)

	authA := &coreauth.Auth{
		ID:       "claude-owner-a@example.com.json",
		Provider: "anthropic",
		FileName: "claude-owner-a@example.com.json",
		Label:    "auth-a",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"type":    "anthropic",
			"api_key": "secret-api-key-a",
			"email":   "owner-a@example.com",
		},
		Attributes: map[string]string{
			"path": "/etc/secrets/claude-owner-a@example.com.json",
		},
	}
	authA.EnsureIndex()

	authB := &coreauth.Auth{
		ID:       "claude-owner-b@example.com.json",
		Provider: "anthropic",
		FileName: "claude-owner-b@example.com.json",
		Label:    "auth-b",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"type":    "anthropic",
			"api_key": "secret-api-key-b",
			"email":   "owner-b@example.com",
		},
		Attributes: map[string]string{
			"path": "/etc/secrets/claude-owner-b@example.com.json",
		},
	}
	authB.EnsureIndex()

	if _, errReg := manager.Register(context.Background(), authA); errReg != nil {
		t.Fatalf("register authA: %v", errReg)
	}
	if _, errReg := manager.Register(context.Background(), authB); errReg != nil {
		t.Fatalf("register authB: %v", errReg)
	}

	t.Run("absent binding returns unbound", func(t *testing.T) {
		reqPayload, _ := json.Marshal(pluginapi.HostAffinityLookupRequest{
			Provider:  "anthropic",
			Model:     "claude-3-7-sonnet",
			SessionID: "non-existent-session",
		})
		rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAffinityLookup, reqPayload)
		if errCall != nil {
			t.Fatalf("callFromPlugin error = %v", errCall)
		}
		resp, errDecode := decodeRPCEnvelope[pluginapi.HostAffinityLookupResponse](rawResp)
		if errDecode != nil {
			t.Fatalf("decode response: %v", errDecode)
		}
		if resp.Status != pluginapi.HostAffinityStatusUnbound {
			t.Fatalf("status = %q, want %q", resp.Status, pluginapi.HostAffinityStatusUnbound)
		}
		if resp.AuthIndex != "" {
			t.Fatalf("unexpected auth in unbound response: %#v", resp)
		}
	})

	t.Run("bound session lookup and payload hygiene without email leak", func(t *testing.T) {
		opts := cliproxyexecutor.Options{
			Headers:  map[string][]string{"X-Claude-Code-Session-Id": {"bound-session-1"}},
			Metadata: make(map[string]any),
		}
		picked, errPick := selector.Pick(context.Background(), "anthropic", "claude-3-7-sonnet", opts, []*coreauth.Auth{authA})
		if errPick != nil || picked.ID != authA.ID {
			t.Fatalf("pick failed: auth=%v err=%v", picked, errPick)
		}

		reqPayload, _ := json.Marshal(pluginapi.HostAffinityLookupRequest{
			Provider:  "anthropic",
			Model:     "claude-3-7-sonnet",
			SessionID: "bound-session-1",
		})
		rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAffinityLookup, reqPayload)
		if errCall != nil {
			t.Fatalf("callFromPlugin error = %v", errCall)
		}

		// Verify no secret, email, credential ID, or physical file path leaked in raw json
		rawStr := string(rawResp)
		for _, sensitive := range []string{"secret-api-key-a", "owner-a@example.com", "claude-owner-a@example.com.json", "/etc/secrets"} {
			if strings.Contains(rawStr, sensitive) {
				t.Fatalf("raw response leaked sensitive token %q: %s", sensitive, rawStr)
			}
		}

		resp, errDecode := decodeRPCEnvelope[pluginapi.HostAffinityLookupResponse](rawResp)
		if errDecode != nil {
			t.Fatalf("decode response: %v", errDecode)
		}
		if resp.Status != pluginapi.HostAffinityStatusBound {
			t.Fatalf("status = %q, want %q", resp.Status, pluginapi.HostAffinityStatusBound)
		}
		if resp.AuthIndex != authA.Index {
			t.Fatalf("auth_index = %q, want %q", resp.AuthIndex, authA.Index)
		}
		if resp.Disabled || resp.Unavailable {
			t.Fatalf("unexpected disabled/unavailable flags: disabled=%v unavailable=%v", resp.Disabled, resp.Unavailable)
		}
		if resp.ObservedAt.IsZero() {
			t.Fatalf("observed_at is zero")
		}
	})

	t.Run("thinking suffix normalization", func(t *testing.T) {
		reqPayload, _ := json.Marshal(pluginapi.HostAffinityLookupRequest{
			Provider:  "anthropic",
			Model:     "claude-3-7-sonnet(high)",
			SessionID: "bound-session-1",
		})
		rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAffinityLookup, reqPayload)
		if errCall != nil {
			t.Fatalf("callFromPlugin error = %v", errCall)
		}
		resp, errDecode := decodeRPCEnvelope[pluginapi.HostAffinityLookupResponse](rawResp)
		if errDecode != nil {
			t.Fatalf("decode response: %v", errDecode)
		}
		if resp.Status != pluginapi.HostAffinityStatusBound || resp.AuthIndex != authA.Index {
			t.Fatalf("normalized thinking model lookup failed: %#v", resp)
		}
	})

	t.Run("long session ID normalization (> 256 bytes)", func(t *testing.T) {
		longSessionID := strings.Repeat("s", 252) // prefix "claude:" + 252 chars = 259 chars > 256
		opts := cliproxyexecutor.Options{
			Headers:  map[string][]string{"X-Claude-Code-Session-Id": {longSessionID}},
			Metadata: make(map[string]any),
		}
		picked, errPick := selector.Pick(context.Background(), "anthropic", "claude-3-7-sonnet", opts, []*coreauth.Auth{authA})
		if errPick != nil || picked.ID != authA.ID {
			t.Fatalf("pick failed: auth=%v err=%v", picked, errPick)
		}

		reqPayload, _ := json.Marshal(pluginapi.HostAffinityLookupRequest{
			Provider:  "anthropic",
			Model:     "claude-3-7-sonnet",
			SessionID: longSessionID,
		})
		rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAffinityLookup, reqPayload)
		if errCall != nil {
			t.Fatalf("callFromPlugin error = %v", errCall)
		}
		resp, errDecode := decodeRPCEnvelope[pluginapi.HostAffinityLookupResponse](rawResp)
		if errDecode != nil {
			t.Fatalf("decode response: %v", errDecode)
		}
		if resp.Status != pluginapi.HostAffinityStatusBound || resp.AuthIndex != authA.Index {
			t.Fatalf("long session lookup failed: %#v", resp)
		}
	})

	t.Run("rebinding A to B reflects updated binding", func(t *testing.T) {
		sessionID := "rebind-session"
		opts := cliproxyexecutor.Options{
			Headers:  map[string][]string{"X-Claude-Code-Session-Id": {sessionID}},
			Metadata: make(map[string]any),
		}
		picked, _ := selector.Pick(context.Background(), "anthropic", "claude-3-7-sonnet", opts, []*coreauth.Auth{authA})
		if picked.ID != authA.ID {
			t.Fatalf("initial pick = %s, want %s", picked.ID, authA.ID)
		}

		// Rebind to authB
		picked2, _ := selector.Pick(context.Background(), "anthropic", "claude-3-7-sonnet", opts, []*coreauth.Auth{authB})
		if picked2.ID != authB.ID {
			t.Fatalf("second pick = %s, want %s", picked2.ID, authB.ID)
		}

		reqPayload, _ := json.Marshal(pluginapi.HostAffinityLookupRequest{
			Provider:  "anthropic",
			Model:     "claude-3-7-sonnet",
			SessionID: sessionID,
		})
		rawResp, _ := host.callFromPlugin(context.Background(), pluginabi.MethodHostAffinityLookup, reqPayload)
		resp, _ := decodeRPCEnvelope[pluginapi.HostAffinityLookupResponse](rawResp)
		if resp.Status != pluginapi.HostAffinityStatusBound || resp.AuthIndex != authB.Index {
			t.Fatalf("lookup after rebinding: got index=%q status=%q, want index=%q", resp.AuthIndex, resp.Status, authB.Index)
		}
	})

	t.Run("unknown subagent does not guess parent binding", func(t *testing.T) {
		// Parent is bound to authA
		parentSession := "parent-main-thread-100"
		opts := cliproxyexecutor.Options{
			Headers:  map[string][]string{"Session-Id": {parentSession}},
			Metadata: make(map[string]any),
		}
		selector.Pick(context.Background(), "anthropic", "claude-3-7-sonnet", opts, []*coreauth.Auth{authA})

		// Query unknown child subagent
		reqPayload, _ := json.Marshal(pluginapi.HostAffinityLookupRequest{
			Provider:  "anthropic",
			Model:     "claude-3-7-sonnet",
			SessionID: "codex:" + parentSession + ":subagent-child-unknown",
		})
		rawResp, _ := host.callFromPlugin(context.Background(), pluginabi.MethodHostAffinityLookup, reqPayload)
		resp, _ := decodeRPCEnvelope[pluginapi.HostAffinityLookupResponse](rawResp)
		if resp.Status != pluginapi.HostAffinityStatusUnbound {
			t.Fatalf("unknown subagent lookup status = %q, want %q", resp.Status, pluginapi.HostAffinityStatusUnbound)
		}
	})

	t.Run("ambiguous unqualified session resolves to ambiguous across namespaces", func(t *testing.T) {
		// Bind "ambig-sess" under claude to authA, and under codex to authB
		optsClaude := cliproxyexecutor.Options{
			Headers:  map[string][]string{"X-Claude-Code-Session-Id": {"ambig-sess"}},
			Metadata: make(map[string]any),
		}
		selector.Pick(context.Background(), "anthropic", "claude-3-7-sonnet", optsClaude, []*coreauth.Auth{authA})

		optsCodex := cliproxyexecutor.Options{
			Headers:  map[string][]string{"Session-Id": {"ambig-sess"}},
			Metadata: make(map[string]any),
		}
		selector.Pick(context.Background(), "anthropic", "claude-3-7-sonnet", optsCodex, []*coreauth.Auth{authB})

		// Query bare "ambig-sess" without namespace
		reqPayload, _ := json.Marshal(pluginapi.HostAffinityLookupRequest{
			Provider:  "anthropic",
			Model:     "claude-3-7-sonnet",
			SessionID: "ambig-sess",
		})
		rawResp, _ := host.callFromPlugin(context.Background(), pluginabi.MethodHostAffinityLookup, reqPayload)
		resp, _ := decodeRPCEnvelope[pluginapi.HostAffinityLookupResponse](rawResp)
		if resp.Status != pluginapi.HostAffinityStatusAmbiguous {
			t.Fatalf("ambiguous session status = %q, want %q", resp.Status, pluginapi.HostAffinityStatusAmbiguous)
		}

		// Qualified queries resolve unambiguously
		reqClaude, _ := json.Marshal(pluginapi.HostAffinityLookupRequest{
			Provider:  "anthropic",
			Model:     "claude-3-7-sonnet",
			SessionID: "claude:ambig-sess",
		})
		rawClaude, _ := host.callFromPlugin(context.Background(), pluginabi.MethodHostAffinityLookup, reqClaude)
		respClaude, _ := decodeRPCEnvelope[pluginapi.HostAffinityLookupResponse](rawClaude)
		if respClaude.Status != pluginapi.HostAffinityStatusBound || respClaude.AuthIndex != authA.Index {
			t.Fatalf("qualified claude session status = %q index = %q, want bound %q", respClaude.Status, respClaude.AuthIndex, authA.Index)
		}
	})

	t.Run("extended namespaces resolve bare ID (task, clientreq, geminicache, execution)", func(t *testing.T) {
		testCases := []struct {
			name   string
			opts   cliproxyexecutor.Options
			bareID string
			qualID string
			auth   *coreauth.Auth
		}{
			{
				name: "task header",
				opts: cliproxyexecutor.Options{
					Headers:  map[string][]string{"X-Task-ID": {"task-xyz-1"}},
					Metadata: make(map[string]any),
				},
				bareID: "task-xyz-1",
				qualID: "task:task-xyz-1",
				auth:   authA,
			},
			{
				name: "client request id header",
				opts: cliproxyexecutor.Options{
					Headers:  map[string][]string{"X-Client-Request-Id": {"crid-abc-2"}},
					Metadata: make(map[string]any),
				},
				bareID: "crid-abc-2",
				qualID: "clientreq:crid-abc-2",
				auth:   authB,
			},
			{
				name: "gemini cache payload",
				opts: cliproxyexecutor.Options{
					OriginalRequest: []byte(`{"cachedContent":"cached-gemini-3"}`),
					Metadata:        make(map[string]any),
				},
				bareID: "cached-gemini-3",
				qualID: "geminicache:cached-gemini-3",
				auth:   authA,
			},
			{
				name: "execution session metadata",
				opts: cliproxyexecutor.Options{
					Metadata: map[string]any{
						cliproxyexecutor.ExecutionSessionMetadataKey: "exec-run-4",
					},
				},
				bareID: "exec-run-4",
				qualID: "execution:exec-run-4",
				auth:   authB,
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				picked, errPick := selector.Pick(context.Background(), "anthropic", "claude-3-7-sonnet", tc.opts, []*coreauth.Auth{authA, authB})
				if errPick != nil || picked.ID != tc.auth.ID {
					t.Fatalf("pick failed: auth=%v err=%v", picked, errPick)
				}

				// Query via bare ID
				reqBare, _ := json.Marshal(pluginapi.HostAffinityLookupRequest{
					Provider:  "anthropic",
					Model:     "claude-3-7-sonnet",
					SessionID: tc.bareID,
				})
				rawBare, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAffinityLookup, reqBare)
				if errCall != nil {
					t.Fatalf("callFromPlugin error = %v", errCall)
				}
				respBare, _ := decodeRPCEnvelope[pluginapi.HostAffinityLookupResponse](rawBare)
				if respBare.Status != pluginapi.HostAffinityStatusBound || respBare.AuthIndex != tc.auth.Index {
					t.Fatalf("bare lookup %q: got status=%q index=%q, want bound index=%q", tc.bareID, respBare.Status, respBare.AuthIndex, tc.auth.Index)
				}

				// Query via qualified ID
				reqQual, _ := json.Marshal(pluginapi.HostAffinityLookupRequest{
					Provider:  "anthropic",
					Model:     "claude-3-7-sonnet",
					SessionID: tc.qualID,
				})
				rawQual, _ := host.callFromPlugin(context.Background(), pluginabi.MethodHostAffinityLookup, reqQual)
				respQual, _ := decodeRPCEnvelope[pluginapi.HostAffinityLookupResponse](rawQual)
				if respQual.Status != pluginapi.HostAffinityStatusBound || respQual.AuthIndex != tc.auth.Index {
					t.Fatalf("qual lookup %q: got status=%q index=%q, want bound index=%q", tc.qualID, respQual.Status, respQual.AuthIndex, tc.auth.Index)
				}
			})
		}

		// Conflict test: bind task:shared-clash to authA and claude:shared-clash to authB
		optsClash1 := cliproxyexecutor.Options{
			Headers:  map[string][]string{"X-Task-ID": {"shared-clash"}},
			Metadata: make(map[string]any),
		}
		selector.Pick(context.Background(), "anthropic", "claude-3-7-sonnet", optsClash1, []*coreauth.Auth{authA})
		optsClash2 := cliproxyexecutor.Options{
			Headers:  map[string][]string{"X-Claude-Code-Session-Id": {"shared-clash"}},
			Metadata: make(map[string]any),
		}
		selector.Pick(context.Background(), "anthropic", "claude-3-7-sonnet", optsClash2, []*coreauth.Auth{authB})

		reqClash, _ := json.Marshal(pluginapi.HostAffinityLookupRequest{
			Provider:  "anthropic",
			Model:     "claude-3-7-sonnet",
			SessionID: "shared-clash",
		})
		rawClash, _ := host.callFromPlugin(context.Background(), pluginabi.MethodHostAffinityLookup, reqClash)
		respClash, _ := decodeRPCEnvelope[pluginapi.HostAffinityLookupResponse](rawClash)
		if respClash.Status != pluginapi.HostAffinityStatusAmbiguous {
			t.Fatalf("clash lookup status = %q, want %q", respClash.Status, pluginapi.HostAffinityStatusAmbiguous)
		}
	})

	t.Run("disabled credential returns bound with disabled flag", func(t *testing.T) {
		sessionID := "disabled-cred-session"
		opts := cliproxyexecutor.Options{
			Headers:  map[string][]string{"X-Claude-Code-Session-Id": {sessionID}},
			Metadata: make(map[string]any),
		}
		selector.Pick(context.Background(), "anthropic", "claude-3-7-sonnet", opts, []*coreauth.Auth{authA})

		// Disable authA in manager
		authA.Disabled = true
		manager.Update(context.Background(), authA)
		defer func() {
			authA.Disabled = false
			manager.Update(context.Background(), authA)
		}()

		reqPayload, _ := json.Marshal(pluginapi.HostAffinityLookupRequest{
			Provider:  "anthropic",
			Model:     "claude-3-7-sonnet",
			SessionID: sessionID,
		})
		rawResp, _ := host.callFromPlugin(context.Background(), pluginabi.MethodHostAffinityLookup, reqPayload)
		resp, _ := decodeRPCEnvelope[pluginapi.HostAffinityLookupResponse](rawResp)
		if resp.Status != pluginapi.HostAffinityStatusBound {
			t.Fatalf("status = %q, want bound", resp.Status)
		}
		if !resp.Disabled {
			t.Fatalf("expected disabled = true, got %v", resp.Disabled)
		}
	})

	t.Run("unsupported when plugin scheduler active", func(t *testing.T) {
		managerWithPluginSched := coreauth.NewManager(nil, nil, nil)
		managerWithPluginSched.SetSelector(selector)
		managerWithPluginSched.SetPluginScheduler(&mockPluginScheduler{})
		hostWithPluginSched := New()
		hostWithPluginSched.SetAuthManager(managerWithPluginSched)

		reqPayload, _ := json.Marshal(pluginapi.HostAffinityLookupRequest{
			Provider:  "anthropic",
			Model:     "claude-3-7-sonnet",
			SessionID: "bound-session-1",
		})
		rawResp, _ := hostWithPluginSched.callFromPlugin(context.Background(), pluginabi.MethodHostAffinityLookup, reqPayload)
		resp, _ := decodeRPCEnvelope[pluginapi.HostAffinityLookupResponse](rawResp)
		if resp.Status != pluginapi.HostAffinityStatusUnsupported {
			t.Fatalf("status with plugin scheduler = %q, want %q", resp.Status, pluginapi.HostAffinityStatusUnsupported)
		}
	})

	t.Run("unsupported when selector is not session affinity", func(t *testing.T) {
		managerNoAffinity := coreauth.NewManager(nil, nil, nil)
		managerNoAffinity.SetSelector(&coreauth.RoundRobinSelector{})
		hostNoAffinity := New()
		hostNoAffinity.SetAuthManager(managerNoAffinity)

		reqPayload, _ := json.Marshal(pluginapi.HostAffinityLookupRequest{
			Provider:  "anthropic",
			Model:     "claude-3-7-sonnet",
			SessionID: "bound-session-1",
		})
		rawResp, _ := hostNoAffinity.callFromPlugin(context.Background(), pluginabi.MethodHostAffinityLookup, reqPayload)
		resp, _ := decodeRPCEnvelope[pluginapi.HostAffinityLookupResponse](rawResp)
		if resp.Status != pluginapi.HostAffinityStatusUnsupported {
			t.Fatalf("status with round robin = %q, want %q", resp.Status, pluginapi.HostAffinityStatusUnsupported)
		}
	})

	t.Run("invalid input validation", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			req  pluginapi.HostAffinityLookupRequest
		}{
			{"empty provider", pluginapi.HostAffinityLookupRequest{Provider: "", Model: "m", SessionID: "s"}},
			{"empty model", pluginapi.HostAffinityLookupRequest{Provider: "p", Model: "", SessionID: "s"}},
			{"empty session", pluginapi.HostAffinityLookupRequest{Provider: "p", Model: "m", SessionID: ""}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				reqPayload, _ := json.Marshal(tc.req)
				_, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAffinityLookup, reqPayload)
				if errCall == nil {
					t.Fatalf("expected error for %s, got nil", tc.name)
				}
			})
		}
	})

	t.Run("LCP session binding observation", func(t *testing.T) {
		lcpOpts := cliproxyexecutor.Options{
			OriginalRequest: []byte(`{"messages":[{"role":"system","content":"host-lcp-sys"},{"role":"user","content":"host-lcp-user"}]}`),
			Metadata: map[string]any{
				cliproxyexecutor.CallerScopeMetadataKey: "plugin-caller",
			},
		}
		picked, errPick := selector.Pick(context.Background(), "anthropic", "claude-3-7-sonnet", lcpOpts, []*coreauth.Auth{authA})
		if errPick != nil || picked.ID != authA.ID {
			t.Fatalf("LCP Pick failed: %v", errPick)
		}
		lcpSessionID, _ := lcpOpts.Metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey].(string)
		if lcpSessionID == "" {
			t.Fatalf("missing LCP session ID in metadata")
		}

		reqPayload, _ := json.Marshal(pluginapi.HostAffinityLookupRequest{
			Provider:  "anthropic",
			Model:     "claude-3-7-sonnet",
			SessionID: lcpSessionID,
		})
		rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAffinityLookup, reqPayload)
		if errCall != nil {
			t.Fatalf("callFromPlugin error = %v", errCall)
		}
		resp, errDecode := decodeRPCEnvelope[pluginapi.HostAffinityLookupResponse](rawResp)
		if errDecode != nil {
			t.Fatalf("decode response: %v", errDecode)
		}
		if resp.Status != pluginapi.HostAffinityStatusBound || resp.AuthIndex != authA.Index {
			t.Fatalf("LCP observation failed: got status=%q index=%q, want bound %q", resp.Status, resp.AuthIndex, authA.Index)
		}
	})

	t.Run("concurrent reads", func(t *testing.T) {
		reqPayload, _ := json.Marshal(pluginapi.HostAffinityLookupRequest{
			Provider:  "anthropic",
			Model:     "claude-3-7-sonnet",
			SessionID: "bound-session-1",
		})
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAffinityLookup, reqPayload)
				if errCall != nil {
					t.Errorf("concurrent callFromPlugin error: %v", errCall)
					return
				}
				resp, errDecode := decodeRPCEnvelope[pluginapi.HostAffinityLookupResponse](rawResp)
				if errDecode != nil || resp.Status != pluginapi.HostAffinityStatusBound {
					t.Errorf("concurrent response error: %#v %v", resp, errDecode)
				}
			}()
		}
		wg.Wait()
	})
}
