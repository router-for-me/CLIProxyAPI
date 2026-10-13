package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	fileauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// These tests are deliberately serial: all traffic is routed to a loopback-only
// synthetic TLS server, and unexpected hosts or paths are rejected.
type codexManagerRoundTripper func(*http.Request) (*http.Response, error)

func (f codexManagerRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func codexManagerIntercept(t *testing.T, handler codexManagerRoundTripper) {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response, err := handler(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	server.StartTLS()

	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ForceAttemptHTTP2 = false
	transport.TLSNextProto = nil
	transport.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	transport.TLSClientConfig.ServerName = "example.com"
	transport.DialTLSContext = nil
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	previous := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() {
		http.DefaultTransport = previous
		server.Close()
	})
}
func codexManagerResponse(r *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}
}
func codexManagerTokenResponse(r *http.Request, access, refresh string) *http.Response {
	body, _ := json.Marshal(map[string]any{
		"access_token": access, "refresh_token": refresh,
		"id_token":   makeTestCodexRefreshJWT("pro", "fixture-account"),
		"expires_in": 172800, "token_type": "Bearer",
		"scope": "openid email profile offline_access api.responses.read api.responses.write chatgpt.tokens.use.direct",
	})
	return codexManagerResponse(r, http.StatusOK, string(body))
}
func codexManagerReadDisk(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var disk map[string]any
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	return disk
}
func codexManagerWriteDisk(t *testing.T, path string, disk map[string]any) {
	t.Helper()
	raw, err := json.Marshal(disk)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCodexManagerRefresh_RotatedTokensSurvivePersistenceFailure(t *testing.T) {
	for _, failure := range []string{"write", "read", "invalid-json"} {
		t.Run(failure, func(t *testing.T) {
			if failure != "invalid-json" && (runtime.GOOS == "windows" || os.Geteuid() == 0) {
				t.Skip("requires Unix permission enforcement for the current user")
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "codex-fixture.json")
			metadata := map[string]any{
				"type": "codex", "access_token": "fixture-old-access", "refresh_token": "fixture-old-refresh",
				"expired": time.Now().Add(time.Hour).Format(time.RFC3339), "note": "original",
			}
			codexManagerWriteDisk(t, path, metadata)
			t.Cleanup(func() { _ = os.Chmod(dir, 0700); _ = os.Chmod(path, 0600) })
			calls := 0
			codexManagerIntercept(t, func(r *http.Request) (*http.Response, error) {
				host := strings.Split(r.Host, ":")[0]
				if host != "auth.openai.com" || (r.URL.Path != "/oauth/token" && r.URL.Path != "/api/accounts/oauth/token") {
					return nil, fmt.Errorf("unexpected HTTP request blocked")
				}
				if err := r.ParseForm(); err != nil {
					return nil, err
				}
				switch r.Form.Get("refresh_token") {
				case "fixture-new-refresh":
					calls++
					return codexManagerTokenResponse(r, "fixture-next-access", "fixture-next-refresh"), nil
				case "fixture-old-refresh":
					if calls > 0 {
						return codexManagerResponse(r, 400, `{"error":"invalid_grant"}`), nil
					}
					calls++
					// The initial read and lock succeeded. Fail only after OAuth rotation.
					switch failure {
					case "write":
						if err := os.Chmod(dir, 0500); err != nil {
							return nil, err
						}
					case "read":
						if err := os.Chmod(path, 0000); err != nil {
							return nil, err
						}
					case "invalid-json":
						if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
							return nil, err
						}
					}
					return codexManagerTokenResponse(r, "fixture-new-access", "fixture-new-refresh"), nil
				default:
					return nil, fmt.Errorf("unexpected refresh fixture")
				}
			})
			store := fileauth.NewFileTokenStore()
			store.SetBaseDir(dir)
			m := cliproxyauth.NewManager(store, nil, nil)
			m.RegisterExecutor(NewCodexExecutor(&config.Config{}))
			a := &cliproxyauth.Auth{ID: "codex-fixture.json", FileName: "codex-fixture.json", Provider: "codex", Metadata: metadata, Attributes: map[string]string{cliproxyauth.AttributePath: path}}
			if _, err := m.Register(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			if _, err := m.ForceRefreshAuth(context.Background(), a.ID); err != nil {
				t.Fatalf("successful rotation must remain usable: %v", err)
			}
			current, ok := m.GetByID(a.ID)
			if !ok || calls != 1 || current.Metadata["access_token"] != "fixture-new-access" || current.Metadata["refresh_token"] != "fixture-new-refresh" {
				t.Fatal("manager discarded the successful rotation")
			}
			outer, siwc := current.CodexRefreshPatch.Partitions()
			if !outer || siwc {
				t.Fatal("lost or broadened the pending outer-only patch")
			}
			switch failure {
			case "write":
				if disk := codexManagerReadDisk(t, path); disk["refresh_token"] != "fixture-old-refresh" {
					t.Fatal("write-failure fixture did not leave stale disk tokens")
				}
			case "read":
				if _, err := os.ReadFile(path); !os.IsPermission(err) {
					t.Fatalf("read-failure fixture: want permission error, got %v", err)
				}
			case "invalid-json":
				raw, err := os.ReadFile(path)
				if err != nil || json.Valid(raw) {
					t.Fatal("invalid-JSON fixture did not fail persistence")
				}
			}
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0600); err != nil {
				t.Fatal(err)
			}
			// Repair storage with its old credential version and concurrent operator
			// edits, plus an independently updated healthy SIWC partition.
			disk := a.Clone().Metadata
			disk["note"] = "operator edit"
			disk["use_siwc"] = false
			disk["siwc"] = map[string]any{"access_token": "fixture-disk-siwc", "expired": time.Now().Add(time.Hour).Format(time.RFC3339), "note": "keep"}
			codexManagerWriteDisk(t, path, disk)
			if _, err := m.ForceRefreshAuth(context.Background(), a.ID); err != nil {
				t.Fatalf("refresh after storage repair: %v", err)
			}
			current, _ = m.GetByID(a.ID)
			disk = codexManagerReadDisk(t, path)
			if calls != 2 || current.Metadata["refresh_token"] != "fixture-next-refresh" || disk["refresh_token"] != "fixture-next-refresh" {
				t.Fatal("follow-up refresh replayed stale disk tokens instead of the pending rotation")
			}
			if disk["note"] != "operator edit" || disk["use_siwc"] != false || disk["siwc"].(map[string]any)["access_token"] != "fixture-disk-siwc" || disk["siwc"].(map[string]any)["note"] != "keep" {
				t.Fatal("recovery overwrote concurrent disk configuration or healthy SIWC credentials")
			}
			if current.CodexRefreshPatch != nil {
				t.Fatal("persisted rotation still pending")
			}
		})
	}
}

func TestCodexManagerRefresh_UnauthorizedRefreshesRejectedPartition(t *testing.T) {
	for _, useSIWC := range []bool{true, false} {
		for _, inactiveDue := range []bool{true, false} {
			t.Run(fmt.Sprintf("use_siwc=%v/inactive_due=%v", useSIWC, inactiveDue), func(t *testing.T) {
				outerExp := time.Now().Add(48 * time.Hour)
				siwcExp := time.Now().Add(time.Hour)
				rejected, replacement := "fixture-old-outer-access", "fixture-new-outer-access"
				if useSIWC {
					rejected, replacement = "fixture-old-siwc-access", "fixture-new-siwc-access"
					if inactiveDue {
						outerExp = time.Now().Add(time.Hour)
					}
				} else if inactiveDue {
					siwcExp = time.Now().Add(time.Minute)
				}
				dir := t.TempDir()
				path := filepath.Join(dir, "codex-fixture.json")
				a := &cliproxyauth.Auth{ID: "codex-fixture.json", FileName: "codex-fixture.json", Provider: "codex", Attributes: map[string]string{cliproxyauth.AttributePath: path}, Metadata: map[string]any{
					"type": "codex", "use_siwc": useSIWC,
					"access_token": "fixture-old-outer-access", "refresh_token": "fixture-old-outer-refresh", "expired": outerExp.Format(time.RFC3339),
					"siwc": map[string]any{"access_token": "fixture-old-siwc-access", "refresh_token": "fixture-old-siwc-refresh", "client_id": "fixture-client", "expired": siwcExp.Format(time.RFC3339)},
				}}
				codexManagerWriteDisk(t, path, a.Metadata)
				outerCalls, siwcCalls := 0, 0
				var attempted []string
				codexManagerIntercept(t, func(r *http.Request) (*http.Response, error) {
					host := strings.Split(r.Host, ":")[0]
					if (host == "api.openai.com" && r.URL.Path == "/v1/responses") || (host == "chatgpt.com" && r.URL.Path == "/backend-api/codex/responses") {
						token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
						attempted = append(attempted, token)
						if token == rejected {
							return codexManagerResponse(r, 401, `{"error":{"message":"fixture access token rejected"}}`), nil
						}
						if token != replacement {
							return nil, fmt.Errorf("unexpected execution fixture")
						}
						return codexManagerResponse(r, 200, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"fixture-response\",\"status\":\"completed\",\"output\":[]}}\n\n"), nil
					}
					host = strings.Split(r.Host, ":")[0]
					if host != "auth.openai.com" || (r.URL.Path != "/oauth/token" && r.URL.Path != "/api/accounts/oauth/token") {
						return nil, fmt.Errorf("unexpected HTTP request blocked")
					}
					if err := r.ParseForm(); err != nil {
						return nil, err
					}
					switch r.Form.Get("refresh_token") {
					case "fixture-old-outer-refresh":
						outerCalls++
						return codexManagerTokenResponse(r, "fixture-new-outer-access", "fixture-new-outer-refresh"), nil
					case "fixture-old-siwc-refresh":
						siwcCalls++
						return codexManagerTokenResponse(r, "fixture-new-siwc-access", "fixture-new-siwc-refresh"), nil
					default:
						return nil, fmt.Errorf("unexpected refresh fixture")
					}
				})
				store := fileauth.NewFileTokenStore()
				store.SetBaseDir(dir)
				m := cliproxyauth.NewManager(store, nil, nil)
				m.RegisterExecutor(NewCodexExecutor(&config.Config{}))
				model := "codex-refresh-fixture-model"
				reg := registry.GetGlobalRegistry()
				reg.RegisterClient(a.ID, "codex", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { reg.UnregisterClient(a.ID) })
				if _, err := m.Register(context.Background(), a); err != nil {
					t.Fatal(err)
				}
				ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.DefaultTransport)
				_, err := m.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model, Payload: []byte(`{"input":[]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
				if err != nil {
					t.Fatalf("401 recovery: %v", err)
				}
				current, _ := m.GetByID(a.ID)
				token, _ := codexCreds(current)
				if token != replacement || len(attempted) != 2 || attempted[0] != rejected || attempted[1] != replacement {
					t.Fatalf("want rejected token then rotated token, got %v", attempted)
				}
				wantOuter, wantSIWC := 1, 1
				if !inactiveDue {
					if useSIWC {
						wantOuter = 0
					} else {
						wantSIWC = 0
					}
				}
				if outerCalls != wantOuter || siwcCalls != wantSIWC {
					t.Fatalf("refreshes outer/SIWC = %d/%d, want %d/%d", outerCalls, siwcCalls, wantOuter, wantSIWC)
				}
				if current.RejectedAccessToken != "" {
					t.Fatal("successful refresh left rejection state")
				}
				disk := codexManagerReadDisk(t, path)
				diskAuth := &cliproxyauth.Auth{Metadata: disk}
				if token, _ := codexCreds(diskAuth); token != replacement {
					t.Fatal("recovered active token was not persisted")
				}
			})
		}
	}
}
