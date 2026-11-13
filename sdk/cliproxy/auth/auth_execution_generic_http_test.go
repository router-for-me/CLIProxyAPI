package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestAuthExecutionPolicyRefusesGenericHTTPHelpers(t *testing.T) {
	for _, operation := range []string{"prepare", "new", "http"} {
		for _, policy := range []string{"nil", "allow", "refuse"} {
			t.Run(operation+"/"+policy, func(t *testing.T) {
				f := newAuthUseFixture(t)
				exec := &authUseGenericHTTPExecutor{authUseExecutor: f.exec}
				f.manager.RegisterExecutor(exec)
				checks := 0
				if policy != "nil" {
					f.manager.SetAuthExecutionPolicy(authUsePolicyFunc(func(context.Context, AuthUse) error {
						checks++
						if policy == "refuse" {
							return errors.New("synthetic private generic HTTP refusal")
						}
						return nil
					}))
				}
				// A synthetic executor handles these calls entirely in memory.
				// The URL is never passed to an HTTP client or socket dialer.
				req, err := http.NewRequestWithContext(f.ctx, http.MethodPost, "https://fixture.invalid/generic", nil)
				if err != nil {
					t.Fatal("generic HTTP fixture request")
				}
				var generated *http.Request
				var response *http.Response
				switch operation {
				case "prepare":
					err = f.manager.PrepareHttpRequest(f.ctx, f.auth, req)
				case "new":
					generated, err = f.manager.NewHttpRequest(f.ctx, f.auth, http.MethodPost, req.URL.String(), nil, nil)
				case "http":
					response, err = f.manager.HttpRequest(f.ctx, f.auth, req)
				}
				if response != nil && response.Body != nil {
					defer response.Body.Close()
				}
				if policy != "nil" {
					requireAuthUseRefusal(t, err)
					var stop interface{ IsRequestStop() bool }
					var scoped interface{ IsRequestScoped() bool }
					if !errors.As(err, &stop) || !stop.IsRequestStop() ||
						!errors.As(err, &scoped) || !scoped.IsRequestScoped() {
						t.Fatal("generic HTTP refusal lost request boundary")
					}
					if exec.prepared != 0 || exec.dispatched != 0 || generated != nil || response != nil ||
						req.Header.Get("Authorization") != "" {
						t.Fatal("generic HTTP policy bypass reached executor")
					}
				} else {
					wantDispatch := 0
					if operation == "http" {
						wantDispatch = 1
					}
					if err != nil || exec.prepared != 1 || exec.dispatched != wantDispatch || exec.authID != f.auth.ID {
						t.Fatal("generic HTTP nil policy changed execution")
					}
					if operation == "new" {
						if generated == nil || generated.Method != http.MethodPost || generated.URL.String() != req.URL.String() {
							t.Fatal("generic HTTP construction changed")
						}
						req = generated
					}
					if req.Header.Get("Authorization") != "Bearer synthetic-a" ||
						req.Context().Value(authUseContextKey{}) != "fixture-context" {
						t.Fatal("generic HTTP credential preparation changed")
					}
					if operation == "http" && (response == nil || response.StatusCode != http.StatusNoContent) {
						t.Fatal("generic HTTP transport response changed")
					}
				}
				// Generic transport has no supported AuthUse/model contract. Even an
				// allowing callback must not manufacture permission for this path.
				if checks != 0 {
					t.Fatal("generic HTTP invented policy operation")
				}
				f.requireNoHealthResult(t)
			})
		}
	}
}

type authUseGenericHTTPExecutor struct {
	*authUseExecutor
	prepared, dispatched int
	authID               string
}

func (e *authUseGenericHTTPExecutor) PrepareRequest(req *http.Request, credential *Auth) error {
	e.prepared++
	e.authID = credential.ID
	req.Header.Set("Authorization", "Bearer "+authAccessToken(credential))
	return nil
}

func (e *authUseGenericHTTPExecutor) HttpRequest(_ context.Context, credential *Auth, req *http.Request) (*http.Response, error) {
	e.dispatched++
	if err := e.PrepareRequest(req, credential); err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody}, nil
}
