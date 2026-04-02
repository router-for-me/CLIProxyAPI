package executor

import (
	"context"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	"github.com/tiktoken-go/tokenizer"
)

func newProxyAwareHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	return helps.NewProxyAwareHTTPClient(ctx, cfg, auth, timeout)
}

func parseOpenAIUsage(data []byte) usage.Detail {
	return helps.ParseOpenAIUsage(data)
}

func parseOpenAIStreamUsage(line []byte) (usage.Detail, bool) {
	return helps.ParseOpenAIStreamUsage(line)
}

func parseOpenAIResponsesUsage(data []byte) usage.Detail {
	return helps.ParseOpenAIUsage(data)
}

func parseOpenAIResponsesStreamUsage(line []byte) (usage.Detail, bool) {
	return helps.ParseOpenAIStreamUsage(line)
}

func getTokenizer(model string) (tokenizer.Codec, error) {
	return helps.TokenizerForModel(model)
}

func countOpenAIChatTokens(enc tokenizer.Codec, payload []byte) (int64, error) {
	return helps.CountOpenAIChatTokens(enc, payload)
}

func countClaudeChatTokens(enc tokenizer.Codec, payload []byte) (int64, error) {
	return helps.CountClaudeChatTokens(enc, payload)
}

func buildOpenAIUsageJSON(count int64) []byte {
	return helps.BuildOpenAIUsageJSON(count)
}
