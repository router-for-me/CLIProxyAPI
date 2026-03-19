package configaccess

import (
    "net/http"
    "net/http/httptest"
    "testing"

    sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
)

func TestProviderAuthenticateHeadersOnly(t *testing.T) {
    provider := newProvider("", []string{"good-key"})

    testCases := []struct {
        name           string
        configureReq   func(r *http.Request)
        expectHandled  bool
        expectSource   string
        expectErrCode  sdkaccess.AuthErrorCode
    }{
        {
            name: "no headers",
            configureReq: func(r *http.Request) {},
            expectHandled: false,
            expectErrCode: sdkaccess.AuthErrorCodeNoCredentials,
        },
        {
            name: "query only should be ignored",
            configureReq: func(r *http.Request) {
                r.URL.RawQuery = "key=good-key&auth_token=good-key"
            },
            expectHandled: false,
            expectErrCode: sdkaccess.AuthErrorCodeNoCredentials,
        },
        {
            name: "bearer authorization matches",
            configureReq: func(r *http.Request) {
                r.Header.Set("Authorization", "Bearer good-key")
            },
            expectHandled: true,
            expectSource: "authorization",
        },
        {
            name: "x-goog-api-key matches",
            configureReq: func(r *http.Request) {
                r.Header.Set("X-Goog-Api-Key", "good-key")
            },
            expectHandled: true,
            expectSource: "x-goog-api-key",
        },
        {
            name: "x-api-key matches",
            configureReq: func(r *http.Request) {
                r.Header.Set("X-Api-Key", "good-key")
            },
            expectHandled: true,
            expectSource: "x-api-key",
        },
        {
            name: "invalid header value",
            configureReq: func(r *http.Request) {
                r.Header.Set("Authorization", "Bearer bad-key")
            },
            expectHandled: false,
            expectErrCode: sdkaccess.AuthErrorCodeInvalidCredential,
        },
    }

    for _, tc := range testCases {
        tc := tc
        t.Run(tc.name, func(t *testing.T) {
            t.Parallel()
            req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
            tc.configureReq(req)

            res, err := provider.Authenticate(req.Context(), req)

            if tc.expectHandled {
                if err != nil {
                    t.Fatalf("expected success, got error: %v", err)
                }
                if res == nil {
                    t.Fatalf("expected result, got nil")
                }
                if res.Principal != "good-key" {
                    t.Fatalf("unexpected principal: %s", res.Principal)
                }
                if res.Metadata["source"] != tc.expectSource {
                    t.Fatalf("unexpected source: %s", res.Metadata["source"])
                }
                return
            }

            if err == nil {
                t.Fatalf("expected error, got nil result: %+v", res)
            }
            if !sdkaccess.IsAuthErrorCode(err, tc.expectErrCode) {
                t.Fatalf("unexpected error code: %v", err.Code)
            }
        })
    }
}
