package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Opt-in: installed native client, synthetic credentials, temporary private home,
// real bound routes + executor. No user auth/config and no external generation.
func TestDelegatedLaunchNativeAppServer(t *testing.T) {
	binary := os.Getenv("QUOTA_NATIVE_CODEX")
	if binary == "" {
		t.Skip("set QUOTA_NATIVE_CODEX to the installed codex binary for offline compatibility proof")
	}
	for _, mode := range []string{"healthy", "long-turn", "compact", "http-quota", "sse-quota", "throttle", "capacity", "tool-quota"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newDelegatedHTTPFixture(t)
			r := f.prepare(t)
			f.mode.Store(mode)
			f.quota.Store(mode == "http-quota")
			server := httptest.NewServer(f.engine)
			defer server.Close()
			home := t.TempDir()
			if err := os.Chmod(home, 0700); err != nil {
				t.Fatal(err)
			}
			args := []string{"app-server"}
			opts := map[string]any{
				"model_provider": "offline", "model_providers.offline.name": "Offline acceptance",
				"model_providers.offline.base_url":                      server.URL + r.BasePath,
				"model_providers.offline.env_key":                       "QUOTA_SYNTHETIC_KEY",
				"model_providers.offline.http_headers.X-Test-Principal": "client",
				"model_providers.offline.wire_api":                      "responses", "model_providers.offline.supports_websockets": false,
				"model_providers.offline.request_max_retries": 0, "model_providers.offline.stream_max_retries": 0,
				"analytics.enabled": false, "web_search": "disabled",
			}
			for k, v := range opts {
				b, _ := json.Marshal(v)
				args = append(args, "-c", k+"="+string(b))
			}
			cmd := exec.Command(binary, args...)
			cmd.Dir = home
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "CODEX_HOME=" + home, "QUOTA_SYNTHETIC_KEY=synthetic-only"}
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			events := make(chan map[string]any, 256)
			go func() {
				defer close(events)
				scan := bufio.NewScanner(stdout)
				scan.Buffer(make([]byte, 4096), 2<<20)
				for scan.Scan() {
					var e map[string]any
					if json.Unmarshal(scan.Bytes(), &e) == nil {
						events <- e
					}
				}
			}()
			var evidence []map[string]any
			defer func() {
				_ = stdin.Close()
				wait := make(chan error, 1)
				go func() { wait <- cmd.Wait() }()
				select {
				case <-wait:
				case <-time.After(8 * time.Second):
					t.Error("own offline native child did not exit on stdin closure")
					_ = cmd.Process.Kill()
					<-wait
				}
				if dir := os.Getenv("QUOTA_NATIVE_EVIDENCE_PATH"); dir != "" {
					b, _ := json.MarshalIndent(evidence, "", "  ")
					_ = os.WriteFile(filepath.Join(dir, "native-"+mode+".json"), b, 0600)
					_ = os.WriteFile(filepath.Join(dir, "native-"+mode+"-stderr.log"), stderr.Bytes(), 0600)
				}
			}()
			send := func(e any) {
				b, _ := json.Marshal(e)
				if _, err := fmt.Fprintln(stdin, string(b)); err != nil {
					t.Fatal(err)
				}
			}
			toolSeen := false
			until := func(id float64, completed bool) map[string]any {
				t.Helper()
				timer := time.NewTimer(30 * time.Second)
				defer timer.Stop()
				for {
					select {
					case e, ok := <-events:
						if !ok {
							t.Fatalf("native exited: %s", stderr.String())
						}
						evidence = append(evidence, e)
						if e["method"] == "item/tool/call" {
							toolSeen = true
							send(map[string]any{"id": e["id"], "result": map[string]any{"contentItems": []map[string]any{{"type": "inputText", "text": "synthetic useful tool result"}}, "success": true}})
						}
						if id != 0 && e["id"] == id {
							if e["error"] != nil {
								t.Fatalf("native RPC error: %v", e)
							}
							return e
						}
						if completed && (e["method"] == "turn/completed" || e["method"] == "thread/compacted" || (e["method"] == "item/completed" && e["params"].(map[string]any)["item"].(map[string]any)["type"] == "contextCompaction")) {
							return e
						}
					case <-timer.C:
						t.Fatalf("native loopback timeout (%s), generations=%d", mode, f.generations.Load())
					}
				}
			}
			send(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]any{"name": "quota-offline", "version": "1"}, "capabilities": map[string]any{"experimentalApi": true}}})
			until(1, false)
			send(map[string]any{"method": "initialized", "params": map[string]any{}})
			params := map[string]any{"model": "gpt-6.1-sol", "modelProvider": "offline", "cwd": home, "approvalPolicy": "never", "sandbox": "read-only", "ephemeral": true}
			if mode == "tool-quota" {
				params["dynamicTools"] = []map[string]any{{"name": "control", "description": "Offline synthetic control only", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}}
			}
			send(map[string]any{"id": 2, "method": "thread/start", "params": params})
			e := until(2, false)
			tid := e["result"].(map[string]any)["thread"].(map[string]any)["id"].(string)
			send(map[string]any{"id": 3, "method": "turn/start", "params": map[string]any{"threadId": tid, "input": []map[string]any{{"type": "text", "text": "Reply OK. No tools except the synthetic control if provided.", "text_elements": []any{}}}}})
			completed := until(0, true)
			if (mode == "healthy" || mode == "long-turn" || mode == "compact") && completed["params"].(map[string]any)["turn"].(map[string]any)["status"] != "completed" {
				t.Fatal("healthy native turn failed")
			}
			expected := int32(1)
			if mode == "long-turn" {
				f.clock.Add(int64(31 * time.Second))
				if time.Unix(0, f.clock.Load()).Before(r.ExpiresAt) {
					t.Fatal("fake clock did not exceed pending expiry")
				}
				send(map[string]any{"id": 4, "method": "turn/start", "params": map[string]any{"threadId": tid, "input": []map[string]any{{"type": "text", "text": "Second normal turn after a long tool delay. Reply OK.", "text_elements": []any{}}}}})
				second := until(0, true)
				if second["params"].(map[string]any)["turn"].(map[string]any)["status"] != "completed" {
					t.Fatal("established second native turn failed")
				}
				expected = 2
			}
			if mode == "compact" {
				send(map[string]any{"id": 4, "method": "thread/compact/start", "params": map[string]any{"threadId": tid}})
				until(4, false)
				until(0, true)
				expected = 2
			}
			if mode == "tool-quota" {
				expected = 2
				if !toolSeen {
					t.Fatal("useful tool event was not delivered")
				}
			}
			if f.generations.Load() != expected {
				t.Fatalf("generation attempts %d want %d", f.generations.Load(), expected)
			}
			if cache, _ := f.cacheKey.Load().(string); cache == "" || cache == r.SessionID {
				t.Fatalf("native did not provide its independent cache key: %q", cache)
			}
			state := f.call("GET", r.BasePath+"/state", "client", "")
			var control auth.LaunchBindingState
			if json.Unmarshal(state.Body.Bytes(), &control) != nil {
				t.Fatal("invalid control state")
			}
			if mode == "healthy" || mode == "long-turn" || mode == "compact" {
				for _, event := range evidence {
					if event["method"] == "error" {
						t.Fatalf("healthy/compact native emitted error: %v", event)
					}
				}
				if mode == "compact" && f.compactions.Load() != 0 {
					t.Fatal("update native compact transport expectation: custom provider now uses remote compact")
				}
				if control.State != "established" {
					t.Fatal(state.Body.String())
				}
			} else {
				typed := false
				for _, event := range evidence {
					if event["method"] != "error" {
						continue
					}
					payload := event["params"].(map[string]any)["error"].(map[string]any)
					info := payload["codexErrorInfo"]
					b, _ := json.Marshal(event)
					t.Logf("native error: %s", b)
					expectedCode := "usageLimitExceeded"
					if mode == "throttle" {
						expectedCode = "rateLimitExceeded"
					}
					if mode == "capacity" {
						expectedCode = "serverOverloaded"
					}
					if mode == "http-quota" {
						if details, ok := info.(map[string]any); ok {
							if exhausted, ok := details["responseTooManyFailedAttempts"].(map[string]any); ok {
								typed = exhausted["httpStatusCode"] == float64(429)
							}
						}
					} else {
						typed = info == expectedCode
					}
				}
				if !typed {
					t.Fatal("native lost expected typed quota/throttle/capacity signal")
				}
				expectedCategory, expectedScope := "usage_limit", "account"
				if mode == "throttle" {
					expectedCategory, expectedScope = "throttled", "transient"
				}
				if mode == "capacity" {
					expectedCategory, expectedScope = "model_capacity", "model"
				}
				if control.Refusal == nil || control.Refusal.Code != expectedCategory || control.Refusal.QuotaScope != expectedScope {
					t.Fatalf("wrong trusted category: %+v", control)
				}
				if expectedScope != "account" && len(control.Refusal.QuotaDomains) != 0 {
					t.Fatal("nonexhaustion blocked account domain")
				}
				// Native retries must be disabled; the control state also prevents duplicate
				// provider execution on an accidental retry after a classified refusal.
				retry := f.call("POST", r.BasePath+"/responses", "client", `{"model":"gpt-6.1-sol"}`)
				if retry.Code != 429 || f.generations.Load() != expected {
					t.Fatal("classified refusal replayed generation")
				}
				if control.ExecutorAttempts != int(expected) {
					t.Fatal(state.Body.String())
				}
			}
			t.Logf("native HTTP bound route: mode=%s provider_requests=%d compact_requests=%d useful_tool=%t state=%s", mode, f.generations.Load(), f.compactions.Load(), toolSeen, state.Body.String())
		})
	}
}
