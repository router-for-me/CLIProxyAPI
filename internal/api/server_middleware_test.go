package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
)

type testAuthProvider struct {
	id       string
	authFunc func(ctx context.Context, r *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError)
}

func (p *testAuthProvider) Identifier() string {
	if p.id != "" {
		return p.id
	}
	return "test-provider"
}

func (p *testAuthProvider) Authenticate(ctx context.Context, r *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	if p.authFunc != nil {
		return p.authFunc(ctx, r)
	}
	return nil, nil
}

func TestAccessAuthMiddleware_Headers(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name                string
		realtimeError       bool
		authErr             *sdkaccess.AuthError
		wantStatus          int
		wantShouldRetry     string
		wantWWWAuth         string
		expectRetryHeader   bool
		expectWWWAuthHeader bool
	}{
		{
			name:                "standard auth missing credentials returns 401 and non-retryable header",
			realtimeError:       false,
			authErr:             sdkaccess.NewNoCredentialsError(),
			wantStatus:          http.StatusUnauthorized,
			wantShouldRetry:     "false",
			wantWWWAuth:         `Bearer realm="cliproxyapi"`,
			expectRetryHeader:   true,
			expectWWWAuthHeader: true,
		},
		{
			name:                "standard auth invalid credentials returns 401 and non-retryable header",
			realtimeError:       false,
			authErr:             sdkaccess.NewInvalidCredentialError(),
			wantStatus:          http.StatusUnauthorized,
			wantShouldRetry:     "false",
			wantWWWAuth:         `Bearer realm="cliproxyapi"`,
			expectRetryHeader:   true,
			expectWWWAuthHeader: true,
		},
		{
			name:                "standard auth request too large returns 413 without non-retryable header",
			realtimeError:       false,
			authErr:             sdkaccess.NewRequestEntityTooLargeError(errors.New("body too large")),
			wantStatus:          http.StatusRequestEntityTooLarge,
			expectRetryHeader:   false,
			expectWWWAuthHeader: false,
		},
		{
			name:                "standard auth internal error returns 500 without non-retryable header",
			realtimeError:       false,
			authErr:             sdkaccess.NewInternalAuthError("internal error", errors.New("backend failed")),
			wantStatus:          http.StatusInternalServerError,
			expectRetryHeader:   false,
			expectWWWAuthHeader: false,
		},
		{
			name:                "realtime auth missing credentials returns 401 and non-retryable header",
			realtimeError:       true,
			authErr:             sdkaccess.NewNoCredentialsError(),
			wantStatus:          http.StatusUnauthorized,
			wantShouldRetry:     "false",
			wantWWWAuth:         `Bearer realm="cliproxyapi"`,
			expectRetryHeader:   true,
			expectWWWAuthHeader: true,
		},
		{
			name:                "realtime auth invalid credentials returns 401 and non-retryable header",
			realtimeError:       true,
			authErr:             sdkaccess.NewInvalidCredentialError(),
			wantStatus:          http.StatusUnauthorized,
			wantShouldRetry:     "false",
			wantWWWAuth:         `Bearer realm="cliproxyapi"`,
			expectRetryHeader:   true,
			expectWWWAuthHeader: true,
		},
		{
			name:                "realtime auth request too large returns 413 without non-retryable header",
			realtimeError:       true,
			authErr:             sdkaccess.NewRequestEntityTooLargeError(errors.New("body too large")),
			wantStatus:          http.StatusRequestEntityTooLarge,
			expectRetryHeader:   false,
			expectWWWAuthHeader: false,
		},
		{
			name:                "realtime auth internal error returns 500 without non-retryable header",
			realtimeError:       true,
			authErr:             sdkaccess.NewInternalAuthError("internal error", errors.New("backend failed")),
			wantStatus:          http.StatusInternalServerError,
			expectRetryHeader:   false,
			expectWWWAuthHeader: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			manager := sdkaccess.NewManager()
			manager.SetProviders([]sdkaccess.Provider{
				&testAuthProvider{
					authFunc: func(ctx context.Context, r *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
						return nil, tc.authErr
					},
				},
			})

			router := gin.New()
			middleware := accessAuthMiddleware(manager, tc.realtimeError)
			router.Use(middleware)
			router.GET("/test", func(c *gin.Context) {
				c.Status(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}

			gotRetry, hasRetry := rec.Header()["X-Should-Retry"]
			if tc.expectRetryHeader {
				if !hasRetry || len(gotRetry) == 0 || gotRetry[0] != tc.wantShouldRetry {
					t.Fatalf("X-Should-Retry = %v, want %q", gotRetry, tc.wantShouldRetry)
				}
			} else {
				if hasRetry {
					t.Fatalf("unexpected X-Should-Retry header present: %v", gotRetry)
				}
			}

			gotWWWAuth, hasWWWAuth := rec.Header()["Www-Authenticate"]
			if !hasWWWAuth {
				gotWWWAuth, hasWWWAuth = rec.Header()["WWW-Authenticate"]
			}
			if tc.expectWWWAuthHeader {
				if !hasWWWAuth || len(gotWWWAuth) == 0 || gotWWWAuth[0] != tc.wantWWWAuth {
					t.Fatalf("WWW-Authenticate = %v, want %q", gotWWWAuth, tc.wantWWWAuth)
				}
			} else {
				if hasWWWAuth {
					t.Fatalf("unexpected WWW-Authenticate header present: %v", gotWWWAuth)
				}
			}
		})
	}
}
