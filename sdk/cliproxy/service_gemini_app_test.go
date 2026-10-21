package cliproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	fileauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestGeminiAppServiceRegistration(t *testing.T) {
	svc := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
	auth := &coreauth.Auth{ID: "gemini-app-service-test", Provider: "gemini-app", Status: coreauth.StatusActive}
	registeredAuth, errRegister := svc.coreManager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	auth = registeredAuth
	models := GlobalModelRegistry()
	t.Cleanup(func() { models.UnregisterClient(auth.ID) })
	svc.ensureExecutorsForAuth(auth)
	bound, ok := svc.coreManager.Executor("gemini-app")
	if !ok {
		t.Fatal("Gemini App executor was not registered")
	}
	if _, ok := bound.(*executor.GeminiAppExecutor); !ok {
		t.Fatalf("unexpected executor: %T", bound)
	}
	svc.registerModelsForAuth(context.Background(), auth)
	if !models.ClientSupportsModel(auth.ID, "gemini-app") {
		t.Fatal("Gemini App model was not published")
	}
	auth = auth.Clone()
	auth.Attributes = map[string]string{"excluded_models": "gemini-app"}
	updatedAuth, errUpdate := svc.coreManager.Update(context.Background(), auth)
	if errUpdate != nil {
		t.Fatal(errUpdate)
	}
	auth = updatedAuth
	svc.registerModelsForAuth(context.Background(), auth)
	if models.ClientSupportsModel(auth.ID, "gemini-app") {
		t.Fatal("excluded Gemini App model is still published")
	}
}

func TestGeminiAppStaticModelLookup(t *testing.T) {
	model := registry.LookupStaticModelInfoByChannel("gemini-app", "gemini-app")
	if model == nil || model.Type != "gemini-app" {
		t.Fatal("Gemini App static lookup failed")
	}
	model.DisplayName = "changed"
	if registry.GetGeminiAppModels()[0].DisplayName != "Gemini App" {
		t.Fatal("caller mutated static model state")
	}
	if registry.LookupStaticModelInfo("gemini-app") == nil {
		t.Fatal("global Gemini App model lookup failed")
	}
}

type geminiAppCookieTransport func(*http.Request) (*http.Response, error)

func (f geminiAppCookieTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestGeminiAppServicePersistsCookieRotations(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, scenario := range []string{"", "session", "generation", "replacement", "session-replacement"} {
			t.Run(fmt.Sprintf("stream=%t/scenario=%s", stream, scenario), func(t *testing.T) {
				dir := t.TempDir()
				store := fileauth.NewFileTokenStore()
				store.SetBaseDir(dir)
				manager := coreauth.NewManager(store, nil, nil)
				auth, err := manager.Register(context.Background(), &coreauth.Auth{
					ID: "gemini-app-rotation.json", FileName: "gemini-app-rotation.json", Provider: "gemini-app", Status: coreauth.StatusActive,
					Metadata: map[string]any{"type": "gemini-app", "notes": "keep", "cookies": map[string]any{"__Secure-1PSID": "initial"}},
				})
				if err != nil {
					t.Fatal(err)
				}
				// Recreate both the manager and executor after the first request. The
				// second session must use disk state, not a previous client's jar.
				for round := 1; round <= 2; round++ {
					wantIncoming := "initial"
					if round == 2 {
						wantIncoming = "generation-1"
						if strings.Contains(scenario, "replacement") {
							wantIncoming = "replacement-1"
						}
						if scenario == "session" {
							wantIncoming = "session-1"
						}
						manager = coreauth.NewManager(store, nil, nil)
						if err := manager.Load(context.Background()); err != nil {
							t.Fatal(err)
						}
					}
					svc := &Service{cfg: &config.Config{AuthDir: dir}, coreManager: manager}
					current, ok := manager.GetByID(auth.ID)
					if !ok {
						t.Fatal("credential missing after reload")
					}
					svc.ensureExecutorsForAuth(current)
					bound, _ := manager.Executor("gemini-app")
					ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(geminiAppCookieTransport(func(req *http.Request) (*http.Response, error) {
						stage := "generation"
						status := http.StatusOK
						body := ""
						if req.URL.Path == "/app" {
							stage = "session"
							if cookie, err := req.Cookie("__Secure-1PSID"); err != nil || cookie.Value != wantIncoming {
								t.Fatal("next execution reused stale session cookies")
							}
							body = `{"SNlM0e":"transient-csrf"}`
						} else {
							if cookie, err := req.Cookie("__Secure-1PSID"); err != nil || cookie.Value != fmt.Sprintf("session-%d", round) {
								t.Fatal("generation lost session-page rotation")
							}
							live, _ := manager.GetByID(auth.ID)
							wantLive := fmt.Sprintf("session-%d", round)
							if scenario == "session-replacement" {
								wantLive = fmt.Sprintf("replacement-%d", round)
							}
							if live.Metadata["cookies"].(map[string]any)["__Secure-1PSID"] != wantLive {
								t.Fatal("session rotation was not published before generation")
							}
							candidate := make([]any, 9)
							candidate[1], candidate[8] = []any{"answer"}, []any{2}
							inner, _ := json.Marshal([]any{nil, nil, nil, nil, []any{candidate}})
							wire, _ := json.Marshal([]any{[]any{"wrb.fr", nil, string(inner)}})
							body = string(wire)
						}
						if (scenario == "replacement" && stage == "generation") || (scenario == "session-replacement" && stage == "session") {
							fresh, _ := manager.GetByID(auth.ID)
							fresh.Metadata["cookies"] = map[string]any{"__Secure-1PSID": fmt.Sprintf("replacement-%d", round), "__Secure-1PSIDTS": fmt.Sprintf("replacement-%d", round)}
							fresh.Metadata["notes"] = "edited concurrently"
							if _, err := manager.Update(context.Background(), fresh); err != nil {
								t.Fatal(err)
							}
						}
						if scenario == stage {
							status = http.StatusServiceUnavailable
						}
						header := http.Header{"Set-Cookie": {
							fmt.Sprintf("__Secure-1PSID=%s-%d; Domain=.google.com; Path=/; Secure; HttpOnly", stage, round),
							fmt.Sprintf("__Secure-1PSIDTS=%s-%d; Domain=.google.com; Path=/; Secure; HttpOnly", stage, round),
							"SID=unrelated-secret; Path=/; Secure",
						}}
						return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
					})))
					req := coreexecutor.Request{Model: "gemini-app", Payload: []byte(`{"contents":[{"parts":[{"text":"Hello"}]}]}`)}
					opts := coreexecutor.Options{SourceFormat: translator.FormatGemini}
					if stream {
						var result *coreexecutor.StreamResult
						result, err = bound.ExecuteStream(ctx, current, req, opts)
						if err == nil {
							for chunk := range result.Chunks {
								if chunk.Err != nil {
									t.Fatal(chunk.Err)
								}
							}
						}
					} else {
						_, err = bound.Execute(ctx, current, req, opts)
					}
					if (err != nil) != (scenario == "session" || scenario == "generation") {
						t.Fatalf("unexpected execution error: %v", err)
					}
					data, err := os.ReadFile(filepath.Join(dir, auth.FileName))
					if err != nil {
						t.Fatal(err)
					}
					var metadata struct {
						Cookies map[string]string `json:"cookies"`
						Notes   string            `json:"notes"`
					}
					if err := json.Unmarshal(data, &metadata); err != nil {
						t.Fatal(err)
					}
					stage := "generation"
					if scenario == "session" {
						stage = "session"
					}
					notes := "keep"
					if strings.Contains(scenario, "replacement") {
						stage, notes = "replacement", "edited concurrently"
					}
					want := fmt.Sprintf("%s-%d", stage, round)
					if len(metadata.Cookies) != 2 || metadata.Cookies["__Secure-1PSID"] != want || metadata.Cookies["__Secure-1PSIDTS"] != want || metadata.Notes != notes {
						t.Fatal("rotated cookies or unrelated metadata did not survive persistence")
					}
					if strings.Contains(string(data), "transient-csrf") || strings.Contains(string(data), "unrelated-secret") {
						t.Fatal("persisted transient or unrelated credentials")
					}
					saved, _ := manager.GetByID(auth.ID)
					if saved.Metadata["cookies"].(map[string]any)["__Secure-1PSID"] != want {
						t.Fatal("runtime auth still contains stale cookies")
					}
				}
			})
		}
	}
}

func TestGeminiAppServiceConcurrentCookieRotations(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, scenario := range []string{"rotations", "metadata edit", "reimport"} {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, scenario), func(t *testing.T) {
				dir := t.TempDir()
				store := fileauth.NewFileTokenStore()
				store.SetBaseDir(dir)
				manager := coreauth.NewManager(store, nil, nil)
				auth, err := manager.Register(t.Context(), &coreauth.Auth{
					ID: "gemini-app-concurrent.json", FileName: "gemini-app-concurrent.json", Provider: "gemini-app", Status: coreauth.StatusActive,
					Metadata: map[string]any{"type": "gemini-app", "notes": "initial", "cookies": map[string]any{"__Secure-1PSID": "initial-psid", "__Secure-1PSIDTS": "initial-ts"}},
				})
				if err != nil {
					t.Fatal(err)
				}
				svc := &Service{cfg: &config.Config{AuthDir: dir}, coreManager: manager}
				svc.ensureExecutorsForAuth(auth)
				bound, _ := manager.Executor("gemini-app")
				candidate := make([]any, 9)
				candidate[1], candidate[8] = []any{"answer"}, []any{2}
				inner, _ := json.Marshal([]any{nil, nil, nil, nil, []any{candidate}})
				wire, _ := json.Marshal([]any{[]any{"wrb.fr", nil, string(inner)}})
				arrived, release, done := make(chan struct{}, 2), make(chan struct{}), make(chan error, 2)
				defer func() {
					select {
					case <-release:
					default:
						close(release)
					}
				}()
				for _, cookie := range []string{"__Secure-1PSID", "__Secure-1PSIDTS"} {
					ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", http.RoundTripper(geminiAppCookieTransport(func(req *http.Request) (*http.Response, error) {
						if req.URL.Path == "/app" {
							return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"SNlM0e":"csrf"}`))}, nil
						}
						arrived <- struct{}{}
						<-release
						header := http.Header{"Set-Cookie": {cookie + "=rotated; Path=/; Secure"}}
						return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(string(wire)))}, nil
					})))
					go func() {
						req := coreexecutor.Request{Model: "gemini-app", Payload: []byte(`{"contents":[{"parts":[{"text":"Hello"}]}]}`)}
						opts := coreexecutor.Options{SourceFormat: translator.FormatGemini}
						var err error
						if stream {
							var result *coreexecutor.StreamResult
							result, err = bound.ExecuteStream(ctx, auth.Clone(), req, opts)
							if err == nil {
								for chunk := range result.Chunks {
									if chunk.Err != nil {
										err = chunk.Err
									}
								}
							}
						} else {
							_, err = bound.Execute(ctx, auth.Clone(), req, opts)
						}
						done <- err
					}()
				}
				for range 2 {
					select {
					case <-arrived:
					case err := <-done:
						t.Fatalf("request ended before generation barrier: %v", err)
					}
				}
				if scenario != "rotations" {
					edit, _ := manager.GetByID(auth.ID)
					edit.Metadata["notes"] = "edited concurrently"
					if scenario == "reimport" {
						edit.Metadata["cookies"].(map[string]any)["__Secure-1PSID"] = "reimported"
					}
					if _, err := manager.Update(t.Context(), edit); err != nil {
						t.Fatal(err)
					}
				}
				close(release)
				for range 2 {
					if err := <-done; err != nil {
						t.Fatal(err)
					}
				}
				// Reload from disk as well as checking the live manager.
				reloaded := coreauth.NewManager(store, nil, nil)
				if err := reloaded.Load(t.Context()); err != nil {
					t.Fatal(err)
				}
				for _, source := range []*coreauth.Manager{manager, reloaded} {
					current, _ := source.GetByID(auth.ID)
					cookies := current.Metadata["cookies"].(map[string]any)
					wantPSID, wantTS, wantNotes := "rotated", "rotated", "initial"
					if scenario != "rotations" {
						wantNotes = "edited concurrently"
					}
					if scenario == "reimport" {
						wantPSID, wantTS = "reimported", "initial-ts"
					}
					if cookies["__Secure-1PSID"] != wantPSID || cookies["__Secure-1PSIDTS"] != wantTS || current.Metadata["notes"] != wantNotes {
						t.Fatal("concurrent cookies or metadata did not survive persistence")
					}
				}
			})
		}
	}
}
