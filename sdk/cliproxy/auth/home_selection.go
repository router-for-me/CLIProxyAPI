package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
)

type executionResources struct {
	mu      sync.Mutex
	closed  bool
	closers []func() error
}

type attemptCancel struct {
	cancel context.CancelFunc
	once   sync.Once
}

func (a *attemptCancel) Cancel() {
	if a == nil || a.cancel == nil {
		return
	}
	a.once.Do(a.cancel)
}

type attemptCancels struct {
	mu      sync.Mutex
	closed  bool
	next    uint64
	cancels map[uint64]*attemptCancel
}

func (a *attemptCancels) Add(cancel context.CancelFunc) (func(), error) {
	if a == nil || cancel == nil {
		return func() {}, executionregistry.ErrInvalidExecutionResource
	}

	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		cancel()
		return func() {}, executionregistry.ErrRegistryNotAccepting
	}
	if a.cancels == nil {
		a.cancels = make(map[uint64]*attemptCancel)
	}
	a.next++
	token := a.next
	attempt := &attemptCancel{cancel: cancel}
	a.cancels[token] = attempt
	a.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			delete(a.cancels, token)
			a.mu.Unlock()
			attempt.Cancel()
		})
	}, nil
}

func (a *attemptCancels) Close() error {
	if a == nil {
		return nil
	}

	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	cancels := a.cancels
	a.cancels = nil
	a.mu.Unlock()

	for _, cancel := range cancels {
		cancel.Cancel()
	}
	return nil
}

func (a *attemptCancels) Len() int {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.cancels)
}

func (r *executionResources) Add(closeFn func() error) error {
	if closeFn == nil {
		return executionregistry.ErrInvalidExecutionResource
	}

	r.mu.Lock()
	if !r.closed {
		r.closers = append(r.closers, closeFn)
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()

	if errClose := closeFn(); errClose != nil {
		return errors.Join(executionregistry.ErrRegistryNotAccepting, errClose)
	}
	return executionregistry.ErrRegistryNotAccepting
}

func (r *executionResources) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	closers := slices.Clone(r.closers)
	r.closers = nil
	r.mu.Unlock()

	var result error
	for index := len(closers) - 1; index >= 0; index-- {
		result = errors.Join(result, closers[index]())
	}
	return result
}

// HomeDispatchSelection keeps a Home execution scope separate from its auth.
type HomeDispatchSelection struct {
	Auth     *Auth
	Executor ProviderExecutor
	Provider string

	scope            *executionregistry.Scope
	accountedModel   string
	resources        *executionResources
	attemptCancels   *attemptCancels
	once             sync.Once
	retained         atomic.Bool
	runtimeAuthBound atomic.Bool
	ended            atomic.Bool
}

func newHomeDispatchSelection(auth *Auth, executor ProviderExecutor, provider string, scope *executionregistry.Scope) (*HomeDispatchSelection, error) {
	if scope == nil {
		return nil, fmt.Errorf("Home dispatch selection has no execution scope")
	}

	resources := &executionResources{}
	attemptCancels := &attemptCancels{}
	if errBind := resources.Add(attemptCancels.Close); errBind != nil {
		_ = attemptCancels.Close()
		scope.End("attempt_cancel_bind_failed")
		return nil, errBind
	}
	if errBind := scope.Bind(resources.Close); errBind != nil {
		_ = resources.Close()
		scope.End("resource_controller_bind_failed")
		return nil, errBind
	}

	return &HomeDispatchSelection{
		Auth:           auth,
		Executor:       executor,
		Provider:       strings.TrimSpace(provider),
		scope:          scope,
		resources:      resources,
		attemptCancels: attemptCancels,
	}, nil
}

// Bind adds a resource to be closed when this selection ends or drains.
func (s *HomeDispatchSelection) Bind(closeFn func() error) error {
	if s == nil || s.resources == nil {
		if closeFn != nil {
			_ = closeFn()
		}
		return fmt.Errorf("Home dispatch selection has no execution resources")
	}
	return s.resources.Add(closeFn)
}

// AttemptContext creates a selection-owned context and returns its release function.
func (s *HomeDispatchSelection) AttemptContext(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	attemptCtx, cancelAttempt := context.WithCancel(ctx)
	if s == nil || s.attemptCancels == nil {
		cancelAttempt()
		return nil, func() {}, fmt.Errorf("Home dispatch selection has no attempt cancels")
	}
	release, errAdd := s.attemptCancels.Add(cancelAttempt)
	if errAdd != nil {
		cancelAttempt()
		return nil, func() {}, errAdd
	}
	return attemptCtx, release, nil
}

// Retain transfers selection ownership from a request to an execution session.
func (s *HomeDispatchSelection) Retain() {
	if s == nil || s.ended.Load() {
		return
	}
	s.retained.Store(true)
}

// Retained reports whether an executor transferred this selection to a session.
func (s *HomeDispatchSelection) Retained() bool {
	return s != nil && s.retained.Load() && !s.ended.Load()
}

// Active reports whether the selection has not ended.
func (s *HomeDispatchSelection) Active() bool {
	return s != nil && !s.ended.Load()
}

// End closes all bound resources and releases the Home execution scope once.
func (s *HomeDispatchSelection) End(reason string) {
	_ = s.EndWithRelease(reason)
}

// EndWithRelease closes all bound resources and returns the Home release ticket.
func (s *HomeDispatchSelection) EndWithRelease(reason string) *executionregistry.ReleaseTicket {
	if s == nil {
		return nil
	}
	var ticket *executionregistry.ReleaseTicket
	s.once.Do(func() {
		s.ended.Store(true)
		if s.scope != nil {
			ticket = s.scope.EndWithRelease(strings.TrimSpace(reason))
		}
	})
	if ticket != nil || s.scope == nil {
		return ticket
	}
	return s.scope.EndWithRelease("")
}

// CloneAuth returns a standalone auth copy without the selection handle.
func (s *HomeDispatchSelection) CloneAuth() *Auth {
	if s == nil || s.Auth == nil {
		return nil
	}
	return s.Auth.Clone()
}

// CloneAuthForRoute returns an auth copy adapted for a retained canonical route.
func (s *HomeDispatchSelection) CloneAuthForRoute(routeModel string) *Auth {
	auth := s.CloneAuth()
	if auth == nil || !s.Retained() {
		return auth
	}
	return cloneRetainedHomeAuthForRoute(auth, routeModel)
}

func cloneRetainedHomeAuthForRoute(auth *Auth, routeModel string) *Auth {
	if auth == nil || auth.Attributes == nil {
		return auth
	}
	upstreamModel := strings.TrimSpace(auth.Attributes[homeUpstreamModelAttributeKey])
	if upstreamModel == "" {
		return auth
	}
	upstreamBase, _ := splitRecognizedHomeReasoningSuffix(upstreamModel)
	_, routeSuffix := splitRecognizedHomeReasoningSuffix(routeModel)
	auth.Attributes[homeUpstreamModelAttributeKey] = upstreamBase + routeSuffix
	if strings.EqualFold(strings.TrimSpace(auth.Attributes[homeForceMappingAttributeKey]), "true") {
		auth.Attributes[homeOriginalAliasAttributeKey] = strings.TrimSpace(rewriteModelForAuth(routeModel, auth))
	}
	return auth
}

func splitRecognizedHomeReasoningSuffix(model string) (string, string) {
	model = strings.Trim(model, asciiWhitespace)
	if !strings.HasSuffix(model, ")") {
		return model, ""
	}
	open := strings.LastIndexByte(model, '(')
	if open < 0 || !recognizedHomeConcurrencySuffix(model[open+1:len(model)-1]) {
		return model, ""
	}
	base := strings.Trim(model[:open], asciiWhitespace)
	if base == "" {
		return model, ""
	}
	return base, model[open:]
}
