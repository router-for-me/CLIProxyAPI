package auth

// SelectedAuthSnapshotCallbackMetadataKey carries an optional func(*Auth)
// invoked with a copy of each selected auth. Unlike a later GetByID lookup it
// also covers Home runtime auths, which are released after a failed attempt.
const SelectedAuthSnapshotCallbackMetadataKey = "selected_auth_snapshot_callback"

// publishSelectedAuthSnapshot reports a copy of the selected auth to the
// optional SelectedAuthSnapshotCallbackMetadataKey callback.
func publishSelectedAuthSnapshot(meta map[string]any, auth *Auth) {
	callback, ok := meta[SelectedAuthSnapshotCallbackMetadataKey].(func(*Auth))
	if !ok || callback == nil || auth == nil {
		return
	}
	callback(auth.Clone())
}
