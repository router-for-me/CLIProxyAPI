package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// ErrHTTPResponded indicates a paid create must not be retried: an HTTP response was observed.
var ErrHTTPResponded = errors.New("media: HTTP response already observed; create retry forbidden")

// ErrAcceptedHandle indicates a paid create must not be retried after a provider accepted a task.
var ErrAcceptedHandle = errors.New("media: provider handle already accepted; create retry forbidden")

// Manager is a thin media façade over the auth Manager. Callers own durable affinity.
// There is intentionally no package-global job→auth map.
type Manager struct {
	Auth *cliproxyauth.Manager

	mu        sync.RWMutex
	executors map[string]Executor
}

// NewManager wraps an auth manager for media execution.
func NewManager(auth *cliproxyauth.Manager) *Manager {
	return &Manager{Auth: auth, executors: map[string]Executor{}}
}

// RegisterExecutor registers a media executor by provider key.
func (m *Manager) RegisterExecutor(exec Executor) {
	if m == nil || exec == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.executors == nil {
		m.executors = map[string]Executor{}
	}
	m.executors[strings.ToLower(strings.TrimSpace(exec.Identifier()))] = exec
}

// Executor returns a registered media executor.
func (m *Manager) Executor(provider string) (Executor, bool) {
	if m == nil {
		return nil, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.executors[strings.ToLower(strings.TrimSpace(provider))]
	return e, ok
}

// Execute runs a media operation with request-scoped retry policy enforcement.
func (m *Manager) Execute(ctx context.Context, req Request, opts Options) (Result, error) {
	if m == nil || m.Auth == nil {
		return Result{}, errors.New("media: manager not configured")
	}
	if opts.RetryPolicy == "" {
		switch req.Phase {
		case PhaseCreate:
			opts.RetryPolicy = RetryPreResponseFailoverOnly
		case PhaseStatus, PhaseContent:
			opts.RetryPolicy = RetryIdempotent
		default:
			opts.RetryPolicy = RetryNone
		}
	}

	provider := strings.TrimSpace(req.Provider)
	if provider == "" {
		return Result{}, errors.New("media: provider required")
	}

	// Prefer dedicated media executor when registered.
	if exec, ok := m.Executor(provider); ok {
		return m.executeOnce(ctx, exec, req, opts)
	}

	// Fall back to ProviderExecutor.Execute with media metadata in the payload.
	return m.executeViaProvider(ctx, req, opts)
}

func (m *Manager) executeOnce(ctx context.Context, exec Executor, req Request, opts Options) (Result, error) {
	// Pre-response failover only: at most one attempt for create when policy is none/pre_response.
	// Dedicated media executors handle their own transport; we enforce no auto-retry here.
	res, err := exec.ExecuteMedia(ctx, req, opts)
	if req.Phase == PhaseCreate && opts.RetryPolicy == RetryPreResponseFailoverOnly {
		if res.HTTPResponded || res.AcceptedHandle || len(res.Handle) > 0 {
			// Caller must not loop; we never re-enter for them.
			res.HTTPResponded = res.HTTPResponded || res.AcceptedHandle || len(res.Handle) > 0
		}
	}
	if opts.PinnedAuthID != "" && res.SelectedAuth.AuthID == "" {
		res.SelectedAuth.AuthID = opts.PinnedAuthID
		res.SelectedAuth.Provider = req.Provider
	}
	return res, err
}

func (m *Manager) executeViaProvider(ctx context.Context, req Request, opts Options) (Result, error) {
	payload, _ := json.Marshal(req)
	meta := map[string]any{
		OperationMetadataKey:          string(req.Operation),
		PhaseMetadataKey:              string(req.Phase),
		RetryPolicyMetadataKey:        string(opts.RetryPolicy),
		cliproxyexecutor.PinnedAuthMetadataKey: opts.PinnedAuthID,
	}
	var selectedID string
	meta[cliproxyexecutor.SelectedAuthCallbackMetadataKey] = func(authID string) {
		selectedID = authID
	}
	creq := cliproxyexecutor.Request{
		Model:    req.Model,
		Payload:  payload,
		Metadata: meta,
	}
	copts := cliproxyexecutor.Options{
		Metadata: meta,
		Headers:  opts.Headers,
	}
	// Single attempt for pre_response_failover_only / none — do not use global retry.
	resp, err := m.Auth.Execute(ctx, []string{req.Provider}, creq, copts)
	res := Result{
		SelectedAuth: SelectedAuth{AuthID: selectedID, Provider: req.Provider},
	}
	if selectedID == "" && opts.PinnedAuthID != "" {
		res.SelectedAuth.AuthID = opts.PinnedAuthID
	}
	if err != nil {
		// Network errors before response: HTTPResponded stays false.
		if isHTTPResponseError(err) {
			res.HTTPResponded = true
		}
		return res, err
	}
	res.HTTPResponded = true
	if len(resp.Payload) > 0 {
		_ = json.Unmarshal(resp.Payload, &res)
		if len(res.Handle) > 0 {
			res.AcceptedHandle = true
		}
	}
	if res.SelectedAuth.AuthID == "" {
		res.SelectedAuth.AuthID = selectedID
	}
	return res, nil
}

func isHTTPResponseError(err error) bool {
	if err == nil {
		return false
	}
	// Best-effort: auth.Error with HTTPStatus > 0 means a response was seen.
	type statusCoder interface{ StatusCode() int }
	if sc, ok := err.(statusCoder); ok && sc.StatusCode() > 0 {
		return true
	}
	return false
}

// FetchContent performs a proxy/auth-aware GET of a temporary provider URL via the
// selected executor's HttpRequest path.
func (m *Manager) FetchContent(ctx context.Context, provider, pinnedAuthID, rawURL string) ([]byte, string, error) {
	if m == nil || m.Auth == nil {
		return nil, "", errors.New("media: manager not configured")
	}
	if strings.TrimSpace(rawURL) == "" {
		return nil, "", errors.New("media: content url required")
	}
	var auth *cliproxyauth.Auth
	if pinnedAuthID != "" {
		if a, ok := m.Auth.GetByID(pinnedAuthID); ok {
			auth = a
		}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := m.Auth.HttpRequest(ctx, auth, httpReq)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("media: content fetch status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, "", err
	}
	ct := resp.Header.Get("Content-Type")
	return data, ct, nil
}
