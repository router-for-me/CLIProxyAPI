package antigravity

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestBuildAuthURLIncludesAICodeScope(t *testing.T) {
	parsedURL, err := url.Parse(NewAntigravityAuth(nil, nil).BuildAuthURL("state", "http://localhost/callback"))
	if err != nil {
		t.Fatalf("parse auth URL: %v", err)
	}

	for _, scope := range strings.Fields(parsedURL.Query().Get("scope")) {
		if scope == "https://www.googleapis.com/auth/aicode" {
			return
		}
	}
	t.Fatalf("authorization URL scope does not include %q", "https://www.googleapis.com/auth/aicode")
}

func TestFetchProjectIDFromLoadCodeAssist(t *testing.T) {
	auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist" {
			t.Fatalf("unexpected request URL: %s", req.URL.String())
		}
		assertLoadCodeAssistHeaders(t, req)
		assertJSONContains(t, req, `"ideType":"ANTIGRAVITY"`)
		return jsonResponse(`{"cloudaicompanionProject":"cogent-snow-4mnnp"}`), nil
	})})

	projectID, err := auth.FetchProjectID(context.Background(), "access-token")
	if err != nil {
		t.Fatalf("FetchProjectID error: %v", err)
	}
	if projectID != "cogent-snow-4mnnp" {
		t.Fatalf("projectID = %q", projectID)
	}
}

func TestFetchProjectIDFallsBackToDailyOnboardUser(t *testing.T) {
	var sawOnboard bool
	auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case "https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist":
			assertLoadCodeAssistHeaders(t, req)
			return jsonResponse(`{"allowedTiers":[{"id":"free-tier","isDefault":true}]}`), nil
		case "https://daily-cloudcode-pa.googleapis.com/v1internal:onboardUser":
			sawOnboard = true
			assertOnboardUserHeaders(t, req)
			assertJSONContains(t, req, `"tier_id":"free-tier"`)
			assertJSONContains(t, req, `"ide_type":"ANTIGRAVITY"`)
			return jsonResponse(`{
				"done": true,
				"response": {
					"cloudaicompanionProject": {
						"id": "cogent-snow-4mnnp",
						"name": "cogent-snow-4mnnp",
						"projectNumber": "22597072101"
					}
				}
			}`), nil
		default:
			t.Fatalf("unexpected request URL: %s", req.URL.String())
			return nil, nil
		}
	})})

	projectID, err := auth.FetchProjectID(context.Background(), "access-token")
	if err != nil {
		t.Fatalf("FetchProjectID error: %v", err)
	}
	if !sawOnboard {
		t.Fatalf("expected onboardUser fallback")
	}
	if projectID != "cogent-snow-4mnnp" {
		t.Fatalf("projectID = %q", projectID)
	}
}

func TestFetchProjectIDUpstreamForbiddenReturnsStatus403(t *testing.T) {
	auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":{"code":403,"message":"The caller does not have permission"}}`)),
		}, nil
	})})

	_, err := auth.FetchProjectID(context.Background(), "access-token")
	if err == nil {
		t.Fatalf("expected error from 403 response")
	}
	type statusCoder interface {
		StatusCode() int
	}
	sc, ok := err.(statusCoder)
	if !ok || sc.StatusCode() != http.StatusForbidden {
		t.Fatalf("expected status code %d, got %T (%v)", http.StatusForbidden, err, err)
	}
}

func assertLoadCodeAssistHeaders(t *testing.T, req *http.Request) {
	t.Helper()
	if got := req.Header.Get("Authorization"); got != "Bearer access-token" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := req.Header.Get("Accept"); got != "" {
		t.Fatalf("Accept = %q, want omitted", got)
	}
	if got := req.Header.Get("X-Goog-Api-Client"); got != "" {
		t.Fatalf("X-Goog-Api-Client = %q, want empty", got)
	}
	userAgent := req.Header.Get("User-Agent")
	if !strings.HasPrefix(userAgent, "antigravity/hub/") {
		t.Fatalf("User-Agent = %q", userAgent)
	}
	if strings.Contains(userAgent, "google-api-nodejs-client/") {
		t.Fatalf("User-Agent = %q", userAgent)
	}
}

func assertOnboardUserHeaders(t *testing.T, req *http.Request) {
	t.Helper()
	if got := req.Header.Get("Authorization"); got != "Bearer access-token" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := req.Header.Get("Accept"); got != "*/*" {
		t.Fatalf("Accept = %q", got)
	}
	if got := req.Header.Get("X-Goog-Api-Client"); got != "gl-node/22.21.1" {
		t.Fatalf("X-Goog-Api-Client = %q", got)
	}
	userAgent := req.Header.Get("User-Agent")
	if !strings.HasPrefix(userAgent, "antigravity/hub/") {
		t.Fatalf("User-Agent = %q", userAgent)
	}
	if !strings.Contains(userAgent, "google-api-nodejs-client/10.3.0") {
		t.Fatalf("User-Agent = %q", userAgent)
	}
}

func assertJSONContains(t *testing.T, req *http.Request, want string) {
	t.Helper()
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	bodyText := string(body)
	req.Body = io.NopCloser(strings.NewReader(bodyText))
	if !strings.Contains(bodyText, want) {
		t.Fatalf("body missing %s: %s", want, bodyText)
	}
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestFetchProjectIDTooManyRequestsPreservesRetryAfter(t *testing.T) {
	auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		header := make(http.Header)
		header.Set("Retry-After", "45")
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(`{"error":{"code":429,"message":"rate limited"}}`)),
		}, nil
	})})

	_, err := auth.FetchProjectID(context.Background(), "access-token")
	if err == nil {
		t.Fatalf("expected error from 429 response")
	}
	type retryAfterProvider interface {
		RetryAfter() *time.Duration
	}
	rap, ok := err.(retryAfterProvider)
	if !ok {
		t.Fatalf("expected error to expose RetryAfter, got %T (%v)", err, err)
	}
	if got := rap.RetryAfter(); got == nil || *got != 45*time.Second {
		t.Fatalf("RetryAfter() = %v, want 45s", got)
	}
}

func TestFetchProjectIDEmptyResponseWrapsErrProjectIDNotFound(t *testing.T) {
	auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case "https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist":
			return jsonResponse(`{"cloudaicompanionProject":{}}`), nil
		case "https://daily-cloudcode-pa.googleapis.com/v1internal:onboardUser":
			return jsonResponse(`{"done":true,"response":{"cloudaicompanionProject":{}}}`), nil
		default:
			t.Fatalf("unexpected request URL: %s", req.URL.String())
			return nil, nil
		}
	})})

	_, err := auth.FetchProjectID(context.Background(), "access-token")
	if !errors.Is(err, ErrProjectIDNotFound) {
		t.Fatalf("FetchProjectID() err = %v, want errors.Is(_, ErrProjectIDNotFound)", err)
	}
}

func TestOnboardUserPreservesFullResponseBodyOnHTTPStatusError(t *testing.T) {
	longBody := `{"error":{"code":403,"message":"` + strings.Repeat("x", 260) + `","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"TOS_VIOLATION"}]}}`
	auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case "https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist":
			return jsonResponse(`{"allowedTiers":[{"id":"free-tier","isDefault":true}]}`), nil
		case "https://daily-cloudcode-pa.googleapis.com/v1internal:onboardUser":
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(longBody)),
			}, nil
		default:
			t.Fatalf("unexpected request URL: %s", req.URL.String())
			return nil, nil
		}
	})})

	_, err := auth.FetchProjectID(context.Background(), "access-token")
	var httpErr *HTTPStatusError
	if !errors.As(err, &httpErr) || httpErr == nil {
		t.Fatalf("expected *HTTPStatusError, got %T (%v)", err, err)
	}
	if got := string(httpErr.ResponseBody()); got != longBody {
		t.Fatalf("ResponseBody() len=%d, want full untruncated body len=%d", len(got), len(longBody))
	}
}

func TestOnboardUserIncompletePollingDoesNotWrapErrProjectIDNotFound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		onboardCalls := 0
		auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.String() {
			case "https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist":
				return jsonResponse(`{"allowedTiers":[{"id":"free-tier","isDefault":true}]}`), nil
			case "https://daily-cloudcode-pa.googleapis.com/v1internal:onboardUser":
				onboardCalls++
				return jsonResponse(`{"done":false}`), nil
			default:
				t.Fatalf("unexpected request URL: %s", req.URL.String())
				return nil, nil
			}
		})})

		_, err := auth.FetchProjectID(context.Background(), "access-token")
		if err == nil {
			t.Fatal("FetchProjectID() = nil, want incomplete onboarding error")
		}
		if onboardCalls != 5 {
			t.Fatalf("onboardCalls = %d, want 5", onboardCalls)
		}
		if errors.Is(err, ErrProjectIDNotFound) {
			t.Fatalf("FetchProjectID() err = %v, must NOT wrap ErrProjectIDNotFound when onboarding did not complete", err)
		}
		if !strings.Contains(err.Error(), "did not complete") {
			t.Fatalf("FetchProjectID() err = %v, want incomplete onboarding message", err)
		}
	})
}

func TestOnboardUserCompletedOperationErrorDoesNotWrapErrProjectIDNotFound(t *testing.T) {
	auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case "https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist":
			return jsonResponse(`{"allowedTiers":[{"id":"free-tier","isDefault":true}]}`), nil
		case "https://daily-cloudcode-pa.googleapis.com/v1internal:onboardUser":
			return jsonResponse(`{"done":true,"error":{"code":14,"message":"UNAVAILABLE"}}`), nil
		default:
			t.Fatalf("unexpected request URL: %s", req.URL.String())
			return nil, nil
		}
	})})

	_, err := auth.FetchProjectID(context.Background(), "access-token")
	if err == nil {
		t.Fatal("FetchProjectID() = nil, want onboardUser operation error")
	}
	if errors.Is(err, ErrProjectIDNotFound) {
		t.Fatalf("FetchProjectID() err = %v, must NOT wrap ErrProjectIDNotFound when done=true carries an operation error", err)
	}
	if !strings.Contains(err.Error(), "14") || !strings.Contains(err.Error(), "UNAVAILABLE") {
		t.Fatalf("FetchProjectID() err = %v, want preserved operation error code and message", err)
	}
}

func TestOnboardUserCompletedOperationPermissionDeniedPreservesHTTP403AndBody(t *testing.T) {
	const lroTOSBody = `{"done":true,"error":{"code":7,"message":"Violation of Terms of Service","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"TOS_VIOLATION","domain":"cloudaicompanion.googleapis.com","metadata":{"appeal_url":"https://forms.gle/lroAppeal","appeal_url_link_text":"Submit Appeal"}}]}}`
	auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case "https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist":
			return jsonResponse(`{"allowedTiers":[{"id":"free-tier","isDefault":true}]}`), nil
		case "https://daily-cloudcode-pa.googleapis.com/v1internal:onboardUser":
			return jsonResponse(lroTOSBody), nil
		default:
			t.Fatalf("unexpected request URL: %s", req.URL.String())
			return nil, nil
		}
	})})

	_, err := auth.FetchProjectID(context.Background(), "access-token")
	if err == nil {
		t.Fatal("FetchProjectID() = nil, want HTTPStatusError 403 for LRO PERMISSION_DENIED")
	}
	var httpErr *HTTPStatusError
	if !errors.As(err, &httpErr) || httpErr == nil {
		t.Fatalf("expected *HTTPStatusError, got %T (%v)", err, err)
	}
	if httpErr.StatusCode() != http.StatusForbidden {
		t.Fatalf("StatusCode() = %d, want 403", httpErr.StatusCode())
	}
	if got := string(httpErr.ResponseBody()); got != lroTOSBody {
		t.Fatalf("ResponseBody() = %q, want full LRO body %q", got, lroTOSBody)
	}
}
