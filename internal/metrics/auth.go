package metrics

import (
	"crypto/subtle"
	"net/http"
)

// BasicAuthMiddleware creates a middleware that protects HTTP handlers with HTTP Basic Authentication.
// It accepts a username and password; if either is empty, authentication is effectively disabled
// and all requests are allowed through without challenge.
//
// The middleware uses constant-time comparison (via crypto/subtle) to prevent timing attacks
// when validating credentials.
//
// Typical usage:
//   mux.Handle("/_qs/metrics", BasicAuthMiddleware("admin", "secret")(metricsHandler))
//
// This is useful for securing the internal metrics dashboard and API endpoints in production.
func BasicAuthMiddleware(user, pass string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// If no credentials are configured, skip authentication entirely
			// This allows easy local development without auth.
			if user == "" || pass == "" {
				next.ServeHTTP(w, r)
				return
			}

			// Extract username and password from the Authorization header (Basic Auth)
			u, p, ok := r.BasicAuth()
			if !ok {
				// No Authorization header or malformed → challenge the client
				w.Header().Set("WWW-Authenticate", `Basic realm="metrics"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			// Compare provided credentials with expected ones in constant time
			// to avoid leaking information through timing differences.
			userMatch := subtle.ConstantTimeCompare([]byte(u), []byte(user)) == 1
			passMatch := subtle.ConstantTimeCompare([]byte(p), []byte(pass)) == 1

			if !userMatch || !passMatch {
				// Credentials do not match → reject with 401
				w.Header().Set("WWW-Authenticate", `Basic realm="metrics"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			// Authentication successful → proceed to the actual handler
			next.ServeHTTP(w, r)
		})
	}
}