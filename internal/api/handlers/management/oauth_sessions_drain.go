package management

import "time"

// HasPendingOAuthSessions prevents maintenance from terminating an accepted
// login or credential commit after its initiating HTTP request has returned.
// Terminal/expired sessions cannot commit credentials through the pending guards.
func HasPendingOAuthSessions() bool {
	return oauthSessions.hasPending(time.Now())
}

func (s *oauthSessionStore) hasPending(now time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, session := range s.sessions {
		if session.Committing || session.SIWCCommitting {
			return true
		}
		if !session.Completed && session.Status == "" &&
			(session.ExpiresAt.IsZero() || !now.After(session.ExpiresAt)) {
			return true
		}
	}
	return false
}
