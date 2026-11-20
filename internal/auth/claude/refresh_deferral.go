package claude

import baseauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth"

// ErrClaudeRefreshDeferred indicates Claude OAuth renewal should be deferred and retried later.
var ErrClaudeRefreshDeferred = baseauth.ErrRefreshDeferred
