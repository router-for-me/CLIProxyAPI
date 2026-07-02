package helps

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestCookieJarForAuth(t *testing.T) {
	t.Run("same_account_reuses_jar", func(t *testing.T) {
		a := &cliproxyauth.Auth{ID: "cookiejar-acct-A"}
		j1 := cookieJarForAuth(nil, a)
		j2 := cookieJarForAuth(nil, a)
		if j1 == nil {
			t.Fatal("expected a jar for an account with an ID")
		}
		if j1 != j2 {
			t.Fatal("same account ID must reuse the same jar instance (cookie persistence)")
		}
	})

	t.Run("different_accounts_isolated", func(t *testing.T) {
		jA := cookieJarForAuth(nil, &cliproxyauth.Auth{ID: "cookiejar-acct-B"})
		jC := cookieJarForAuth(nil, &cliproxyauth.Auth{ID: "cookiejar-acct-C"})
		if jA == nil || jC == nil {
			t.Fatal("expected jars for both accounts")
		}
		if jA == jC {
			t.Fatal("different accounts must get isolated jars")
		}
	})

	t.Run("nil_auth_returns_nil", func(t *testing.T) {
		if cookieJarForAuth(nil, nil) != nil {
			t.Fatal("nil auth must yield no jar")
		}
	})

	t.Run("empty_id_returns_nil", func(t *testing.T) {
		if cookieJarForAuth(nil, &cliproxyauth.Auth{ID: "   "}) != nil {
			t.Fatal("blank account ID must yield no jar")
		}
	})

	t.Run("disabled_returns_nil", func(t *testing.T) {
		cfg := &config.Config{DisableUpstreamCookieJar: true}
		if cookieJarForAuth(cfg, &cliproxyauth.Auth{ID: "cookiejar-acct-D"}) != nil {
			t.Fatal("DisableUpstreamCookieJar must yield no jar")
		}
	})
}
