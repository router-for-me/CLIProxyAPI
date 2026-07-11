package helps

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type utlsClientRoundTripFunc func(*http.Request) (*http.Response, error)

func (f utlsClientRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestFallbackRoundTripperRoutesHosts(t *testing.T) {
	testCases := []struct {
		name          string
		host          string
		wantProtected bool
	}{
		{
			name: "chatgpt uses fallback transport",
			host: "chatgpt.com",
		},
		{
			name:          "anthropic uses utls transport",
			host:          "api.anthropic.com",
			wantProtected: true,
		},
		{
			name:          "anthropic mixed case uses utls transport",
			host:          "API.anthropic.com",
			wantProtected: true,
		},
		{
			name: "ordinary host uses fallback transport",
			host: "example.com",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			utlsCalls := 0
			fallbackCalls := 0
			roundTripper := &fallbackRoundTripper{
				utls: utlsClientRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					utlsCalls++
					return newTestResponse(req), nil
				}),
				fallback: utlsClientRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					fallbackCalls++
					return newTestResponse(req), nil
				}),
			}

			_, protected := utlsProtectedHosts[strings.ToLower(testCase.host)]
			if protected != testCase.wantProtected {
				t.Fatalf("utls protected status for %q = %t, want %t", testCase.host, protected, testCase.wantProtected)
			}

			req, err := http.NewRequest(http.MethodGet, "https://"+testCase.host+"/", nil)
			if err != nil {
				t.Fatalf("http.NewRequest returned error: %v", err)
			}
			resp, err := roundTripper.RoundTrip(req)
			if err != nil {
				t.Fatalf("RoundTrip returned error: %v", err)
			}
			if errClose := resp.Body.Close(); errClose != nil {
				t.Fatalf("response body close returned error: %v", errClose)
			}

			if testCase.wantProtected {
				if utlsCalls != 1 || fallbackCalls != 0 {
					t.Fatalf("transport calls: utls = %d, fallback = %d; want utls = 1, fallback = 0", utlsCalls, fallbackCalls)
				}
				return
			}
			if utlsCalls != 0 || fallbackCalls != 1 {
				t.Fatalf("transport calls: utls = %d, fallback = %d; want utls = 0, fallback = 1", utlsCalls, fallbackCalls)
			}
		})
	}
}

func newTestResponse(req *http.Request) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("{}")),
		Request:    req,
	}
}
