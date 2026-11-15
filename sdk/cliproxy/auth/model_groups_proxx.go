package auth

// proxx: model groups ("auto" model). A request whose provider list is the synthetic
// groups.Provider is fanned out to the group's member models, each executed through the normal
// Execute / ExecuteStream / ExecuteCount path with req.Model set to the member. Members whose
// credentials are all cooling down are skipped before execution, and members are executed in a
// no-wait mode so a fresh 429 fails over immediately instead of sleeping in waitForCooldown.
// See docs/proxx/decisions/0003-model-groups.md.

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/proxx/groups"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const (
	// ProxxServedModelHeader reports the group member that served the request.
	ProxxServedModelHeader = "X-Proxx-Served-Model"
	// ProxxModelGroupMetadataKey records the requested group name on member executions.
	ProxxModelGroupMetadataKey = "proxx_model_group"
)

type proxxNoWaitKey struct{}

// proxxGroupMaxWait is the retry-wait hook used by Execute, ExecuteCount and ExecuteStream.
// Group member executions run with maxWait 0: retry rounds that need no wait still run, but a
// round that would sleep for a cooldown ends so the group can fail over to the next member.
func proxxGroupMaxWait(ctx context.Context, maxWait time.Duration) time.Duration {
	if ctx != nil {
		if noWait, _ := ctx.Value(proxxNoWaitKey{}).(bool); noWait {
			return 0
		}
	}
	return maxWait
}

func isProxxGroupRequest(providers []string) bool {
	for _, provider := range providers {
		if strings.EqualFold(strings.TrimSpace(provider), groups.Provider) {
			return true
		}
	}
	return false
}

// proxxExecuteGroup is the Execute hook. handled is false for non-group requests.
func (m *Manager) proxxExecuteGroup(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, handled bool, err error) {
	if !isProxxGroupRequest(providers) {
		return resp, false, nil
	}
	resp, err = proxxRunGroup(m, ctx, req, opts, m.Execute)
	return resp, true, err
}

// proxxExecuteCountGroup is the ExecuteCount hook.
func (m *Manager) proxxExecuteCountGroup(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, handled bool, err error) {
	if !isProxxGroupRequest(providers) {
		return resp, false, nil
	}
	resp, err = proxxRunGroup(m, ctx, req, opts, m.ExecuteCount)
	return resp, true, err
}

// proxxExecuteStreamGroup is the ExecuteStream hook. A member is committed only after its first
// stream chunk arrived without an error, so failover never happens after bytes reached the client.
func (m *Manager) proxxExecuteStreamGroup(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (result *cliproxyexecutor.StreamResult, handled bool, err error) {
	if !isProxxGroupRequest(providers) {
		return nil, false, nil
	}
	result, err = proxxRunGroup(m, ctx, req, opts, m.proxxStreamMember)
	return result, true, err
}

// proxxStreamMember runs ExecuteStream for one member and peeks the first chunk. The manager
// already buffers the stream bootstrap, so an upstream failure before the first payload arrives
// either as the returned error or as the only chunk of the result; both are returned as errors.
func (m *Manager) proxxStreamMember(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	result, err := m.ExecuteStream(ctx, providers, req, opts)
	if err != nil {
		return nil, err
	}
	if result == nil || result.Chunks == nil {
		return nil, &Error{Code: "empty_stream", Message: "upstream stream has no source", Retryable: true, HTTPStatus: http.StatusBadGateway}
	}
	var (
		first cliproxyexecutor.StreamChunk
		ok    bool
	)
	select {
	case first, ok = <-result.Chunks:
	case <-ctx.Done():
		discardStreamChunks(result.Chunks)
		return nil, ctx.Err()
	}
	if !ok {
		return nil, &Error{Code: "empty_stream", Message: "upstream stream closed before the first chunk", Retryable: true, HTTPStatus: http.StatusBadGateway}
	}
	if first.Err != nil {
		discardStreamChunks(result.Chunks)
		return nil, first.Err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	source := result.Chunks
	go func() {
		defer close(out)
		chunk := first
		for {
			select {
			case out <- chunk:
			case <-ctx.Done():
				discardStreamChunks(source)
				return
			}
			next, more := <-source
			if !more {
				return
			}
			chunk = next
		}
	}()
	forwarded := *result
	forwarded.Chunks = out
	return &forwarded, nil
}

type proxxCooledMember struct {
	model     string
	providers []string
	req       cliproxyexecutor.Request
	opts      cliproxyexecutor.Options
	until     time.Time
}

func proxxRunGroup[T any](m *Manager, ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, run func(context.Context, []string, cliproxyexecutor.Request, cliproxyexecutor.Options) (T, error)) (T, error) {
	var zero T
	if ctx == nil {
		ctx = context.Background()
	}
	group, suffix, ok := groups.Lookup(req.Model)
	if !ok {
		return zero, &Error{Code: "model_not_found", Message: "model group " + req.Model + " is not configured", HTTPStatus: http.StatusBadRequest}
	}
	entry := logEntryWithRequestID(ctx)
	noWaitCtx := context.WithValue(ctx, proxxNoWaitKey{}, true)
	var cooled []proxxCooledMember
	var lastErr error
	for _, member := range groups.Members(group, suffix) {
		if errCtx := ctx.Err(); errCtx != nil {
			return zero, errCtx
		}
		providers := proxxMemberProviders(member)
		if len(providers) == 0 {
			entry.Debugf("proxx: group %s skips %s: no provider serves it", group.Name, member)
			if lastErr == nil {
				lastErr = &Error{Code: "model_not_found", Message: "no provider serves " + member, HTTPStatus: http.StatusServiceUnavailable}
			}
			continue
		}
		memberReq, memberOpts := proxxMemberRequest(req, opts, group.Name, member)
		until, hasReady, _ := m.earliestPoolCooldownForModelAt(ctx, providers, member, memberOpts, time.Now())
		if !hasReady {
			entry.Debugf("proxx: group %s skips %s: no ready credential (cooldown until %s)", group.Name, member, until.Format(time.RFC3339))
			if !until.IsZero() {
				cooled = append(cooled, proxxCooledMember{model: member, providers: providers, req: memberReq, opts: memberOpts, until: until})
			}
			continue
		}
		out, errRun := run(noWaitCtx, providers, memberReq, memberOpts)
		if errRun == nil {
			proxxCommitMember(ctx, group.Name, member)
			return out, nil
		}
		if !proxxShouldFailover(ctx, errRun) {
			return zero, errRun
		}
		entry.Infof("proxx: group %s member %s failed (status %d), failing over: %v", group.Name, member, statusCodeFromError(errRun), errRun)
		lastErr = errRun
	}
	if len(cooled) > 0 {
		// Every member is cooling down or failed. Run the member that recovers first with the
		// normal cooldown wait, which is bounded by routing.retry.max-retry-interval exactly as
		// for a plain model request (longer cooldowns return 429 with Retry-After).
		sort.SliceStable(cooled, func(i, j int) bool { return cooled[i].until.Before(cooled[j].until) })
		next := cooled[0]
		out, errRun := run(ctx, next.providers, next.req, next.opts)
		if errRun == nil {
			proxxCommitMember(ctx, group.Name, next.model)
			return out, nil
		}
		return zero, errRun
	}
	if lastErr == nil {
		lastErr = &Error{Code: "auth_not_found", Message: "no member of model group " + group.Name + " is available", HTTPStatus: http.StatusServiceUnavailable}
	}
	return zero, lastErr
}

func proxxCommitMember(ctx context.Context, group, member string) {
	logEntryWithRequestID(ctx).Debugf("proxx: group %s served by %s", group, member)
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil || ginCtx.Request == nil || ginCtx.IsWebsocket() {
		return
	}
	ginCtx.Header(ProxxServedModelHeader, member)
}

// proxxMemberRequest rewrites the request for one member so translation, payload rules and
// usage accounting see the real model instead of the group name.
func proxxMemberRequest(req cliproxyexecutor.Request, opts cliproxyexecutor.Options, group, member string) (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	groupModel := req.Model
	req.Model = member
	meta := cloneRequestMetadata(opts.Metadata)
	meta[cliproxyexecutor.RequestedModelMetadataKey] = member
	if raw, ok := meta[cliproxyexecutor.AuthSelectionModelMetadataKey].(string); ok && (raw == groupModel || raw == group) {
		meta[cliproxyexecutor.AuthSelectionModelMetadataKey] = member
	}
	meta[ProxxModelGroupMetadataKey] = group
	opts.Metadata = meta
	return req, opts
}

// proxxMemberProviders resolves the providers of a member the same way handlers do.
func proxxMemberProviders(member string) []string {
	base := member
	if idx := strings.LastIndex(member, "("); idx > 0 && strings.HasSuffix(member, ")") {
		base = member[:idx]
	}
	providers := util.GetProviderName(strings.TrimSpace(base))
	if len(providers) == 0 && base != member {
		providers = util.GetProviderName(member)
	}
	out := providers[:0]
	for _, provider := range providers {
		if provider != groups.Provider {
			out = append(out, provider)
		}
	}
	return out
}

// proxxShouldFailover reports whether a member failure should move on to the next member.
// Client-side request errors stop the group (another model would reject them too), except
// context-length errors, which another model with a larger window may accept.
func proxxShouldFailover(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if isRequestTerminatedError(err) || isRequestStopError(err) || isStreamTruncatedError(err) {
		return false
	}
	status := statusCodeFromError(err)
	switch {
	case status == 0:
		return true
	case status == http.StatusTooManyRequests, status >= http.StatusInternalServerError:
		return true
	case status == http.StatusUnauthorized, status == http.StatusPaymentRequired, status == http.StatusForbidden,
		status == http.StatusNotFound, status == http.StatusRequestTimeout, status == http.StatusRequestEntityTooLarge:
		return true
	case status == http.StatusBadRequest, status == http.StatusUnprocessableEntity:
		return proxxIsContextLengthError(err)
	default:
		return false
	}
}

var proxxContextLengthMarkers = []string{
	"context length", "context_length", "context window", "maximum context",
	"prompt is too long", "input is too long", "too many tokens", "token limit",
	"reduce the length", "exceeds the maximum number of tokens",
}

func proxxIsContextLengthError(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, marker := range proxxContextLengthMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// ProxxModelAvailability describes whether a model can be served right now.
type ProxxModelAvailability struct {
	Providers     []string
	Credentials   int
	Available     bool
	CooldownUntil time.Time
}

// ProxxModelAvailability reports the providers, credential count and cooldown state of a model
// using the same candidate filters as selection. It is used by the proxx groups endpoint.
func (m *Manager) ProxxModelAvailability(ctx context.Context, model string) ProxxModelAvailability {
	out := ProxxModelAvailability{Providers: proxxMemberProviders(model)}
	if m == nil || len(out.Providers) == 0 {
		return out
	}
	if ctx == nil {
		ctx = context.Background()
	}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: model}}
	until, hasReady, _ := m.earliestPoolCooldownForModelAt(ctx, out.Providers, model, opts, time.Now())
	out.Available = hasReady
	if !hasReady {
		out.CooldownUntil = until
	}
	providerSet := make(map[string]struct{}, len(out.Providers))
	for _, provider := range out.Providers {
		providerSet[canonicalSchedulingProvider(provider)] = struct{}{}
	}
	registryRef := registry.GetGlobalRegistry()
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, candidate := range m.auths {
		if candidate == nil || candidate.Disabled {
			continue
		}
		if _, match := providerSet[canonicalSchedulingProvider(executorKeyFromAuth(candidate))]; !match {
			continue
		}
		if m.authSupportsRouteModel(registryRef, candidate, model) {
			out.Credentials++
		}
	}
	return out
}
