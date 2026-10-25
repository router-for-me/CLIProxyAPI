package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

type kiroLifecycleTransport func(*http.Request) (*http.Response, error)

func (f kiroLifecycleTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestKiroConcurrentFirstInferencePreparesAndPersistsImport(t *testing.T) {
	const profile = "arn:aws:codewhisperer:eu-central-1:123:profile/fixture"
	var refreshes, profiles, generations atomic.Int32
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", kiroLifecycleTransport(func(req *http.Request) (*http.Response, error) {
		payload := ""
		contentType := "application/json"
		switch {
		case req.URL.Path == "/refreshToken":
			refreshes.Add(1)
			if !strings.HasPrefix(req.Header.Get("User-Agent"), "KiroIDE-0.6.18-") || len(req.Header.Get("User-Agent")) <= len("KiroIDE-0.6.18-") {
				return nil, fmt.Errorf("desktop auth client identification changed: %q", req.Header.Get("User-Agent"))
			}
			payload = `{"accessToken":"access-fixture","refreshToken":"rotated-fixture","expiresIn":3600}`
		case req.Header.Get("X-Amz-Target") == "KiroControlPlaneBearerService.ListAvailableProfiles":
			profiles.Add(1)
			if !strings.HasPrefix(req.Header.Get("User-Agent"), "aws-sdk-js/1.0.0 ua/2.1 os/") || !strings.Contains(req.Header.Get("User-Agent"), "api/kirocontrolplanebearer#1.0.0 m/N,E") || req.Header.Get("x-amz-user-agent") != "aws-sdk-js/1.0.0" || req.Header.Get("amz-sdk-invocation-id") == "" || req.Header.Get("amz-sdk-request") != "attempt=1; max=1" {
				return nil, fmt.Errorf("control-plane headers no longer mimic the IDE client")
			}
			payload = `{"profiles":[{"arn":"` + profile + `"}]}`
		case req.URL.Path == "/generateAssistantResponse":
			generations.Add(1)
			data, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, err
			}
			if req.URL.Host != "runtime.eu-central-1.kiro.dev" || req.Header.Get("Authorization") != "Bearer access-fixture" || gjson.GetBytes(data, "profileArn").String() != profile {
				return nil, fmt.Errorf("inference used stale credentials or the wrong region")
			}
			if !strings.HasPrefix(req.Header.Get("User-Agent"), "aws-sdk-js/1.0.39 ua/2.1 os/") || !strings.Contains(req.Header.Get("User-Agent"), "api/codewhispererstreaming#1.0.39 m/N KiroIDE-") || !strings.HasPrefix(req.Header.Get("x-amz-user-agent"), "aws-sdk-js/1.0.39 KiroIDE-") || req.Header.Get("amz-sdk-invocation-id") == "" || req.Header.Get("amz-sdk-request") != "attempt=1; max=1" || req.Header.Get("x-amzn-codewhisperer-optout") != "true" || req.Header.Get("x-amzn-kiro-agent-mode") != "vibe" || req.Header.Get("x-amzn-kiro-profile-arn") != profile {
				return nil, fmt.Errorf("runtime headers no longer mimic the IDE client")
			}
			contentType = "application/vnd.amazon.eventstream"
			frames := kiroExecutorFrame("assistantResponseEvent", `{"content":"fixture response"}`)
			frames = append(frames, kiroExecutorFrame("metadataEvent", `{"tokenUsage":{"uncachedInputTokens":5,"outputTokens":2}}`)...)
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(bytes.NewReader(frames))}, nil
		default:
			return nil, fmt.Errorf("unexpected request: %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(payload))}, nil
	}))
	files := sdkauth.NewFileTokenStore()
	files.SetBaseDir(t.TempDir())
	manager := coreauth.NewManager(files, nil, nil)
	manager.RegisterExecutor(NewKiroExecutor(&config.Config{}))
	auth, err := manager.Register(ctx, &coreauth.Auth{ID: "kiro-lifecycle.json", FileName: "kiro-lifecycle.json", Provider: "kiro", Metadata: map[string]any{"email": "user@example.com", "provider": "Github", "refreshToken": "refresh-fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "kiro", []*registry.ModelInfo{{ID: "auto"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	const concurrent = 6
	errors := make(chan error, concurrent)
	for range concurrent {
		go func() {
			result, err := manager.Execute(ctx, []string{"kiro"}, coreexecutor.Request{Model: "auto", Payload: []byte(`{"model":"auto","messages":[{"role":"user","content":"Hello"}]}`)}, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
			if err == nil && !bytes.Contains(result.Payload, []byte("fixture response")) {
				err = fmt.Errorf("response translation failed")
			}
			errors <- err
		}()
	}
	for range concurrent {
		if err := <-errors; err != nil {
			t.Error(err)
		}
	}
	if refreshes.Load() != 1 || profiles.Load() != 1 || generations.Load() != concurrent {
		t.Fatalf("requests: refresh=%d profiles=%d generations=%d", refreshes.Load(), profiles.Load(), generations.Load())
	}
	saved, err := files.List(context.Background())
	if err != nil || len(saved) != 1 || saved[0].Metadata["refresh_token"] != "rotated-fixture" || saved[0].Metadata["profile_arn"] != profile {
		t.Fatal("prepared token and profile did not survive persistence")
	}
}

func TestKiroProfileLookupDefersEmptyAndFailedResults(t *testing.T) {
	for _, status := range []int{200, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var lookups atomic.Int32
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", kiroLifecycleTransport(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/refreshToken" {
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"accessToken":"new-access","expiresIn":3600}`))}, nil
				}
				lookups.Add(1)
				body := `{"profiles":[]}`
				if status != 200 {
					body = `{"__type":"ServiceUnavailableException"}`
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			}))
			manager := coreauth.NewManager(nil, nil, nil)
			ex := NewKiroExecutor(&config.Config{})
			auth, err := manager.Register(ctx, &coreauth.Auth{ID: t.Name(), Provider: "kiro", Metadata: map[string]any{"type": "kiro", "access_token": "access", "refresh_token": "refresh", "expired": time.Now().Add(time.Hour).Format(time.RFC3339)}})
			if err != nil {
				t.Fatal(err)
			}
			const parallel = 5
			done := make(chan error, parallel)
			for range parallel {
				go func() { _, err := manager.PrepareRequestAuth(ctx, ex, auth); done <- err }()
			}
			for range parallel {
				if err := <-done; err != nil {
					t.Error(err)
				}
			}
			prepared, _ := manager.GetByID(auth.ID)
			if lookups.Load() != 1 || ex.ShouldPrepareRequestAuth(prepared) {
				t.Fatalf("profile lookup not deferred: calls=%d metadata=%v", lookups.Load(), prepared.Metadata)
			}
			generation := prepared.Generation
			if _, err = manager.PrepareRequestAuth(ctx, ex, prepared); err != nil {
				t.Fatal(err)
			}
			after, _ := manager.GetByID(auth.ID)
			if after.Generation != generation {
				t.Fatal("deferred lookup caused another credential write")
			}
			prepared.Metadata["kiro_profile_retry_after"] = time.Now().Add(-time.Second).Format(time.RFC3339)
			if !ex.ShouldPrepareRequestAuth(prepared) {
				t.Fatal("expired retry delay prevents profile recovery")
			}
			refreshed, err := ex.Refresh(ctx, after)
			if err != nil {
				t.Fatal(err)
			}
			if !ex.ShouldPrepareRequestAuth(refreshed) {
				t.Fatal("manual refresh did not permit another profile lookup")
			}
		})
	}
}
