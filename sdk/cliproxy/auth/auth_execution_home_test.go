package auth

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type authPolicyHomeDispatcher struct{ calls atomic.Int64 }

func (*authPolicyHomeDispatcher) HeartbeatOK() bool       { return true }
func (*authPolicyHomeDispatcher) AbortAmbiguousDispatch() {}
func (d *authPolicyHomeDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	d.calls.Add(1)
	return nil, errors.New("fixture remote dispatch attempted")
}

func TestAuthExecutionPolicyRefusesUnsupportedHome(t *testing.T) {
	for _, operation := range []string{"execute", "stream", "count", "verify"} {
		t.Run(operation, func(t *testing.T) {
			f := newAuthUseFixture(t)
			f.manager.SetAuthExecutionPolicy(authUsePolicyFunc(func(context.Context, AuthUse) error { return nil }))
			// SetConfig models a reload that enables Home after policy setup.
			f.manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			dispatcher := &authPolicyHomeDispatcher{}
			f.manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
			var err error
			if operation == "verify" {
				_, err = f.manager.VerifyInference(f.ctx, f.auth.ID, f.model)
			} else {
				err = f.invoke(AuthUseKind(operation), cliproxyexecutor.Options{})
			}
			requireAuthUseRefusal(t, err)
			prepares, inputs := f.exec.observed()
			if dispatcher.calls.Load() != 0 || prepares != 0 || len(inputs) != 0 {
				t.Fatal("unsupported Home mode performed remote or provider IO")
			}
		})
	}
}
