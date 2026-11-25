package executor

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature/signaturetest"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestAntigravitySignatureCacheDoesNotCrossCredentials(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			cache.ClearSignatureCache("")
			t.Cleanup(func() { cache.ClearSignatureCache("") })

			const model = "claude-sonnet-4-6"
			signature := signaturetest.AntigravityCAQSWithChannel(37)
			response := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"shared thought","thoughtSignature":"` + signature + `"},{"text":"answer"}]},"finishReason":"STOP"}],"modelVersion":"claude-sonnet-4-6","usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"thoughtsTokenCount":3,"totalTokenCount":15}}}`)
			requests := make(chan []byte, 3)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Errorf("read request: %v", errRead)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				requests <- body
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "data: %s\n\n", response)
			}))
			defer server.Close()

			executor := NewAntigravityExecutor(&config.Config{})
			auth := func(id, project, token string) *cliproxyauth.Auth {
				return &cliproxyauth.Auth{
					ID: id,
					Metadata: map[string]any{
						"access_token": token,
						"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
						"project_id":   project,
					},
					Attributes: map[string]string{"base_url": server.URL},
				}
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatClaude,
				ResponseFormat: sdktranslator.FormatClaude,
				Stream:         stream,
			}
			execute := func(auth *cliproxyauth.Auth, payload []byte) {
				t.Helper()
				req := cliproxyexecutor.Request{Model: model, Payload: payload}
				if !stream {
					if _, errExecute := executor.Execute(t.Context(), auth, req, opts); errExecute != nil {
						t.Fatalf("Execute() error = %v", errExecute)
					}
					return
				}
				result, errExecute := executor.ExecuteStream(t.Context(), auth, req, opts)
				if errExecute != nil {
					t.Fatalf("ExecuteStream() error = %v", errExecute)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk error = %v", chunk.Err)
					}
				}
			}

			accountA := auth("account-a", "shared-project", "token-a")
			accountB := auth("account-b", "shared-project", "token-b")
			execute(accountA, []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"start"}]}`))
			<-requests

			recoveryRequest := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"shared thought"}]},{"role":"user","content":"continue"}]}`)
			execute(accountA, recoveryRequest)
			accountAUpstream := <-requests
			if !containsSignature(accountAUpstream, signature) {
				t.Fatalf("account A did not recover its thoughtSignature: %s", accountAUpstream)
			}

			execute(accountB, recoveryRequest)
			accountBUpstream := <-requests
			if containsSignature(accountBUpstream, signature) {
				t.Fatalf("account B request contains account A thoughtSignature: %s", accountBUpstream)
			}
		})
	}
}

func TestAntigravitySignatureCacheScopeUsesCredentialIdentity(t *testing.T) {
	sharedProject := func(refreshToken string) *cliproxyauth.Auth {
		return &cliproxyauth.Auth{Metadata: map[string]any{
			"project_id":    "shared-project",
			"refresh_token": refreshToken,
		}}
	}
	accountA := sharedProject("refresh-a")
	accountB := sharedProject("refresh-b")
	if antigravitySignatureCacheScope(accountA) == antigravitySignatureCacheScope(accountB) {
		t.Fatal("credentials in the same project shared a signature cache scope")
	}

	sameID := func(token string) *cliproxyauth.Auth {
		return &cliproxyauth.Auth{ID: "same-record", Metadata: map[string]any{
			"project_id":    "shared-project",
			"refresh_token": token,
		}}
	}
	if antigravitySignatureCacheScope(sameID("refresh-a")) == antigravitySignatureCacheScope(sameID("refresh-b")) {
		t.Fatal("replacement credential reused the prior signature cache scope")
	}
	replaced := sameID("refresh-b")
	replaced.Metadata[antigravitySignatureGrantScopeMetadataKey] = antigravityCredentialScope("grant:", "refresh-a\x00")
	replaced.Metadata[antigravitySignatureGrantBindingMetadataKey] = antigravityCredentialScope("grant:", "refresh-a\x00")
	priorScope := antigravitySignatureCacheScope(sameID("refresh-a"))
	if antigravitySignatureCacheScope(replaced) == priorScope {
		t.Fatal("replacement credential inherited a stale persisted signature scope")
	}
	oldBinding := antigravitySignatureGrantBinding(replaced)
	delete(replaced.Metadata, antigravitySignatureGrantScopeMetadataKey)
	replaced.Metadata[antigravitySignatureGrantBindingMetadataKey] = oldBinding
	if antigravitySignatureCacheScope(replaced) == priorScope {
		t.Fatal("replacement credential rebound stale signature scope after refresh")
	}
	refreshed := sameID("refresh-old")
	refreshed.Metadata["email"] = "person@example.com"
	beforeRefresh := antigravitySignatureCacheScope(refreshed)
	refreshed.Metadata[antigravitySignatureGrantScopeMetadataKey] = antigravitySignatureGrantIdentity(refreshed)
	refreshed.Metadata[antigravitySignatureGrantBindingMetadataKey] = antigravitySignatureGrantBinding(refreshed)
	refreshed.Metadata["access_token"] = "access-new"
	refreshed.Metadata[antigravitySignatureGrantBindingMetadataKey] = antigravitySignatureGrantBinding(refreshed)
	if afterAccessRefresh := antigravitySignatureCacheScope(refreshed); afterAccessRefresh != beforeRefresh {
		t.Fatalf("access refresh changed scope: before=%q after=%q", beforeRefresh, afterAccessRefresh)
	}
	refreshed.Metadata["refresh_token"] = "refresh-new"
	refreshed.Metadata[antigravitySignatureGrantBindingMetadataKey] = antigravitySignatureGrantBinding(refreshed)
	if afterRefreshGrant := antigravitySignatureCacheScope(refreshed); afterRefreshGrant != beforeRefresh {
		t.Fatalf("adopted grant rotation changed scope: before=%q after=%q", beforeRefresh, afterRefreshGrant)
	}
	if scope := antigravitySignatureCacheScope(&cliproxyauth.Auth{Metadata: map[string]any{"project_id": "shared-project"}}); scope != "" {
		t.Fatalf("anonymous credential scope = %q, want empty", scope)
	}
}

func containsSignature(payload []byte, signature string) bool {
	found := false
	var visit func(ojson.Result) bool
	visit = func(value ojson.Result) bool {
		if value.IsArray() || value.IsObject() {
			value.ForEach(func(_, child ojson.Result) bool { return visit(child) })
			return !found
		}
		if value.Type == ojson.String && value.String() == signature {
			found = true
			return false
		}
		return !found
	}
	visit(ojson.ParseBytes(payload))
	return found
}
