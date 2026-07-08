package translator

import (
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/claude/gemini"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/claude/interactions"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/claude/openai/chat-completions"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/claude/openai/responses"

	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/claude"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/gemini"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/interactions"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/openai/chat-completions"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/openai/responses"

	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/gemini/claude"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/gemini/gemini"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/gemini/interactions"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/gemini/openai/chat-completions"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/gemini/openai/responses"

	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/interactions/claude"

	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/claude"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/gemini"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/interactions/chat-completions"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/interactions/responses"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/openai/chat-completions"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/openai/responses"

	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/antigravity/claude"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/antigravity/gemini"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/antigravity/interactions"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/antigravity/openai/chat-completions"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/antigravity/openai/responses"
)
