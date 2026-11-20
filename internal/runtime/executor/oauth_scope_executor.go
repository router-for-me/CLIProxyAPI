package executor

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// ForAPIKey binds configuration once after credential selection. Value receivers
// copy executor configuration while preserving shared transport/session stores.
func (e CodexExecutor) ForAPIKey(source config.PayloadFilterSource) cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.ForConfiguredKey(source)
	return &e
}

// ForAPIKey binds the selected key while retaining shared state.
func (e ClaudeExecutor) ForAPIKey(source config.PayloadFilterSource) cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.ForConfiguredKey(source)
	return &e
}

// ForAPIKey binds the selected key while retaining shared state.
func (e GeminiExecutor) ForAPIKey(source config.PayloadFilterSource) cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.ForConfiguredKey(source)
	return &e
}

// ForAPIKey binds the selected key while retaining shared state.
func (e GeminiVertexExecutor) ForAPIKey(source config.PayloadFilterSource) cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.ForConfiguredKey(source)
	return &e
}

// ForAPIKey binds the selected key while retaining shared state.
func (e OpenAICompatExecutor) ForAPIKey(source config.PayloadFilterSource) cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.ForConfiguredKey(source)
	return &e
}

// ForAPIKey binds the selected key while retaining shared state.
func (e MetaExecutor) ForAPIKey(source config.PayloadFilterSource) cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.ForConfiguredKey(source)
	return &e
}

// ForAPIKey binds the selected key while retaining shared state.
func (e XAIExecutor) ForAPIKey(source config.PayloadFilterSource) cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.ForConfiguredKey(source)
	return &e
}

// ForAPIKey binds the selected key while retaining shared state.
func (e CodexWebsocketsExecutor) ForAPIKey(source config.PayloadFilterSource) cliproxyauth.ProviderExecutor {
	if e.CodexExecutor != nil {
		e.CodexExecutor = e.CodexExecutor.ForAPIKey(source).(*CodexExecutor)
	}
	return &e
}

// ForAPIKey binds the selected key while retaining shared state.
func (e XAIWebsocketsExecutor) ForAPIKey(source config.PayloadFilterSource) cliproxyauth.ProviderExecutor {
	if e.XAIExecutor != nil {
		e.XAIExecutor = e.XAIExecutor.ForAPIKey(source).(*XAIExecutor)
	}
	return &e
}

// ForAPIKey binds the selected key while retaining shared state.
func (e CodexAutoExecutor) ForAPIKey(source config.PayloadFilterSource) cliproxyauth.ProviderExecutor {
	if e.httpExec != nil {
		e.httpExec = e.httpExec.ForAPIKey(source).(*CodexExecutor)
	}
	if e.wsExec != nil {
		e.wsExec = e.wsExec.ForAPIKey(source).(*CodexWebsocketsExecutor)
	}
	return &e
}

// ForAPIKey binds the selected key while retaining shared state.
func (e XAIAutoExecutor) ForAPIKey(source config.PayloadFilterSource) cliproxyauth.ProviderExecutor {
	if e.httpExec != nil {
		e.httpExec = e.httpExec.ForAPIKey(source).(*XAIExecutor)
	}
	if e.wsExec != nil {
		e.wsExec = e.wsExec.ForAPIKey(source).(*XAIWebsocketsExecutor)
	}
	return &e
}

// WithKeyPayloadFilters copies the Codex config while preserving OAuth settings.
func (e CodexExecutor) WithKeyPayloadFilters(source config.PayloadFilterSource) cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.WithKeyPayloadFilters(source)
	return &e
}

// WithKeyPayloadFilters copies the Gemini config while preserving OAuth settings.
func (e GeminiExecutor) WithKeyPayloadFilters(source config.PayloadFilterSource) cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.WithKeyPayloadFilters(source)
	return &e
}

// WithKeyPayloadFilters copies the Vertex config while preserving OAuth settings.
func (e GeminiVertexExecutor) WithKeyPayloadFilters(source config.PayloadFilterSource) cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.WithKeyPayloadFilters(source)
	return &e
}

// WithKeyPayloadFilters copies the compatibility config while preserving OAuth settings.
func (e OpenAICompatExecutor) WithKeyPayloadFilters(source config.PayloadFilterSource) cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.WithKeyPayloadFilters(source)
	return &e
}

// WithKeyPayloadFilters copies the xAI config while preserving OAuth settings.
func (e XAIExecutor) WithKeyPayloadFilters(source config.PayloadFilterSource) cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.WithKeyPayloadFilters(source)
	return &e
}

// WithKeyPayloadFilters binds Codex filters without replacing the WebSocket session store.
func (e CodexWebsocketsExecutor) WithKeyPayloadFilters(source config.PayloadFilterSource) cliproxyauth.ProviderExecutor {
	e.CodexExecutor = e.CodexExecutor.WithKeyPayloadFilters(source).(*CodexExecutor)
	return &e
}

// WithKeyPayloadFilters binds xAI filters without replacing the WebSocket session store.
func (e XAIWebsocketsExecutor) WithKeyPayloadFilters(source config.PayloadFilterSource) cliproxyauth.ProviderExecutor {
	e.XAIExecutor = e.XAIExecutor.WithKeyPayloadFilters(source).(*XAIExecutor)
	return &e
}

// WithKeyPayloadFilters binds the same Codex policy for both transports.
func (e CodexAutoExecutor) WithKeyPayloadFilters(source config.PayloadFilterSource) cliproxyauth.ProviderExecutor {
	e.httpExec = e.httpExec.WithKeyPayloadFilters(source).(*CodexExecutor)
	e.wsExec = e.wsExec.WithKeyPayloadFilters(source).(*CodexWebsocketsExecutor)
	return &e
}

// WithKeyPayloadFilters binds the same xAI policy for both transports.
func (e XAIAutoExecutor) WithKeyPayloadFilters(source config.PayloadFilterSource) cliproxyauth.ProviderExecutor {
	e.httpExec = e.httpExec.WithKeyPayloadFilters(source).(*XAIExecutor)
	e.wsExec = e.wsExec.WithKeyPayloadFilters(source).(*XAIWebsocketsExecutor)
	return &e
}
