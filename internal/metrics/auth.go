package metrics

import (
	"crypto/subtle"
	"net/http"
)

func BasicAuthMiddleware(user, pass string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if user == "" || pass == "" {
				next.ServeHTTP(w, r)
				return
			}

			u, p, ok := r.BasicAuth()
			if !ok || subtle.ConstantTimeCompare([]byte(u), []byte(user)) != 1 ||
				subtle.ConstantTimeCompare([]byte(p), []byte(pass)) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="metrics"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}