package executor

// SelectedAuthProviderCallbackMetadataKey carries an optional callback invoked
// with the provider of the selected auth. It works for Home runtime auths too,
// which are not kept in the manager's auth map after a failed attempt.
const SelectedAuthProviderCallbackMetadataKey = "selected_auth_provider_callback"
