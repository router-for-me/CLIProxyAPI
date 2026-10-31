//go:build cgo && (linux || darwin || freebsd)

package pluginhost

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestNativeAuthRefreshPostEmissionNoReplay(t *testing.T) {
	for _, termination := range []string{"done", "unauthorized-frame", "broken-transport"} {
		t.Run(termination, func(t *testing.T) {
			native := nativeAuthRefreshBuildNativeHost(t)
			upstream := &nativeAuthRefreshLoopback{}
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/oauth/token" {
					upstream.ServeHTTP(w, r)
					return
				}
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				var payload struct {
					Model  string `json:"model"`
					Stream bool   `json:"stream"`
				}
				if err := json.Unmarshal(raw, &payload); err != nil {
					t.Error(err)
					return
				}
				upstream.mu.Lock()
				upstream.requests = append(upstream.requests, nativeAuthRefreshUpstreamRequest{Path: r.URL.Path, Stream: payload.Stream, Authorization: r.Header.Get("Authorization"), Body: string(raw)})
				upstream.mu.Unlock()
				if r.URL.Path != "/v1/chat/completions" || !payload.Stream || r.Header.Get("Authorization") != "Bearer synthetic-access-rotated" {
					http.Error(w, "invalid native stream request", 400)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "data: {\"id\":\"native-auth-refresh-stream\",\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{\"content\":\"native-auth-refresh-first-emitted\"},\"finish_reason\":null}]}\n\n", payload.Model)
				w.(http.Flusher).Flush()
				<-release
				switch termination {
				case "done":
					_, _ = fmt.Fprintf(w, "data: {\"id\":\"native-auth-refresh-stream\",\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", payload.Model)
					w.(http.Flusher).Flush()
				case "unauthorized-frame":
					_, _ = io.WriteString(w, "data: {\"error\":{\"message\":\"post-emission rejection\",\"type\":\"authentication_error\",\"code\":401}}\n\n")
					w.(http.Flusher).Flush()
				case "broken-transport":
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
				}
			}))
			t.Cleanup(provider.Close)
			credential := nativeAuthRefreshCredential("native-auth-refresh-stream.json", "expired", provider.URL)
			credential["test_refresh_policy"] = "on-demand"
			auth := nativeAuthRefreshParse(t, native.host, native.cfg.AuthDir, credential)
			manager := nativeAuthRefreshManager(t, native.cfg, native.host, auth)
			server := nativeAuthRefreshHandlerServer(t, native.cfg, manager, native.host)
			request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"native-auth-refresh-chat","messages":[{"role":"user","content":"synthetic"}],"stream":true}`))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Content-Type", "application/json")
			resp, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			reader := bufio.NewReader(resp.Body)
			var emitted strings.Builder
			for !strings.Contains(emitted.String(), "native-auth-refresh-first-emitted") {
				line, err := reader.ReadString('\n')
				emitted.WriteString(line)
				if err != nil {
					t.Fatalf("no first content emitted before termination: status=%d body=%s error=%v", resp.StatusCode, emitted.String(), err)
				}
			}
			before := upstream.snapshot()
			if resp.StatusCode != http.StatusOK || len(before) != 2 || before[0].Path != "/oauth/token" || before[1].Authorization != "Bearer synthetic-access-rotated" {
				t.Fatalf("refresh did not precede physical stream opening: status=%d requests=%+v", resp.StatusCode, before)
			}
			unblock()
			remainder, err := io.ReadAll(reader)
			if err != nil {
				t.Fatalf("downstream could not report stream termination: %v", err)
			}
			emitted.Write(remainder)
			body := emitted.String()
			counters := nativeAuthRefreshNativeCounters(t, native.client)
			seen := upstream.snapshot()
			if counters.Refresh != 1 || counters.ConsistencyErrors != 0 || len(seen) != 2 || strings.Count(body, "native-auth-refresh-first-emitted") != 1 {
				t.Fatalf("partially emitted native stream replayed: native=%+v requests=%+v body=%s", counters, seen, body)
			}
			if termination == "done" {
				if !strings.Contains(body, "[DONE]") || strings.Contains(body, `"error"`) {
					t.Fatalf("success control=%s", body)
				}
			} else if !strings.Contains(body, `"error"`) {
				t.Fatalf("failure after first emitted content was swallowed: %s", body)
			}
			if termination == "unauthorized-frame" && !strings.Contains(body, "post-emission rejection") {
				t.Fatalf("actual rejection lost: %s", body)
			}
			if termination == "broken-transport" && !strings.Contains(body, "unexpected EOF") {
				t.Fatalf("actual transport failure lost: %s", body)
			}
			t.Logf("termination=%s native-refresh=%d token+inference=%d emitted-once=true body=%s", termination, counters.Refresh, len(seen), body)
		})
	}
}
