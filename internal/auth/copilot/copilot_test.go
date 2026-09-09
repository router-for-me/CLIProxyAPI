package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func testClient(t *testing.T, fn roundTripFunc) *Client {
	t.Helper()
	return &Client{httpClient: &http.Client{Transport: fn}, githubURL: "https://github.test", apiURL: "https://api.github.test/" + t.Name()}
}

func TestDeviceAuthorizationAndSlowDown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var polls []time.Time
		client := testClient(t, func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("Accept") != "application/json" {
				t.Error("missing JSON accept header")
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("client_id") != ClientID {
				t.Error("wrong OAuth app")
			}
			if strings.HasSuffix(r.URL.Path, "/device/code") {
				return response(`{"device_code":"secret-code","user_code":"ABCD-EFGH","verification_uri":"https://github.com/login/device","expires_in":900,"interval":1}`), nil
			}
			if r.Form.Get("device_code") != "secret-code" || r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" {
				t.Error("wrong device grant")
			}
			polls = append(polls, time.Now())
			switch len(polls) {
			case 1:
				return response(`{"error":"authorization_pending"}`), nil
			case 2:
				return response(`{"error":"slow_down"}`), nil
			}
			return response(`{"access_token":"github-token"}`), nil
		})
		code, err := client.StartDeviceFlow(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		token, err := client.WaitForAuthorization(context.Background(), code)
		if err != nil {
			t.Fatal(err)
		}
		if token != "github-token" || len(polls) != 3 {
			t.Fatalf("token=%q polls=%d", token, len(polls))
		}
		if polls[1].Sub(polls[0]) != time.Second || polls[2].Sub(polls[1]) != 6*time.Second {
			t.Fatalf("polling did not honor slow_down: %v", polls)
		}
	})
}

func TestDeviceAuthorizationDeniedExpiredAndCancelled(t *testing.T) {
	for _, code := range []string{"access_denied", "expired_token", "unknown_error", ""} {
		t.Run(code, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				client := testClient(t, func(*http.Request) (*http.Response, error) { return response(fmt.Sprintf(`{"error":%q}`, code)), nil })
				_, err := client.WaitForAuthorization(context.Background(), &DeviceCode{DeviceCode: "code", ExpiresIn: 30, Interval: 1})
				if err == nil {
					t.Fatal("accepted failed authorization")
				}
			})
		})
	}
	client := testClient(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("cancelled login sent a request")
		return nil, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.WaitForAuthorization(ctx, &DeviceCode{ExpiresIn: 30}); err != context.Canceled {
		t.Fatalf("error=%v", err)
	}
}

func TestDeviceAuthorizationRetriesTransientFailures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var polls []time.Time
		client := testClient(t, func(*http.Request) (*http.Response, error) {
			polls = append(polls, time.Now())
			switch len(polls) {
			case 1:
				return nil, &net.DNSError{IsTimeout: true}
			case 2:
				res := response(`{}`)
				res.StatusCode = http.StatusServiceUnavailable
				return res, nil
			default:
				return response(`{"access_token":"github-token"}`), nil
			}
		})
		token, err := client.WaitForAuthorization(context.Background(), &DeviceCode{DeviceCode: "code", ExpiresIn: 30, Interval: 1})
		if err != nil || token != "github-token" {
			t.Fatalf("authorization did not recover: %v", err)
		}
		if len(polls) != 3 || polls[1].Sub(polls[0]) != 2*time.Second || polls[2].Sub(polls[1]) != 4*time.Second {
			t.Fatalf("retry did not back off: %v", polls)
		}
	})
}

func TestTokenCacheCoalescesAndRenews(t *testing.T) {
	var exchanges atomic.Int32
	client := testClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "token github-token" {
			t.Error("token exchange used wrong credential")
		}
		exchanges.Add(1)
		return response(fmt.Sprintf(`{"token":"copilot-token","expires_at":%d,"endpoints":{"api":"https://api.business.githubcopilot.com"}}`, time.Now().Add(time.Hour).Unix())), nil
	})
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			token, err := client.CopilotToken(context.Background(), "github-token", false)
			if err != nil || token.Token != "copilot-token" {
				t.Errorf("token=%v err=%v", token, err)
			}
		})
	}
	wg.Wait()
	if exchanges.Load() != 1 {
		t.Fatalf("exchanges=%d", exchanges.Load())
	}
	if _, err := client.CopilotToken(context.Background(), "github-token", true); err != nil {
		t.Fatal(err)
	}
	if exchanges.Load() != 2 {
		t.Fatalf("forced refresh exchanges=%d", exchanges.Load())
	}
	other := *client
	other.proxyURL = "http://other-proxy.test"
	if _, err := other.CopilotToken(context.Background(), "github-token", false); err != nil {
		t.Fatal(err)
	}
	if exchanges.Load() != 3 {
		t.Fatal("cache shared across proxies")
	}
}

func TestTokenValidationAndErrorRedaction(t *testing.T) {
	for _, endpoint := range []string{"http://api.githubcopilot.com", "https://githubcopilot.com.evil.test", "https://user:pass@api.githubcopilot.com", "https://api.githubcopilot.com?token=secret"} {
		t.Run(endpoint, func(t *testing.T) {
			client := testClient(t, func(*http.Request) (*http.Response, error) {
				return response(fmt.Sprintf(`{"token":"copilot-token","expires_at":%d,"endpoints":{"api":%q}}`, time.Now().Add(time.Hour).Unix(), endpoint)), nil
			})
			if _, err := client.CopilotToken(context.Background(), "github-token", false); err == nil {
				t.Fatal("accepted unsafe endpoint")
			}
		})
	}
	client := testClient(t, func(*http.Request) (*http.Response, error) {
		r := response(`{"access_token":"must-not-leak"}`)
		r.StatusCode = 403
		return r, nil
	})
	_, err := client.CopilotToken(context.Background(), "github-token", false)
	if err == nil || strings.Contains(err.Error(), "must-not-leak") {
		t.Fatalf("unsafe error=%v", err)
	}
}

func TestAccountIdentityAndQuota(t *testing.T) {
	client := testClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "token github-token" {
			t.Error("wrong GitHub credential")
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/copilot_internal/user"):
			return response(`{"copilot_plan":"individual","quota_snapshots":{"premium_interactions":{"entitlement":300,"remaining":225,"percent_remaining":75}}}`), nil
		case strings.HasSuffix(r.URL.Path, "/v2/token"):
			return response(fmt.Sprintf(`{"token":"short-lived","expires_at":%d}`, time.Now().Add(time.Hour).Unix())), nil
		default:
			return response(`{"login":"octocat","id":42}`), nil
		}
	})
	metadata, err := client.Login(context.Background(), "github-token")
	if err != nil {
		t.Fatal(err)
	}
	if metadata["github_id"] != int64(42) || metadata["access_token"] != "github-token" {
		t.Fatalf("wrong persistent account metadata")
	}
	if _, ok := metadata["token"]; ok {
		t.Fatal("persisted short-lived token")
	}
	quota, err := client.Quota(context.Background(), "github-token")
	if err != nil || !json.Valid(quota) {
		t.Fatalf("quota err=%v", err)
	}
}

func TestModelDiscoveryCapabilitiesAndEndpoints(t *testing.T) {
	for _, tc := range []struct {
		endpoints []string
		want      string
	}{{nil, "/chat/completions"}, {[]string{"/responses"}, "/responses"}, {[]string{"/v1/messages"}, "/v1/messages"}, {[]string{"ws:/responses"}, ""}} {
		model := Model{ID: "example", SupportedEndpoints: tc.endpoints}
		model.Capabilities.Type = "chat"
		model.Capabilities.Supports.ReasoningEffort = []string{"low", "high"}
		model.Capabilities.Supports.Vision = true
		info := model.ModelInfo()
		if tc.want == "" {
			if info != nil {
				t.Fatal("advertised unsupported model")
			}
			continue
		}
		if info == nil || info.UpstreamEndpoint != tc.want || info.Thinking == nil || len(info.SupportedInputModalities) != 2 {
			t.Fatalf("bad model info: %+v", info)
		}
		model.Capabilities.Type = "embeddings"
		if model.ModelInfo() != nil {
			t.Fatal("advertised embeddings as chat")
		}
	}
}
