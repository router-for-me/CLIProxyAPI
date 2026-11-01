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
