package antigravity

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"
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

type boundedCountingReader struct {
	remaining int64
	readBytes atomic.Int64
	fill      byte
}

func (r *boundedCountingReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	n := len(p)
	if int64(n) > r.remaining {
		n = int(r.remaining)
	}
	for i := 0; i < n; i++ {
		p[i] = r.fill
	}
	r.remaining -= int64(n)
	r.readBytes.Add(int64(n))
	return n, nil
}

func (r *boundedCountingReader) Close() error {
	return nil
}

func TestOnboardUserPreservesStructuredErrorDetailsPast200Bytes(t *testing.T) {
	t.Run("403 TOS_VIOLATION envelope past 200 bytes", func(t *testing.T) {
		tosBody := `{"error":{"code":403,"message":"` + strings.Repeat("account suspended notice ", 12) + `","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"TOS_VIOLATION","domain":"cloudaicompanion.googleapis.com","metadata":{"appeal_url":"https://forms.gle/testAppeal7759"}}]}}`
		if len(tosBody) < 500 {
			t.Fatalf("test fixture too short: len=%d", len(tosBody))
		}
		auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(tosBody)),
			}, nil
		})})

		_, err := auth.OnboardUser(context.Background(), "access-token", "free-tier")
		var httpErr *HTTPStatusError
		if !errors.As(err, &httpErr) || httpErr == nil {
			t.Fatalf("expected *HTTPStatusError, got %T (%v)", err, err)
		}
		if httpErr.StatusCode() != http.StatusForbidden {
			t.Fatalf("StatusCode() = %d, want 403", httpErr.StatusCode())
		}
		if !strings.Contains(httpErr.Error(), "TOS_VIOLATION") {
			t.Fatalf("Error() lost TOS_VIOLATION detail: %q", httpErr.Error())
		}
		if !strings.Contains(httpErr.Error(), "https://forms.gle/testAppeal7759") {
			t.Fatalf("Error() lost appeal_url detail: %q", httpErr.Error())
		}
		if got := string(httpErr.ResponseBody()); got != tosBody {
			t.Fatalf("ResponseBody() = %q, want %q", got, tosBody)
		}
	})

	t.Run("429 RetryInfo past 200 bytes and Retry-After header", func(t *testing.T) {
		rateLimitBody := `{"error":{"code":429,"message":"` + strings.Repeat("quota exceeded detail ", 14) + `","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"37s"}]}}`
		if len(rateLimitBody) < 400 {
			t.Fatalf("test fixture too short: len=%d", len(rateLimitBody))
		}
		auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			hdr := make(http.Header)
			hdr.Set("Retry-After", "42")
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     hdr,
				Body:       io.NopCloser(strings.NewReader(rateLimitBody)),
			}, nil
		})})

		_, err := auth.OnboardUser(context.Background(), "access-token", "free-tier")
		var httpErr *HTTPStatusError
		if !errors.As(err, &httpErr) || httpErr == nil {
			t.Fatalf("expected *HTTPStatusError, got %T (%v)", err, err)
		}
		if httpErr.StatusCode() != http.StatusTooManyRequests {
			t.Fatalf("StatusCode() = %d, want 429", httpErr.StatusCode())
		}
		if !strings.Contains(httpErr.Error(), `"retryDelay":"37s"`) {
			t.Fatalf("Error() lost retryDelay detail: %q", httpErr.Error())
		}
		if got := httpErr.RetryAfter(); got == nil || *got != 42*time.Second {
			t.Fatalf("RetryAfter() = %v, want 42s", got)
		}
	})
}

func TestAuthErrorMessageTruncationRespectsUTF8AndSafeCap(t *testing.T) {
	// Place a 3-byte UTF-8 rune ("中") straddling both byte 200 and byte 8192 (8<<10).
	shortBody := strings.Repeat("a", 199) + "中" + strings.Repeat("b", 80)
	longBody := strings.Repeat("a", (8<<10)-1) + "中" + strings.Repeat("z", 2048)

	t.Run("OnboardUser mid-UTF-8 around byte 200 is not corrupted", func(t *testing.T) {
		auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(shortBody)),
			}, nil
		})})

		_, err := auth.OnboardUser(context.Background(), "access-token", "free-tier")
		var httpErr *HTTPStatusError
		if !errors.As(err, &httpErr) || httpErr == nil {
			t.Fatalf("expected *HTTPStatusError, got %T (%v)", err, err)
		}
		if !utf8.ValidString(httpErr.Error()) {
			t.Fatalf("Error() contains invalid UTF-8: %q", httpErr.Error())
		}
		if !strings.Contains(httpErr.Error(), "中"+strings.Repeat("b", 80)) {
			t.Fatalf("Error() truncated short UTF-8 body unexpectedly: %q", httpErr.Error())
		}
	})

	t.Run("OnboardUser caps very large error message cleanly on UTF-8 boundary", func(t *testing.T) {
		auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(longBody)),
			}, nil
		})})

		_, err := auth.OnboardUser(context.Background(), "access-token", "free-tier")
		var httpErr *HTTPStatusError
		if !errors.As(err, &httpErr) || httpErr == nil {
			t.Fatalf("expected *HTTPStatusError, got %T (%v)", err, err)
		}
		if !utf8.ValidString(httpErr.Error()) {
			t.Fatalf("Error() contains invalid UTF-8: len=%d", len(httpErr.Error()))
		}
		if len(httpErr.Error()) > (8<<10)+64 {
			t.Fatalf("Error() length = %d, want bounded around 8 KiB", len(httpErr.Error()))
		}
		if len(httpErr.Error()) < (8 << 10) {
			t.Fatalf("Error() length = %d, want close to 8 KiB cap (not 200-byte slice)", len(httpErr.Error()))
		}
	})

	t.Run("FetchProjectID caps very large error message cleanly on UTF-8 boundary", func(t *testing.T) {
		auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(longBody)),
			}, nil
		})})

		_, err := auth.FetchProjectID(context.Background(), "access-token")
		var httpErr *HTTPStatusError
		if !errors.As(err, &httpErr) || httpErr == nil {
			t.Fatalf("expected *HTTPStatusError, got %T (%v)", err, err)
		}
		if !utf8.ValidString(httpErr.Error()) {
			t.Fatalf("Error() contains invalid UTF-8: len=%d", len(httpErr.Error()))
		}
		if len(httpErr.Error()) > (8<<10)+64 {
			t.Fatalf("Error() length = %d, want bounded around 8 KiB", len(httpErr.Error()))
		}
	})
}

func TestOnboardUserPollingCancelsImmediatelyOnContextDone(t *testing.T) {
	t.Run("context canceled during polling sleep", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var attempts atomic.Int32
			auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				if errCtx := req.Context().Err(); errCtx != nil {
					return nil, errCtx
				}
				attempts.Add(1)
				return jsonResponse(`{"done":false}`), nil
			})})

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			errCh := make(chan error, 1)
			go func() {
				_, err := auth.OnboardUser(ctx, "access-token", "free-tier")
				errCh <- err
			}()

			// Wait until OnboardUser has completed attempt 1 and is durably blocked in its retry delay.
			synctest.Wait()
			if got := attempts.Load(); got != 1 {
				t.Fatalf("attempts before cancel = %d, want 1", got)
			}

			cancelAt := time.Now()
			cancel()
			err := <-errCh
			elapsed := time.Since(cancelAt)

			if !errors.Is(err, context.Canceled) {
				t.Fatalf("OnboardUser() err = %v, want context.Canceled", err)
			}
			if elapsed != 0 {
				t.Fatalf("OnboardUser() took %v after cancel, want immediate exit without 2s sleep", elapsed)
			}
			if got := attempts.Load(); got != 1 {
				t.Fatalf("attempts after cancel = %d, want 1", got)
			}
		})
	})

	t.Run("context deadline exceeded during polling sleep", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var attempts atomic.Int32
			auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				if errCtx := req.Context().Err(); errCtx != nil {
					return nil, errCtx
				}
				attempts.Add(1)
				return jsonResponse(`{"done":false}`), nil
			})})

			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()

			start := time.Now()
			_, err := auth.OnboardUser(ctx, "access-token", "free-tier")
			elapsed := time.Since(start)

			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("OnboardUser() err = %v, want context.DeadlineExceeded", err)
			}
			if elapsed != 500*time.Millisecond {
				t.Fatalf("OnboardUser() took %v, want 500ms deadline exit", elapsed)
			}
			if got := attempts.Load(); got != 1 {
				t.Fatalf("attempts = %d, want 1", got)
			}
		})
	})

	t.Run("nil context polls safely without panic", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var attempts atomic.Int32
			auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				if attempts.Add(1) == 1 {
					return jsonResponse(`{"done":false}`), nil
				}
				return jsonResponse(`{"done":true,"response":{"cloudaicompanionProject":"nil-ctx-project"}}`), nil
			})})

			//nolint:staticcheck // intentionally verify nil context defensive handling
			projectID, err := auth.OnboardUser(nil, "access-token", "free-tier")
			if err != nil {
				t.Fatalf("OnboardUser(nil, ...) error: %v", err)
			}
			if projectID != "nil-ctx-project" {
				t.Fatalf("projectID = %q, want %q", projectID, "nil-ctx-project")
			}
			if got := attempts.Load(); got != 2 {
				t.Fatalf("attempts = %d, want 2", got)
			}
		})
	})
}

func TestTruncateUTF8Bytes(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		maxBytes int
		want     string
	}{
		{name: "zero max", input: "abc", maxBytes: 0, want: ""},
		{name: "fits ascii", input: "hello", maxBytes: 10, want: "hello"},
		{name: "cuts 3-byte rune after byte 1", input: "ab中c", maxBytes: 3, want: "ab"},
		{name: "cuts 3-byte rune after byte 2", input: "ab中c", maxBytes: 4, want: "ab"},
		{name: "keeps complete 3-byte rune", input: "ab中c", maxBytes: 5, want: "ab中"},
		{name: "cuts 4-byte emoji after byte 3", input: "a😀b", maxBytes: 4, want: "a"},
		{name: "keeps complete 4-byte emoji", input: "a😀b", maxBytes: 5, want: "a😀"},
		{name: "trims incomplete trailing utf8 from limit reader", input: "abc\xe4\xb8", maxBytes: 16, want: "abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateUTF8Bytes(tc.input, tc.maxBytes)
			if got != tc.want {
				t.Fatalf("truncateUTF8Bytes(%q, %d) = %q, want %q", tc.input, tc.maxBytes, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("truncateUTF8Bytes(%q, %d) produced invalid UTF-8: %q", tc.input, tc.maxBytes, got)
			}
		})
	}
}

func TestFetchProjectIDAndOnboardUserBoundResponseBodyReads(t *testing.T) {
	const oversizedLen = 4 << 20 // 4 MiB
	const maxSuccessRead = maxAntigravityAuthResponseBytes + 1
	const maxErrorRead = maxAntigravityAuthErrorBodyBytes

	t.Run("FetchProjectID bounds 200 response read", func(t *testing.T) {
		reader := &boundedCountingReader{remaining: oversizedLen, fill: 'a'}
		auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       reader,
			}, nil
		})})

		_, err := auth.FetchProjectID(context.Background(), "access-token")
		if err == nil {
			t.Fatal("FetchProjectID() = nil, want error for oversized 200 body")
		}
		if got := reader.readBytes.Load(); got > maxSuccessRead {
			t.Fatalf("FetchProjectID read %d bytes from oversized 200 body, want bounded <= %d", got, maxSuccessRead)
		}
	})

	t.Run("FetchProjectID bounds error response read and preserves HTTPStatusError", func(t *testing.T) {
		reader := &boundedCountingReader{remaining: oversizedLen, fill: 'e'}
		auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			hdr := make(http.Header)
			hdr.Set("Retry-After", "15")
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     hdr,
				Body:       reader,
			}, nil
		})})

		_, err := auth.FetchProjectID(context.Background(), "access-token")
		var httpErr *HTTPStatusError
		if !errors.As(err, &httpErr) || httpErr == nil {
			t.Fatalf("expected *HTTPStatusError, got %T (%v)", err, err)
		}
		if httpErr.StatusCode() != http.StatusTooManyRequests {
			t.Fatalf("StatusCode() = %d, want 429", httpErr.StatusCode())
		}
		if got := httpErr.RetryAfter(); got == nil || *got != 15*time.Second {
			t.Fatalf("RetryAfter() = %v, want 15s", got)
		}
		if got := reader.readBytes.Load(); got > maxErrorRead {
			t.Fatalf("FetchProjectID read %d bytes from oversized error body, want bounded <= %d", got, maxErrorRead)
		}
		if got := len(httpErr.ResponseBody()); got > maxErrorRead {
			t.Fatalf("FetchProjectID ResponseBody() len = %d, want <= %d", got, maxErrorRead)
		}
	})

	t.Run("OnboardUser bounds 200 response read", func(t *testing.T) {
		reader := &boundedCountingReader{remaining: oversizedLen, fill: 'a'}
		auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       reader,
			}, nil
		})})

		_, err := auth.OnboardUser(context.Background(), "access-token", "free-tier")
		if err == nil {
			t.Fatal("OnboardUser() = nil, want error for oversized 200 body")
		}
		if got := reader.readBytes.Load(); got > maxSuccessRead {
			t.Fatalf("OnboardUser read %d bytes from oversized 200 body, want bounded <= %d", got, maxSuccessRead)
		}
	})

	t.Run("OnboardUser bounds error response read and preserves HTTPStatusError", func(t *testing.T) {
		reader := &boundedCountingReader{remaining: oversizedLen, fill: 'e'}
		auth := NewAntigravityAuth(nil, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			hdr := make(http.Header)
			hdr.Set("Retry-After", "25")
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Header:     hdr,
				Body:       reader,
			}, nil
		})})

		_, err := auth.OnboardUser(context.Background(), "access-token", "free-tier")
		var httpErr *HTTPStatusError
		if !errors.As(err, &httpErr) || httpErr == nil {
			t.Fatalf("expected *HTTPStatusError, got %T (%v)", err, err)
		}
		if httpErr.StatusCode() != http.StatusServiceUnavailable {
			t.Fatalf("StatusCode() = %d, want 503", httpErr.StatusCode())
		}
		if got := httpErr.RetryAfter(); got == nil || *got != 25*time.Second {
			t.Fatalf("RetryAfter() = %v, want 25s", got)
		}
		if got := reader.readBytes.Load(); got > maxErrorRead {
			t.Fatalf("OnboardUser read %d bytes from oversized error body, want bounded <= %d", got, maxErrorRead)
		}
		if got := len(httpErr.ResponseBody()); got > maxErrorRead {
			t.Fatalf("OnboardUser ResponseBody() len = %d, want <= %d", got, maxErrorRead)
		}
	})
}
