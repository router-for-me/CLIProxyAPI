package executor

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func (e *CodexExecutor) usesV1Compaction(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) bool {
	var models []config.CodexModel
	prefix := ""
	if entry := e.resolveCodexConfig(auth); entry != nil {
		models = entry.Models
		prefix = entry.Prefix
	}
	return helps.ModelUsesV1Compaction(req, opts, models, prefix)
}

func (e *CodexExecutor) v1CompactionCredentials(auth *cliproxyauth.Auth) (string, []string) {
	key, baseURL := codexCreds(auth)
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	secrets := []string{key}
	if e.cfg != nil {
		for _, entry := range e.cfg.CodexKey {
			entryBaseURL := strings.TrimRight(strings.TrimSpace(entry.BaseURL), "/")
			if entryBaseURL == "" {
				entryBaseURL = "https://chatgpt.com/backend-api/codex"
			}
			if entryBaseURL == baseURL {
				secrets = append(secrets, entry.APIKey)
			}
		}
	}
	return e.Identifier() + "\x00" + baseURL, secrets
}

func (e *CodexExecutor) prepareV1Compaction(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Request, bool, error) {
	if opts.Alt != "" || !e.usesV1Compaction(auth, req, opts) || (opts.SourceFormat != sdktranslator.FormatOpenAIResponse && opts.SourceFormat != sdktranslator.FormatCodex) {
		return req, false, nil
	}
	summary := helps.HasResponsesCompactionTrigger(req.Payload)
	if summary || helps.HasResponsesCompactionItem(req.Payload) {
		if err := helps.ValidateV1CompactionContext(ctx, req.Payload); err != nil {
			return req, false, err
		}
	}
	scope, secrets := e.v1CompactionCredentials(auth)
	payload, errExpand := helps.ExpandResponsesCompactionCapsules(req.Payload, scope, secrets)
	if errExpand == nil {
		req.Payload = payload
	}
	return req, summary && errExpand == nil, errExpand
}

func (e *OpenAICompatExecutor) usesV1Compaction(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) bool {
	var models []config.OpenAICompatibilityModel
	prefix := ""
	if entry := e.resolveCompatConfig(auth, req); entry != nil {
		models = entry.Models
		prefix = entry.Prefix
	}
	return helps.ModelUsesV1Compaction(req, opts, models, prefix)
}

func (e *OpenAICompatExecutor) v1CompactionCredentials(auth *cliproxyauth.Auth, req cliproxyexecutor.Request) (string, []string) {
	baseURL, key := e.resolveCredentials(auth)
	secrets := []string{key}
	if entry := e.resolveCompatConfig(auth, req); entry != nil {
		for _, entryKey := range entry.APIKeyEntries {
			secrets = append(secrets, entryKey.APIKey)
		}
	}
	return e.Identifier() + "\x00" + strings.TrimRight(baseURL, "/"), secrets
}

func (e *OpenAICompatExecutor) needsV1Responses(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) bool {
	return opts.Alt == "" && (opts.SourceFormat == sdktranslator.FormatOpenAIResponse || opts.SourceFormat == sdktranslator.FormatCodex) && e.usesV1Compaction(auth, req, opts) && (helps.HasResponsesCompactionTrigger(req.Payload) || helps.HasResponsesCompactionItem(req.Payload))
}
