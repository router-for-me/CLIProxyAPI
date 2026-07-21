package auth

// Metadata keys used to persist the permanent-refresh-failure marker
// across restarts. FileTokenStore / PostgresStore / ObjectStore / GitStore
// only durably persist Auth.Metadata, so the marker (which lives on Auth
// struct fields) must be mirrored into Metadata for restart recovery.
//
// Keys are namespaced with the `cpa_permanent_` prefix to avoid colliding
// with provider/plugin credential metadata that may legitimately use
// generic names like "status" or "unavailable" (codex review).
const (
	metadataKeyPermanentRefreshFailure = "cpa_permanent_refresh_failure"
	metadataKeyUnavailable             = "cpa_permanent_unavailable"
	metadataKeyStatus                  = "cpa_permanent_status"
	metadataKeyStatusMessage           = "cpa_permanent_status_message"
)

// ApplyPermanentFailureFromMetadata restores the permanent-refresh-failure
// marker from auth.Metadata into the Auth struct fields. Called by store
// readers (FileTokenStore.readAuthFiles, PostgresStore.List,
// ObjectTokenStore.readAuthFile, GitTokenStore.readAuthFile) after
// rebuilding the Auth from the persisted JSON, so a restart recovers the
// dead-credential marker and the guards (shouldRefresh,
// nextRefreshCheckAt, isAuthBlockedForModel) continue to block the
// revoked credential.
//
// If the marker is absent (auth persisted before this feature, or
// recovered after manual token replacement), this is a no-op — the Auth
// retains its default field values (PermanentRefreshFailure=false,
// Unavailable=false, Status=StatusActive).
func ApplyPermanentFailureFromMetadata(auth *Auth) {
	if auth == nil || len(auth.Metadata) == 0 {
		return
	}
	if v, ok := auth.Metadata[metadataKeyPermanentRefreshFailure].(bool); ok && v {
		auth.PermanentRefreshFailure = true
		// Restore Unavailable only when the marker is present; otherwise
		// leave the default (false) so a recovered auth is routable.
		if u, ok := auth.Metadata[metadataKeyUnavailable].(bool); ok {
			auth.Unavailable = u
		} else {
			auth.Unavailable = true
		}
		if s, ok := auth.Metadata[metadataKeyStatus].(string); ok && s != "" {
			auth.Status = Status(s)
		} else {
			auth.Status = StatusError
		}
		if sm, ok := auth.Metadata[metadataKeyStatusMessage].(string); ok {
			auth.StatusMessage = sm
		}
	}
}
