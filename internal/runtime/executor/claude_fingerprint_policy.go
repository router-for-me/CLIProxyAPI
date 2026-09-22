package executor

import (
	"fmt"
	"strings"
	"sync"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	claudeFingerprintProfileDefault       = config.ClaudeFingerprintProfileDefault
	claudeFingerprintProfileClaudeCodeCLI = config.ClaudeFingerprintProfileClaudeCodeCLI
	claudeFingerprintProfileAttr          = "fingerprint_profile"
)

// claudeFingerprintProfileWarned deduplicates the unrecognized-value warning.
// Profile resolution runs several times per request (policy, wire policy,
// headers), so warning on every call turns one config typo into a per-request
// log flood. Management writes reject unknown values outright; this only covers
// values that reached the process through a config file or auth JSON.
var claudeFingerprintProfileWarned sync.Map

// claudeFingerprintPolicy is a single switch-driven view of Claude fingerprint
// behavior for Anthropic Messages. The heavy algorithms stay shared:
//   - betas: claudeCodeCLIBetas(..., useOAuthBetas)
//   - CCH: claudeCCHSigningEnabled / finalizeAnthropicMessagesBodyCCH
//   - identity: EnsureClaudeCLIFingerprintIdentity + ApplyClaudeCredentialMetadata
//
// Goal: Anthropic Messages API keys, custom gateways, and delegated providers
// (such as Kimi) can opt into the Claude Code OAuth CLI request fingerprint via
// fingerprint-profile=claude-code-cli, without OAuth control-plane semantics.
// Real Claude OAuth tokens always keep the strict CLI fingerprint. First-party
// api.anthropic.com API keys stay caller-owned by default and only take the CLI
// Messages fingerprint when this field is set. MCP aliases and diagnostics are
// wire fingerprint behavior; refresh, profile and cancellation stay gated on
// AuthIsOAuthToken.
type claudeFingerprintPolicy struct {
	AuthIsOAuthToken     bool
	ProfileClaudeCodeCLI bool
	UseOAuthBetas        bool
	ApplyCLIIdentity     bool
	SynthesizeIdentity   bool
	MCPAlias             bool
	InjectDiagnostics    bool
	OAuthCancellation    bool
}

func normalizeClaudeFingerprintProfile(raw string) string {
	profile, ok := config.NormalizeClaudeFingerprintProfile(raw)
	if !ok {
		if _, warned := claudeFingerprintProfileWarned.LoadOrStore(strings.TrimSpace(raw), struct{}{}); !warned {
			log.Warnf("unrecognized claude fingerprint-profile %q (supported: %q); falling back to default", raw, claudeFingerprintProfileClaudeCodeCLI)
		}
	}
	return profile
}

func claudeFingerprintProfileFromAuth(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return claudeFingerprintProfileDefault
	}
	if auth.Attributes != nil {
		if raw, ok := auth.Attributes[claudeFingerprintProfileAttr]; ok && strings.TrimSpace(raw) != "" {
			return normalizeClaudeFingerprintProfile(raw)
		}
	}
	for _, key := range []string{claudeFingerprintProfileAttr, "fingerprint-profile"} {
		raw := claudeauth.ReadMetadataString(&auth.Metadata, key)
		if strings.TrimSpace(raw) != "" {
			return normalizeClaudeFingerprintProfile(raw)
		}
	}
	return claudeFingerprintProfileDefault
}

func claudeFingerprintProfileFromConfig(cfg *config.Config, auth *cliproxyauth.Auth) string {
	if profile := claudeFingerprintProfileFromAuth(auth); profile != claudeFingerprintProfileDefault {
		return profile
	}
	entry := resolveClaudeKeyConfig(cfg, auth)
	if entry == nil {
		return claudeFingerprintProfileDefault
	}
	return normalizeClaudeFingerprintProfile(entry.FingerprintProfile)
}

// resolveClaudeFingerprintPolicy resolves credential-scoped fingerprint
// behavior. It is deliberately independent of the upstream origin: the wire
// profile follows the credential, while the one origin-sensitive decision (CCH
// signing) is resolved separately by claudeCCHSigningEnabled.
func resolveClaudeFingerprintPolicy(cfg *config.Config, auth *cliproxyauth.Auth, apiKey string) claudeFingerprintPolicy {
	// Keep actual Claude OAuth lifecycle authority separate from the broader
	// request fingerprint policy used by API keys and delegated providers.
	authIsOAuth := isClaudeOAuthToken(apiKey)
	profile := claudeFingerprintProfileFromConfig(cfg, auth)
	profileClaudeCodeCLI := authIsOAuth || profile == claudeFingerprintProfileClaudeCodeCLI

	return claudeFingerprintPolicy{
		AuthIsOAuthToken:     authIsOAuth,
		ProfileClaudeCodeCLI: profileClaudeCodeCLI,
		UseOAuthBetas:        profileClaudeCodeCLI,
		ApplyCLIIdentity:     profileClaudeCodeCLI,
		SynthesizeIdentity:   profileClaudeCodeCLI && !authIsOAuth,
		MCPAlias:             profileClaudeCodeCLI,
		InjectDiagnostics:    profileClaudeCodeCLI,
		OAuthCancellation:    authIsOAuth,
	}
}

// applyClaudeCLIIdentity applies the Claude Code CLI credential identity to the
// upstream Messages body. It is the single implementation behind both the
// streaming and the non-streaming request paths; keep it that way.
//
// ApplyCLIIdentity and ProfileClaudeCodeCLI are the same predicate, so
// sessionID has already been resolved by ClaudeAgentSessionUUIDForRequest,
// which always returns a UUID. Do not add a second session source here: a
// per-apiKey cached ID would silently break agent-conversation continuity.
//
// API keys seed the synthesized identity from the key itself; delegated
// providers such as Kimi seed from the stable auth identity, so an access-token
// rotation does not rotate the device fingerprint.
func applyClaudeCLIIdentity(body []byte, auth *cliproxyauth.Auth, apiKey, upstreamURL, sessionID string, synthesize bool) ([]byte, error) {
	identitySeed := apiKey
	if isKimiMessagesUpstream(auth, upstreamURL) {
		identitySeed = helps.ClaudeCLIAuthIdentitySeed(auth)
	}
	identityAuth, errIdentity := helps.PrepareClaudeCLIFingerprintAuth(auth, identitySeed, synthesize)
	if errIdentity != nil {
		return nil, fmt.Errorf("ensure Claude CLI fingerprint identity: %w", errIdentity)
	}
	updated, _, errApply := helps.ApplyClaudeCredentialMetadata(body, identityAuth, sessionID)
	if errApply != nil {
		return nil, fmt.Errorf("apply Claude credential metadata: %w", errApply)
	}
	return updated, nil
}
