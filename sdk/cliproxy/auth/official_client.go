package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// OfficialClientRequiredCode is the Error code returned when every credential
// able to serve a request was skipped by the official-client restriction.
const OfficialClientRequiredCode = "official_client_required"

// OfficialClientVerifier reports whether the inbound request described by opts
// was issued by the official client of provider ("claude" or "codex").
type OfficialClientVerifier func(ctx context.Context, provider string, opts cliproxyexecutor.Options) bool

var officialClientVerifier atomic.Pointer[OfficialClientVerifier]

// OfficialClientRequirement reports whether credentials of provider are
// restricted to that provider's official client.
type OfficialClientRequirement func(provider string) bool

var officialClientRequirement atomic.Pointer[OfficialClientRequirement]

// SetOfficialClientRequirement installs the process-wide lookup that decides which
// providers are restricted to their official client (the oauth.official-client-only
// configuration). Without a lookup no provider is restricted.
func SetOfficialClientRequirement(requirement OfficialClientRequirement) {
	if requirement == nil {
		officialClientRequirement.Store(nil)
		return
	}
	officialClientRequirement.Store(&requirement)
}

// SetOfficialClientVerifier installs the process-wide verifier consulted for
// providers restricted to their official client. Without a verifier restricted
// providers are never selected (fail closed).
func SetOfficialClientVerifier(verifier OfficialClientVerifier) {
	if verifier == nil {
		officialClientVerifier.Store(nil)
		return
	}
	officialClientVerifier.Store(&verifier)
}

// SupportsOfficialClientOnly reports whether provider can be restricted to its official client.
func SupportsOfficialClientOnly(provider string) bool {
	return officialClientName(strings.ToLower(strings.TrimSpace(provider))) != ""
}

func officialClientName(provider string) string {
	switch provider {
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex"
	default:
		return ""
	}
}

// officialClientRequired reports whether auth belongs to a provider restricted to its official client.
func officialClientRequired(auth *Auth) bool {
	if auth == nil || !SupportsOfficialClientOnly(auth.Provider) {
		return false
	}
	requirement := officialClientRequirement.Load()
	if requirement == nil || *requirement == nil {
		return false
	}
	return (*requirement)(strings.ToLower(strings.TrimSpace(auth.Provider)))
}

// officialClientCheck lazily verifies, once per provider, that the current
// request came from the official client, and remembers which providers had
// credentials skipped so selection failures can be explained.
type officialClientCheck struct {
	ctx  context.Context
	opts cliproxyexecutor.Options

	mu       sync.Mutex
	verdicts map[string]bool
	rejected map[string]struct{}
}

type officialClientCheckContextKey struct{}

func newOfficialClientCheck(ctx context.Context, opts cliproxyexecutor.Options) *officialClientCheck {
	if ctx == nil {
		ctx = context.Background()
	}
	return &officialClientCheck{ctx: ctx, opts: opts}
}

func withOfficialClientCheck(ctx context.Context, check *officialClientCheck) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, officialClientCheckContextKey{}, check)
}

func officialClientCheckFromContext(ctx context.Context) *officialClientCheck {
	if ctx == nil {
		return nil
	}
	check, _ := ctx.Value(officialClientCheckContextKey{}).(*officialClientCheck)
	return check
}

// allows reports whether auth may serve the request under its official-client restriction.
func (c *officialClientCheck) allows(auth *Auth) bool {
	if c == nil || !officialClientRequired(auth) {
		return true
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	c.mu.Lock()
	defer c.mu.Unlock()
	verdict, seen := c.verdicts[provider]
	if !seen {
		verdict = c.verifyLocked(provider)
		if c.verdicts == nil {
			c.verdicts = make(map[string]bool, 2)
		}
		c.verdicts[provider] = verdict
	}
	if !verdict {
		if c.rejected == nil {
			c.rejected = make(map[string]struct{}, 2)
		}
		c.rejected[provider] = struct{}{}
	}
	return verdict
}

func (c *officialClientCheck) verifyLocked(provider string) bool {
	verifier := officialClientVerifier.Load()
	if verifier == nil || *verifier == nil {
		return false
	}
	return (*verifier)(c.ctx, provider, c.opts)
}

// rejectionError turns a "no auth available" selection failure into a 403 when
// credentials were skipped because the request did not come from an official
// client. Any other error is returned unchanged.
func (c *officialClientCheck) rejectionError(err error) error {
	if c == nil || err == nil {
		return err
	}
	var authErr *Error
	if !errors.As(err, &authErr) || authErr == nil || authErr.Code != "auth_not_found" {
		return err
	}
	c.mu.Lock()
	names := make([]string, 0, len(c.rejected))
	for provider := range c.rejected {
		names = append(names, officialClientName(provider))
	}
	c.mu.Unlock()
	if len(names) == 0 {
		return err
	}
	sort.Strings(names)
	return &Error{
		Code:       OfficialClientRequiredCode,
		Message:    fmt.Sprintf("no auth available: credentials restricted to official %s clients were skipped because this request did not come from one", strings.Join(names, " and ")),
		HTTPStatus: http.StatusForbidden,
	}
}
