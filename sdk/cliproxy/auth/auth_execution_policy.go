package auth

import "context"

// AuthUseKind identifies a provider operation checked by AuthExecutionPolicy.
type AuthUseKind string

const (
	AuthUseExecute AuthUseKind = "execute"
	AuthUseStream  AuthUseKind = "stream"
	AuthUseCount   AuthUseKind = "count"
	AuthUsePrepare AuthUseKind = "prepare"
	AuthUseRefresh AuthUseKind = "refresh"
)

// AuthUse describes the actual credential and model about to reach a provider.
type AuthUse struct {
	Kind AuthUseKind
	// Auth is a detached snapshot. Storage, Runtime, and CodexRefreshPatch are
	// omitted because they are opaque shared objects rather than credential data.
	Auth *Auth
	// RequestedModel is the client model before routing; Model is the upstream
	// model. Direct preparation calls may have no model information.
	RequestedModel string
	Model          string
}

// AuthExecutionPolicy checks actual provider inputs for Manager execution,
// stream startup, token counting, request-auth preparation and refresh.
// Home dispatch and the generic PrepareHttpRequest, NewHttpRequest and
// HttpRequest helpers are unsupported while a policy is configured; they
// return a fixed request refusal instead of invoking those provider paths.
// Implementations must be safe for concurrent calls, must not retain credential
// snapshots, and must not perform IO or call back into Manager. Request context
// values are preserved. Any non-nil error becomes a fixed 503 request refusal;
// the callback's error text is not exposed, and no provider result is recorded.
type AuthExecutionPolicy interface {
	Check(context.Context, AuthUse) error
}

// AuthTransition describes a successfully committed provider preparation or
// refresh. Before is its actual input; CommitBase is the current record used by
// the SDK merge; Merged is the normalized result before persistence; Committed
// is the saved result. These detached snapshots omit opaque runtime objects.
type AuthTransition struct {
	Kind       AuthUseKind
	Before     *Auth
	CommitBase *Auth
	Merged     *Auth
	Committed  *Auth
}

// AuthTransitionObserver is an optional interface on AuthExecutionPolicy.
// ObserveTransition runs after a trusted provider result is committed, outside
// the manager mutex but before releasing the credential mutation gate. It must
// not call Manager or perform IO. Public Update methods, discarded results, and
// cached credentials do not emit transitions. Notification cannot fail a commit.
type AuthTransitionObserver interface {
	ObserveTransition(context.Context, AuthTransition)
}

type authExecutionPolicyHolder struct {
	policy AuthExecutionPolicy
}

// SetAuthExecutionPolicy configures the optional policy before serving requests.
// A nil policy preserves stock execution behavior. The host must keep the policy
// fixed for the service lifetime, including configuration reloads.
func (m *Manager) SetAuthExecutionPolicy(policy AuthExecutionPolicy) {
	if m == nil {
		return
	}
	if policy == nil {
		m.authExecutionPolicy.Store(nil)
		return
	}
	m.authExecutionPolicy.Store(&authExecutionPolicyHolder{policy: policy})
}
