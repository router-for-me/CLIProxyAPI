package auth

import "context"

// This path is private to successful provider invocations. Exported Update
// methods never supply a capture, so ordinary writes cannot mint provenance.
func (m *Manager) captureProviderTransition(before *Auth, kind AuthUseKind) *authTransitionCapture {
	observer, ok := m.executionPolicy().(AuthTransitionObserver)
	if !ok {
		return nil
	}
	snapshot, err := snapshotAuthForPolicy(before)
	if err != nil {
		// An unavailable proof must not discard a successful rotated grant.
		return nil
	}
	return &authTransitionCapture{observer: observer, event: AuthTransition{Kind: kind, Before: snapshot}}
}

func (m *Manager) updateProviderAuth(ctx context.Context, before, updated *Auth, mode updateAuthMode, capture *authTransitionCapture) (*Auth, error) {
	return m.updateInternalWithTransition(ctx, before, updated, mode, capture)
}

type authTransitionCapture struct {
	observer AuthTransitionObserver
	event    AuthTransition
}

func (c *authTransitionCapture) commitBase(auth *Auth) {
	if c != nil {
		c.event.CommitBase, _ = snapshotAuthForPolicy(auth)
	}
}

func (c *authTransitionCapture) merged(auth *Auth) {
	if c != nil {
		c.event.Merged, _ = snapshotAuthForPolicy(auth)
	}
}

func (c *authTransitionCapture) notify(ctx context.Context, auth *Auth) {
	if c == nil || c.event.CommitBase == nil || c.event.Merged == nil {
		return
	}
	c.event.Committed, _ = snapshotAuthForPolicy(auth)
	if c.event.Committed != nil {
		c.observer.ObserveTransition(ctx, c.event)
	}
}
