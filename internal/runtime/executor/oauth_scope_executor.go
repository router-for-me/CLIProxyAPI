package executor

import cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"

// ForAPIKey binds configuration once after credential selection. Value receivers
// copy executor configuration while preserving shared transport/session stores.
func (e CodexExecutor) ForAPIKey() cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.ForAPIKey()
	return &e
}

func (e ClaudeExecutor) ForAPIKey() cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.ForAPIKey()
	return &e
}

func (e GeminiExecutor) ForAPIKey() cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.ForAPIKey()
	return &e
}

func (e GeminiVertexExecutor) ForAPIKey() cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.ForAPIKey()
	return &e
}

func (e OpenAICompatExecutor) ForAPIKey() cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.ForAPIKey()
	return &e
}

func (e MetaExecutor) ForAPIKey() cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.ForAPIKey()
	return &e
}

func (e XAIExecutor) ForAPIKey() cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.ForAPIKey()
	return &e
}

func (e CodexWebsocketsExecutor) ForAPIKey() cliproxyauth.ProviderExecutor {
	if e.CodexExecutor != nil {
		e.CodexExecutor = e.CodexExecutor.ForAPIKey().(*CodexExecutor)
	}
	return &e
}

func (e XAIWebsocketsExecutor) ForAPIKey() cliproxyauth.ProviderExecutor {
	if e.XAIExecutor != nil {
		e.XAIExecutor = e.XAIExecutor.ForAPIKey().(*XAIExecutor)
	}
	return &e
}

func (e CodexAutoExecutor) ForAPIKey() cliproxyauth.ProviderExecutor {
	if e.httpExec != nil {
		e.httpExec = e.httpExec.ForAPIKey().(*CodexExecutor)
	}
	if e.wsExec != nil {
		e.wsExec = e.wsExec.ForAPIKey().(*CodexWebsocketsExecutor)
	}
	return &e
}

func (e XAIAutoExecutor) ForAPIKey() cliproxyauth.ProviderExecutor {
	if e.httpExec != nil {
		e.httpExec = e.httpExec.ForAPIKey().(*XAIExecutor)
	}
	if e.wsExec != nil {
		e.wsExec = e.wsExec.ForAPIKey().(*XAIWebsocketsExecutor)
	}
	return &e
}
