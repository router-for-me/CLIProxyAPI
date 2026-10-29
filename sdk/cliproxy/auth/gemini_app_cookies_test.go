package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/geminiapp"
)

func geminiAppCookieAuth() *Auth {
	return &Auth{ID: "gemini-app-cookies", Provider: geminiapp.Provider, Status: StatusActive,
		Metadata: map[string]any{"cookies": map[string]any{geminiapp.CookiePSID: "old-psid", geminiapp.CookiePSIDTS: "old-ts"}}}
}

func TestGeminiAppCookieReplacementRecoversUnauthorizedAuth(t *testing.T) {
	for _, key := range []string{geminiapp.CookiePSID, geminiapp.CookiePSIDTS} {
		t.Run(key, func(t *testing.T) {
			ctx := context.Background()
			manager := NewManager(nil, nil, nil)
			initial, err := manager.Register(ctx, geminiAppCookieAuth())
			if err != nil {
				t.Fatal(err)
			}
			failure := Result{AuthID: initial.ID, Provider: initial.Provider, Model: "gemini-app", CredentialVersion: initial.CredentialVersion,
				Error: &Error{HTTPStatus: http.StatusUnauthorized, Message: "expired session"}}
			manager.MarkResult(ctx, failure)
			expired, _ := manager.GetByID(initial.ID)
			if state := expired.ModelStates["gemini-app"]; state == nil || !state.Unavailable {
				t.Fatal("fixture did not enter unauthorized state")
			}
			replacement := expired.Clone()
			replacement.Metadata["cookies"].(map[string]any)[key] = "reimported"
			fresh, err := manager.Update(ctx, replacement)
			if err != nil {
				t.Fatal(err)
			}
			if fresh.CredentialVersion <= initial.CredentialVersion {
				t.Fatal("cookie replacement did not advance credential version")
			}
			manager.MarkResult(ctx, failure) // An old in-flight 401 must stay stale.
			fresh, _ = manager.GetByID(initial.ID)
			if fresh.Unavailable || fresh.LastError != nil {
				t.Fatal("replacement kept an expired session's auth state")
			}
			if state := fresh.ModelStates["gemini-app"]; state != nil && (state.Unavailable || state.LastError != nil || !state.NextRetryAfter.IsZero()) {
				t.Fatal("replacement was cooled by the expired session")
			}
		})
	}
}

func TestGeminiAppPreparedCookiesMergeIndependentRotations(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, removeTS := range []bool{false, true} {
			t.Run(fmt.Sprintf("reverse=%t/delete=%t", reverse, removeTS), func(t *testing.T) {
				ctx := context.Background()
				manager := NewManager(nil, nil, nil)
				base, err := manager.Register(ctx, geminiAppCookieAuth())
				if err != nil {
					t.Fatal(err)
				}
				first, second := base.Clone(), base.Clone()
				first.Metadata["cookies"].(map[string]any)[geminiapp.CookiePSID] = "new-psid"
				if removeTS {
					delete(second.Metadata["cookies"].(map[string]any), geminiapp.CookiePSIDTS)
				} else {
					second.Metadata["cookies"].(map[string]any)[geminiapp.CookiePSIDTS] = "new-ts"
				}
				if reverse {
					first, second = second, first
				}
				for _, updated := range []*Auth{first, second} {
					if _, err := manager.UpdatePreparedAuth(ctx, base, updated); err != nil {
						t.Fatal(err)
					}
				}
				current, _ := manager.GetByID(base.ID)
				want := map[string]any{geminiapp.CookiePSID: "new-psid", geminiapp.CookiePSIDTS: "new-ts"}
				if removeTS {
					delete(want, geminiapp.CookiePSIDTS)
				}
				if !reflect.DeepEqual(current.Metadata["cookies"], want) {
					t.Fatal("independent rotation was lost")
				}
				if !reflect.DeepEqual(base.Metadata["cookies"], geminiAppCookieAuth().Metadata["cookies"]) {
					t.Fatal("merge mutated the original execution snapshot")
				}
			})
		}
	}
}

func TestGeminiAppPreparedCookiesPreserveExplicitReplacement(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	base, err := manager.Register(ctx, geminiAppCookieAuth())
	if err != nil {
		t.Fatal(err)
	}
	// Re-import can change just one cookie. An old request must not mix a
	// rotation from the previous session into the replacement's unchanged slot.
	replacement := base.Clone()
	replacement.Metadata["cookies"].(map[string]any)[geminiapp.CookiePSID] = "reimported"
	replacement.Metadata["notes"] = "keep this edit"
	if _, err := manager.Update(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	stale := base.Clone()
	stale.Metadata["cookies"].(map[string]any)[geminiapp.CookiePSIDTS] = "stale-rotation"
	if _, err := manager.UpdatePreparedAuth(ctx, base, stale); err != nil {
		t.Fatal(err)
	}
	current, _ := manager.GetByID(base.ID)
	if !reflect.DeepEqual(current.Metadata["cookies"], replacement.Metadata["cookies"]) || current.Metadata["notes"] != "keep this edit" {
		t.Fatal("old request changed the explicit replacement")
	}
}

func TestGeminiAppTransportErrorKeepsRetryClassification(t *testing.T) {
	for _, test := range []struct {
		name                 string
		err                  error
		lifecycle, transient bool
	}{
		{"EOF", io.ErrUnexpectedEOF, true, true},
		{"HTTP2", errors.New("http2: client conn is closed"), true, true},
		{"canceled", context.Canceled, true, false},
		{"deadline", context.DeadlineExceeded, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			wrapped := geminiapp.NewTransportError("generation request", &url.Error{Op: "Post", URL: "https://gemini.google.com/?f.sid=secret", Err: test.err})
			if isConnectionLifecycleError(wrapped) != test.lifecycle || isTransientTransportError(wrapped) != test.transient {
				t.Fatal("sanitization changed retry/cooldown classification")
			}
		})
	}
}
