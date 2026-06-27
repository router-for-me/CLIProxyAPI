package translator

import (
	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/claude/gemini"
	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/claude/openai/chat-completions"
	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/claude/openai/responses"

	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/codex/claude"
	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/codex/gemini"
	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/codex/openai/chat-completions"
	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/codex/openai/responses"

	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/gemini/claude"
	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/gemini/gemini"
	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/gemini/openai/chat-completions"
	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/gemini/openai/responses"

	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/openai/claude"
	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/openai/gemini"
	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/openai/openai/chat-completions"
	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/openai/openai/responses"

	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/antigravity/claude"
	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/antigravity/gemini"
	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/antigravity/openai/chat-completions"
	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/antigravity/openai/responses"
)
