package homeaccess

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

type fakeAccessClient struct {
	heartbeat bool
	raw       []byte
	err       error
	calls     int
	headers   http.Header
	query     url.Values
}

func (c *fakeAccessClient) HeartbeatOK() bool {
	return c != nil && c.heartbeat
}

func (c *fakeAccessClient) ValidateAccess(_ context.Context, headers http.Header, query url.Values) ([]byte, error) {
	c.calls++
	c.headers = headers.Clone()
	c.query = cloneValues(query)
	return c.raw, c.err
}

func TestProviderAuthenticate(t *testing.T) {
	cases := []struct {
		name          string
		target        string
		header        map[string]string
		client        *fakeAccessClient
		wantCode      sdkaccess.AuthErrorCode
		wantStatus    int
		wantPrincipal string
		wantSource    string
		wantCalls     int
	}{
		{
			name:          "valid query key",
			target:        "/v1/models?key=valid-client-key",
			client:        &fakeAccessClient{heartbeat: true, raw: []byte(`{"authenticated":true,"provider":"cluster-api-key","principal":"valid-client-key","metadata":{"source":"query-key"}}`)},
			wantPrincipal: "valid-client-key",
			wantSource:    "query-key",
			wantCalls:     1,
		},
		{
			name:       "home invalid",
			target:     "/v1/models",
			header:     map[string]string{"Authorization": "Bearer invalid-client-key"},
			client:     &fakeAccessClient{heartbeat: true, raw: []byte(`{"authenticated":false,"error":{"type":"invalid_credential","message":"Invalid API key"}}`)},
			wantCode:   sdkaccess.AuthErrorCodeInvalidCredential,
			wantStatus: http.StatusUnauthorized,
			wantCalls:  1,
		},
		{
			name:       "home missing",
			target:     "/v1/models",
			header:     map[string]string{"Authorization": "Bearer client-key"},
			client:     &fakeAccessClient{heartbeat: true, raw: []byte(`{"authenticated":false,"error":{"type":"no_credentials","message":"Missing API key"}}`)},
			wantCode:   sdkaccess.AuthErrorCodeNoCredentials,
			wantStatus: http.StatusUnauthorized,
			wantCalls:  1,
		},
		{
			name:       "home unavailable",
			target:     "/v1/models",
			header:     map[string]string{"X-Api-Key": "client-key"},
			client:     &fakeAccessClient{heartbeat: false},
			wantCode:   sdkaccess.AuthErrorCodeInternal,
			wantStatus: http.StatusServiceUnavailable,
			wantCalls:  0,
		},
		{
			name:       "home unsupported",
			target:     "/v1/models",
			header:     map[string]string{"X-Api-Key": "client-key"},
			client:     &fakeAccessClient{heartbeat: true, err: errors.New("ERR unsupported type")},
			wantCode:   sdkaccess.AuthErrorCodeInternal,
			wantStatus: http.StatusServiceUnavailable,
			wantCalls:  1,
		},
		{
			name:       "home missing without credentials",
			target:     "/v1/models",
			client:     &fakeAccessClient{heartbeat: true, raw: []byte(`{"authenticated":false,"error":{"type":"no_credentials","message":"Missing API key"}}`)},
			wantCode:   sdkaccess.AuthErrorCodeNoCredentials,
			wantStatus: http.StatusUnauthorized,
			wantCalls:  1,
		},
		{
			name:       "home authenticated without principal",
			target:     "/v1/models",
			client:     &fakeAccessClient{heartbeat: true, raw: []byte(`{"authenticated":true,"metadata":{"source":"anonymous"}}`)},
			wantCode:   sdkaccess.AuthErrorCodeInternal,
			wantStatus: http.StatusServiceUnavailable,
			wantCalls:  1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.target, nil)
			for key, value := range tc.header {
				req.Header.Set(key, value)
			}
			p := NewProvider(func() accessClient { return tc.client })

			result, authErr := p.Authenticate(context.Background(), req)
			if tc.wantCode != "" {
				if !sdkaccess.IsAuthErrorCode(authErr, tc.wantCode) {
					t.Fatalf("Authenticate() error = %v, want code %s", authErr, tc.wantCode)
				}
				if authErr.HTTPStatusCode() != tc.wantStatus {
					t.Fatalf("HTTPStatusCode() = %d, want %d", authErr.HTTPStatusCode(), tc.wantStatus)
				}
			} else if authErr != nil {
				t.Fatalf("Authenticate() error = %v", authErr)
			}
			if tc.wantPrincipal != "" || tc.wantSource != "" {
				if result == nil {
					t.Fatal("result = nil")
				}
				if result.Principal != tc.wantPrincipal {
					t.Fatalf("principal = %q, want %q", result.Principal, tc.wantPrincipal)
				}
				if result.Metadata["source"] != tc.wantSource {
					t.Fatalf("metadata.source = %q, want %q", result.Metadata["source"], tc.wantSource)
				}
			}
			if tc.client.calls != tc.wantCalls {
				t.Fatalf("ValidateAccess calls = %d, want %d", tc.client.calls, tc.wantCalls)
			}
		})
	}
}

func TestProviderAuthenticateUsesFirstCredentialHeaderValue(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Add("Authorization", "Bearer client-key")
	req.Header.Add("Authorization", "Bearer keep")
	client := &fakeAccessClient{
		heartbeat: true,
		raw:       []byte(`{"authenticated":true,"provider":"cluster-api-key","principal":"client-key"}`),
	}
	p := NewProvider(func() accessClient { return client })

	if _, authErr := p.Authenticate(context.Background(), req); authErr != nil {
		t.Fatalf("Authenticate() error = %v", authErr)
	}

	values := client.headers.Values("Authorization")
	if len(values) != 1 {
		t.Fatalf("forwarded Authorization values = %v, want one value", values)
	}
	if values[0] != "Bearer client-key" {
		t.Fatalf("forwarded Authorization = %q, want first header value", values[0])
	}
}

func cloneValues(values url.Values) url.Values {
	out := url.Values{}
	for key, list := range values {
		out[key] = append([]string(nil), list...)
	}
	return out
}
