package executor

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestAntigravityExecutorDirectModelCreditsAdmission_Issue8182(t *testing.T) {
	const model = "claude-opus-5-5-high"
	const quotaError = `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Quota exhausted","details":[{"reason":"QUOTA_EXHAUSTED"}]}}`
	const capacityError = `{"error":{"code":503,"status":"UNAVAILABLE","details":[{"reason":"MODEL_CAPACITY_EXHAUSTED"}]}}`
	const success = `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"recovered"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`

	future := time.Now().Add(time.Hour)

	cases := []struct {
		name       string
		state      *cliproxyauth.ModelState
		wantAllow  bool
		wantStatus int
	}{
		{
			name: "model_status_disabled",
			state: &cliproxyauth.ModelState{
				Status:         cliproxyauth.StatusDisabled,
				Unavailable:    true,
				NextRetryAfter: future,
				Quota:          cliproxyauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: future},
				LastError:      &cliproxyauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: quotaError},
			},
			wantAllow:  false,
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name: "permanent_unavailability",
			state: &cliproxyauth.ModelState{
				Status:      cliproxyauth.StatusError,
				Unavailable: true,
			},
			wantAllow:  false,
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name: "forced_model_cooldown",
			state: &cliproxyauth.ModelState{
				Status:         cliproxyauth.StatusError,
				Unavailable:    true,
				NextRetryAfter: future,
				LastError: &cliproxyauth.Error{
					Code:       cliproxyauth.ErrorCodeForceCooldown,
					HTTPStatus: http.StatusTooManyRequests,
					Message:    quotaError,
				},
			},
			wantAllow:  false,
			wantStatus: http.StatusTooManyRequests,
		},
		{
			name: "active_non_quota_cooldown_403",
			state: &cliproxyauth.ModelState{
				Status:         cliproxyauth.StatusError,
				Unavailable:    true,
				NextRetryAfter: future,
				LastError:      &cliproxyauth.Error{HTTPStatus: http.StatusForbidden, Message: "forbidden"},
			},
			wantAllow:  false,
			wantStatus: http.StatusTooManyRequests,
		},
		{
			name: "eligible_quota_429_cooldown",
			state: &cliproxyauth.ModelState{
				Status:         cliproxyauth.StatusError,
				Unavailable:    true,
				NextRetryAfter: future,
				Quota:          cliproxyauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: future},
				LastError:      &cliproxyauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: quotaError},
			},
			wantAllow: true,
		},
		{
			name: "eligible_capacity_503_cooldown",
			state: &cliproxyauth.ModelState{
				Status:         cliproxyauth.StatusError,
				Unavailable:    true,
				NextRetryAfter: time.Now().Add(5 * time.Second),
				LastError:      &cliproxyauth.Error{HTTPStatus: http.StatusServiceUnavailable, Message: capacityError},
			},
			wantAllow: true,
		},
	}

	for _, stream := range []bool{false, true} {
		mode := "execute"
		if stream {
			mode = "stream"
		}
		for _, tc := range cases {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				var upstreamCalls atomic.Int32
				var sawCredit atomic.Value
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/v1internal:loadCodeAssist" {
						_, _ = io.WriteString(w, `{"paidTier":{"id":"tier-1","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"100","minimumCreditAmountForUsage":"50"}]}}`)
						return
					}
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Errorf("read request body: %v", errRead)
					}
					upstreamCalls.Add(1)
					sawCredit.Store(ojson.GetBytes(body, "enabledCreditTypes.0").Str)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: "+success+"\n\n")
				}))
				t.Cleanup(server.Close)

				auth := newIssue7865TestAuth(t.Name())
				auth.Attributes = map[string]string{"base_url": server.URL}
				auth.ModelStates = map[string]*cliproxyauth.ModelState{
					model: tc.state,
				}

				reg := registry.GetGlobalRegistry()
				reg.RegisterClient(auth.ID, "antigravity", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() {
					reg.UnregisterClient(auth.ID)
					cliproxyauth.SetAntigravityCreditsHintForAuth(auth, cliproxyauth.AntigravityCreditsHint{})
				})
				cliproxyauth.SetAntigravityCreditsHintForAuth(auth, cliproxyauth.AntigravityCreditsHint{
					Known:     true,
					Available: true,
					UpdatedAt: time.Now(),
				})

				cfg := &config.Config{QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true}}
				e := NewAntigravityExecutor(cfg)
				req := cliproxyexecutor.Request{
					Model:   model,
					Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
				}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}

				var payload []byte
				var errExec error
				if stream {
					var streamRes *cliproxyexecutor.StreamResult
					streamRes, errExec = e.ExecuteStream(cliproxyauth.WithAntigravityCredits(t.Context()), auth, req, opts)
					if errExec == nil && streamRes != nil {
						for chunk := range streamRes.Chunks {
							if chunk.Err != nil {
								errExec = chunk.Err
							}
							payload = append(payload, chunk.Payload...)
						}
					}
				} else {
					var resp cliproxyexecutor.Response
					resp, errExec = e.Execute(cliproxyauth.WithAntigravityCredits(t.Context()), auth, req, opts)
					payload = resp.Payload
				}
				if tc.wantAllow {
					if errExec != nil {
						t.Fatalf("%s error = %v, want success", mode, errExec)
					}
					if !strings.Contains(string(payload), "recovered") {
						t.Fatalf("%s payload = %q, want recovered", mode, string(payload))
					}
					if upstreamCalls.Load() != 1 {
						t.Fatalf("upstream calls = %d, want 1", upstreamCalls.Load())
					}
					if gotCredit, _ := sawCredit.Load().(string); gotCredit != "GOOGLE_ONE_AI" {
						t.Fatalf("enabledCreditTypes.0 = %q, want GOOGLE_ONE_AI", gotCredit)
					}
					return
				}

				if errExec == nil {
					t.Fatalf("%s succeeded unexpectedly; upstreamCalls=%d", mode, upstreamCalls.Load())
				}
				if upstreamCalls.Load() != 0 {
					t.Fatalf("upstream calls = %d, want 0", upstreamCalls.Load())
				}
				var status interface{ StatusCode() int }
				if !errors.As(errExec, &status) || status.StatusCode() != tc.wantStatus {
					t.Fatalf("%s error = %v, want status %d", mode, errExec, tc.wantStatus)
				}
			})
		}
	}
}

func TestAntigravityExecutorDirectModelCredits_Capacity503DoesNotMaskPrior404Cooldown_Issue8182(t *testing.T) {
	const model = "claude-opus-5-5-high"
	const capacityError = `{"error":{"code":503,"status":"UNAVAILABLE","details":[{"reason":"MODEL_CAPACITY_EXHAUSTED"}]}}`
	const success = `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"recovered"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`

	for _, stream := range []bool{false, true} {
		mode := "execute"
		if stream {
			mode = "stream"
		}
		t.Run(mode, func(t *testing.T) {
			var upstreamCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1internal:loadCodeAssist" {
					_, _ = io.WriteString(w, `{"paidTier":{"id":"tier-1","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"100","minimumCreditAmountForUsage":"50"}]}}`)
					return
				}
				upstreamCalls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: "+success+"\n\n")
			}))
			t.Cleanup(server.Close)

			cdsDir := t.TempDir()
			cfg := &config.Config{QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true}}
			e := NewAntigravityExecutor(cfg)
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.SetCooldownStateStore(cliproxyauth.NewFileCooldownStateStore(cdsDir))
			manager.RegisterExecutor(e)

			auth := newIssue7865TestAuth(t.Name())
			auth.Attributes = map[string]string{"base_url": server.URL}
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(auth.ID, "antigravity", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() {
				reg.UnregisterClient(auth.ID)
				cliproxyauth.SetAntigravityCreditsHintForAuth(auth, cliproxyauth.AntigravityCreditsHint{})
			})

			registered, errReg := manager.Register(t.Context(), auth)
			if errReg != nil {
				t.Fatalf("Register() error = %v", errReg)
			}
			cliproxyauth.SetAntigravityCreditsHintForAuth(registered, cliproxyauth.AntigravityCreditsHint{
				Known:     true,
				Available: true,
				UpdatedAt: time.Now(),
			})

			// Record 404 (12h non-quota cooldown) followed by 503 MODEL_CAPACITY_EXHAUSTED.
			manager.MarkResult(t.Context(), cliproxyauth.Result{
				AuthID:            registered.ID,
				Provider:          "antigravity",
				Model:             model,
				RouteModel:        model,
				Success:           false,
				Error:             &cliproxyauth.Error{HTTPStatus: http.StatusNotFound, Message: "model not found"},
				CredentialVersion: registered.CredentialVersion,
				RegistrationEpoch: registered.RegistrationEpoch,
			})
			manager.MarkResult(t.Context(), cliproxyauth.Result{
				AuthID:            registered.ID,
				Provider:          "antigravity",
				Model:             model,
				RouteModel:        model,
				Success:           false,
				Error:             &cliproxyauth.Error{HTTPStatus: http.StatusServiceUnavailable, Message: capacityError},
				CredentialVersion: registered.CredentialVersion,
				RegistrationEpoch: registered.RegistrationEpoch,
			})

			current, okCurrent := manager.GetByID(registered.ID)
			if !okCurrent || current == nil {
				t.Fatal("failed to load registered auth")
			}

			req := cliproxyexecutor.Request{
				Model:   model,
				Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
			}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}

			var errExec error
			if stream {
				var streamRes *cliproxyexecutor.StreamResult
				streamRes, errExec = e.ExecuteStream(cliproxyauth.WithAntigravityCredits(t.Context()), current, req, opts)
				if errExec == nil && streamRes != nil {
					for chunk := range streamRes.Chunks {
						if chunk.Err != nil {
							errExec = chunk.Err
						}
					}
				}
			} else {
				_, errExec = e.Execute(cliproxyauth.WithAntigravityCredits(t.Context()), current, req, opts)
			}

			if errExec == nil {
				t.Fatalf("%s unexpectedly allowed paid credits request after 404+503; upstreamCalls=%d", mode, upstreamCalls.Load())
			}
			if upstreamCalls.Load() != 0 {
				t.Fatalf("upstream paid calls = %d, want 0", upstreamCalls.Load())
			}
			var status interface{ StatusCode() int }
			if !errors.As(errExec, &status) || status.StatusCode() != http.StatusTooManyRequests {
				t.Fatalf("%s error = %v, want 429 cooldown rejection", mode, errExec)
			}

			// Verify the same rejection holds after persisting and restoring into a fresh Manager.
			restoredManager := cliproxyauth.NewManager(nil, nil, nil)
			restoredManager.SetConfig(cfg)
			restoredManager.SetCooldownStateStore(cliproxyauth.NewFileCooldownStateStore(cdsDir))
			restoredManager.RegisterExecutor(e)
			freshAuth := newIssue7865TestAuth(t.Name())
			freshAuth.Attributes = map[string]string{"base_url": server.URL}
			if _, errReg2 := restoredManager.Register(t.Context(), freshAuth); errReg2 != nil {
				t.Fatalf("restoredManager.Register() error = %v", errReg2)
			}
			if errRestore := restoredManager.RestoreCooldownStates(t.Context()); errRestore != nil {
				t.Fatalf("RestoreCooldownStates() error = %v", errRestore)
			}
			restoredAuth, okRestored := restoredManager.GetByID(freshAuth.ID)
			if !okRestored || restoredAuth == nil {
				t.Fatal("failed to load restored auth")
			}
			var errRestoredExec error
			if stream {
				var streamRes *cliproxyexecutor.StreamResult
				streamRes, errRestoredExec = e.ExecuteStream(cliproxyauth.WithAntigravityCredits(t.Context()), restoredAuth, req, opts)
				if errRestoredExec == nil && streamRes != nil {
					for chunk := range streamRes.Chunks {
						if chunk.Err != nil {
							errRestoredExec = chunk.Err
						}
					}
				}
			} else {
				_, errRestoredExec = e.Execute(cliproxyauth.WithAntigravityCredits(t.Context()), restoredAuth, req, opts)
			}
			if errRestoredExec == nil {
				t.Fatalf("%s unexpectedly allowed paid credits request after restart restore of 404+503; upstreamCalls=%d", mode, upstreamCalls.Load())
			}
			if upstreamCalls.Load() != 0 {
				t.Fatalf("upstream paid calls after restore = %d, want 0", upstreamCalls.Load())
			}
			var restoredStatus interface{ StatusCode() int }
			if !errors.As(errRestoredExec, &restoredStatus) || restoredStatus.StatusCode() != http.StatusTooManyRequests {
				t.Fatalf("%s restored error = %v, want 429 cooldown rejection", mode, errRestoredExec)
			}
		})
	}
}

func TestAntigravityManagerCreditsFallback_SwitchedVariantRecoversDespiteSourceModelCooldown_Issue8182(t *testing.T) {
	const group = "claude-credits-v8182"
	const high, low = group + "-high", group + "-low"
	const quotaError = `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Quota exhausted","details":[{"reason":"QUOTA_EXHAUSTED"}]}}`
	const success = `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"recovered"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`

	for _, stream := range []bool{false, true} {
		mode := "execute"
		if stream {
			mode = "stream"
		}
		t.Run(mode, func(t *testing.T) {
			cfg := &config.Config{QuotaExceeded: config.QuotaExceeded{AntigravityCredits: true}}
			e := NewAntigravityExecutor(cfg)
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 1)
			manager.RegisterExecutor(e)

			var registered *cliproxyauth.Auth
			attempts := make(chan string, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1internal:loadCodeAssist" {
					_, _ = io.WriteString(w, `{"paidTier":{"id":"tier-1","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"100","minimumCreditAmountForUsage":"50"}]}}`)
					return
				}
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil || ojson.GetBytes(body, "model").Str != low {
					t.Errorf("unexpected Claude request: path=%s body=%s err=%v", r.URL.Path, body, errRead)
				}
				credit := ojson.GetBytes(body, "enabledCreditTypes.0").Str
				attempts <- credit
				if credit == "" {
					// Simulate a concurrent request placing the source model ("high") into a
					// non-quota 403 cooldown while this request fails with quota 429 on "low".
					manager.MarkResult(t.Context(), cliproxyauth.Result{
						AuthID:            registered.ID,
						Provider:          "antigravity",
						Model:             high,
						RouteModel:        high,
						Success:           false,
						Error:             &cliproxyauth.Error{HTTPStatus: http.StatusForbidden, Message: "forbidden"},
						CredentialVersion: registered.CredentialVersion,
						RegistrationEpoch: registered.RegistrationEpoch,
					})
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = io.WriteString(w, quotaError)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: "+success+"\n\n")
			}))
			t.Cleanup(server.Close)

			auth := newIssue7865TestAuth(t.Name())
			auth.Attributes = map[string]string{"base_url": server.URL}
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(auth.ID, "antigravity", []*registry.ModelInfo{{ID: high}, {ID: low}})
			t.Cleanup(func() {
				reg.UnregisterClient(auth.ID)
				cliproxyauth.SetAntigravityCreditsHintForAuth(auth, cliproxyauth.AntigravityCreditsHint{})
			})

			var errReg error
			registered, errReg = manager.Register(t.Context(), auth)
			if errReg != nil {
				t.Fatalf("Register() error = %v", errReg)
			}
			if !manager.StoreAntigravityModelCatalog(t.Context(), registered, map[string]string{high: "HIGH", low: "LOW"}, map[string]map[string]int{group: {"low": 50000, "high": 60000}}, nil) {
				t.Fatal("failed to publish catalog")
			}
			cliproxyauth.SetAntigravityCreditsHintForAuth(registered, cliproxyauth.AntigravityCreditsHint{
				Known:     true,
				Available: true,
				UpdatedAt: time.Now(),
			})

			req := cliproxyexecutor.Request{
				Model:   high,
				Payload: []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"low"},"messages":[{"role":"user","content":"hi"}]}`),
			}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}

			var payload []byte
			var errExec error
			if stream {
				var streamRes *cliproxyexecutor.StreamResult
				streamRes, errExec = manager.ExecuteStream(t.Context(), []string{"antigravity"}, req, opts)
				if errExec == nil && streamRes != nil {
					for chunk := range streamRes.Chunks {
						if chunk.Err != nil {
							errExec = chunk.Err
						}
						payload = append(payload, chunk.Payload...)
					}
				}
			} else {
				var resp cliproxyexecutor.Response
				resp, errExec = manager.Execute(t.Context(), []string{"antigravity"}, req, opts)
				payload = resp.Payload
			}

			if errExec != nil {
				t.Fatalf("%s error = %v, want credits recovery on switched variant %s", mode, errExec, low)
			}
			if !strings.Contains(string(payload), "recovered") {
				t.Fatalf("%s payload = %q, want recovered", mode, string(payload))
			}
			if len(attempts) != 2 {
				t.Fatalf("attempts = %d, want 2 (initial + credits retry)", len(attempts))
			}
			if first, second := <-attempts, <-attempts; first != "" || second != "GOOGLE_ONE_AI" {
				t.Fatalf("credits on initial/retry = %q/%q, want empty/GOOGLE_ONE_AI", first, second)
			}
		})
	}
}
