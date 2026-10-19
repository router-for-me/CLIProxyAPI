package synthesizer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	kimiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kimi"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

// FileSynthesizer generates Auth entries from OAuth JSON files.
// It handles file-based authentication.
type FileSynthesizer struct{}

// NewFileSynthesizer creates a new FileSynthesizer instance.
func NewFileSynthesizer() *FileSynthesizer {
	return &FileSynthesizer{}
}

// Synthesize generates Auth entries from auth files in the auth directory.
func (s *FileSynthesizer) Synthesize(ctx *SynthesisContext) ([]*coreauth.Auth, error) {
	out := make([]*coreauth.Auth, 0, 16)
	if ctx == nil || ctx.AuthDir == "" {
		return out, nil
	}

	entries, err := os.ReadDir(ctx.AuthDir)
	if err != nil {
		// Not an error if directory doesn't exist
		return out, nil
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		full := filepath.Join(ctx.AuthDir, name)
		data, errRead := os.ReadFile(full)
		if errRead != nil {
			continue
		}
		if !json.Valid(data) {
			ctx.RecordIncompleteFile(full)
			continue
		}
		auths, errSynthesize := synthesizeFileAuths(ctx, full, data)
		if errSynthesize != nil {
			log.WithError(errSynthesize).Warnf("skipping auth file %s", name)
			continue
		}
		if len(auths) == 0 {
			continue
		}
		out = append(out, auths...)
	}
	return out, nil
}

// SynthesizeAuthFile generates Auth entries for one auth JSON file payload.
// It shares exactly the same mapping behavior as FileSynthesizer.Synthesize.
func SynthesizeAuthFile(ctx *SynthesisContext, fullPath string, data []byte) ([]*coreauth.Auth, error) {
	return synthesizeFileAuths(ctx, fullPath, data)
}

func synthesizeFileAuths(ctx *SynthesisContext, fullPath string, data []byte) ([]*coreauth.Auth, error) {
	if ctx == nil || len(data) == 0 {
		return nil, nil
	}
	now := ctx.Now
	var metadata map[string]any
	if errUnmarshal := json.Unmarshal(data, &metadata); errUnmarshal != nil {
		if ctx.StrictFileParsing {
			return nil, fmt.Errorf("parse auth file: %w", errUnmarshal)
		}
		return nil, nil
	}
	coreauth.NormalizeCredentialMetadata(metadata)
	if errWeight := coreauth.ValidateAuthWeight(&coreauth.Auth{Metadata: metadata}); errWeight != nil {
		return nil, fmt.Errorf("invalid weight in %s: %w", filepath.Base(fullPath), errWeight)
	}
	// Invalid per-account rules must not drop an existing credential: keep the valid
	// entries and warn. Write entry points (upload and PATCH) reject them before persisting.
	perAccountExcluded, errExcluded := ExtractExcludedModelsFromMetadata(metadata)
	if errExcluded != nil {
		log.WithError(errExcluded).Warnf("ignoring invalid excluded models in auth file %s", filepath.Base(fullPath))
	}
	t, _ := metadata["type"].(string)
	provider := strings.ToLower(strings.TrimSpace(t))
	if provider == "gemini" {
		provider = "gemini-cli"
	}
	if ctx.PluginAuthParser != nil {
		auths, handled, errParse := parsePluginFileAuths(ctx.PluginAuthParser, pluginapi.AuthParseRequest{
			Provider: provider,
			Path:     fullPath,
			FileName: filepath.Base(fullPath),
			RawJSON:  data,
		})
		if errParse != nil && ctx.StrictFileParsing {
			return nil, fmt.Errorf("parse plugin auth file: %w", errParse)
		}
		if errParse == nil && handled {
			auths = compactPluginAuths(auths)
			if len(auths) == 0 {
				return nil, nil
			}
			perAccountModelAliases := extractOAuthModelAliasesFromMetadata(metadata)
			disabled, _ := metadata["disabled"].(bool)
			for index, auth := range auths {
				if auth == nil {
					continue
				}
				coreauth.NormalizeCredentialMetadata(auth.Metadata)
				if len(auths) > 1 {
					coreauth.MarkPluginVirtualAuth(auth, fullPath, index)
				}
				auth.CreatedAt = now
				auth.UpdatedAt = now
				if auth.Attributes == nil {
					auth.Attributes = make(map[string]string)
				}
				auth.Attributes[coreauth.AttributePath] = fullPath
				auth.Attributes[coreauth.AttributeSource] = fullPath
				auth.Attributes[coreauth.AttributeSourceBackend] = coreauth.AuthSourceFile
				if disabled {
					auth.Disabled = true
					auth.Status = coreauth.StatusDisabled
					if auth.Metadata == nil {
						auth.Metadata = make(map[string]any)
					}
					auth.Metadata["disabled"] = true
				}
				if p, ok := metadata["proxy_url"].(string); ok && auth.ProxyURL == "" {
					auth.ProxyURL = strings.TrimSpace(p)
				}
				if pref, ok := metadata["prefix"].(string); ok && auth.Prefix == "" {
					auth.Prefix = strings.Trim(strings.TrimSpace(pref), "/")
				}
				if errWeight := coreauth.ApplyAuthWeightMetadata(auth, metadata); errWeight != nil {
					return nil, fmt.Errorf("invalid plugin auth weight in %s: %w", filepath.Base(fullPath), errWeight)
				}
				coreauth.ApplyAuthPriorityMetadata(auth, metadata)
				if _, inherited := auth.Attributes[coreauth.AttributeFilePriority]; inherited {
					if setter, ok := auth.Storage.(interface{ SetMetadata(map[string]any) }); ok {
						setter.SetMetadata(auth.Metadata)
					}
				}
				coreauth.SetOAuthModelAliasesAttribute(auth, perAccountModelAliases)
				ApplyAuthExcludedModelsMeta(auth, perAccountExcluded, "oauth")
				coreauth.ApplyCustomHeadersFromMetadata(auth)
				applyFingerprintProfileAttribute(auth, metadata)
				auth.BindFileSnapshot(fullPath, data)
			}
			return auths, nil
		}
	}
	if provider == "" || provider == "gemini-cli" {
		return nil, nil
	}
	label := provider
	if email, _ := metadata["email"].(string); email != "" {
		label = email
	}
	// Use relative path under authDir as ID to stay consistent with the file-based token store.
	id := fullPath
	if strings.TrimSpace(ctx.AuthDir) != "" {
		if rel, errRel := filepath.Rel(ctx.AuthDir, fullPath); errRel == nil && rel != "" {
			id = rel
		}
	}
	if runtime.GOOS == "windows" {
		id = strings.ToLower(id)
	}

	proxyURL := ""
	if p, ok := metadata["proxy_url"].(string); ok {
		proxyURL = p
	}

	prefix := ""
	if rawPrefix, ok := metadata["prefix"].(string); ok {
		trimmed := strings.TrimSpace(rawPrefix)
		trimmed = strings.Trim(trimmed, "/")
		if trimmed != "" && !strings.Contains(trimmed, "/") {
			prefix = trimmed
		}
	}

	disabled, _ := metadata["disabled"].(bool)
	status := coreauth.StatusActive
	if disabled {
		status = coreauth.StatusDisabled
	}

	perAccountModelAliases := extractOAuthModelAliasesFromMetadata(metadata)

	a := &coreauth.Auth{
		ID:       id,
		FileName: filepath.Base(fullPath),
		Provider: provider,
		Label:    label,
		Prefix:   prefix,
		Status:   status,
		Disabled: disabled,
		Attributes: map[string]string{
			coreauth.AttributeSource:        fullPath,
			coreauth.AttributePath:          fullPath,
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
		},
		ProxyURL:  proxyURL,
		Metadata:  metadata,
		CreatedAt: now,
		UpdatedAt: now,
	}
	// Read priority from auth file.
	coreauth.ApplyAuthPriorityMetadata(a, metadata)
	if errWeight := coreauth.ApplyAuthWeightMetadata(a, metadata); errWeight != nil {
		return nil, fmt.Errorf("invalid auth weight in %s: %w", filepath.Base(fullPath), errWeight)
	}
	// Read note from auth file.
	if rawNote, ok := metadata["note"]; ok {
		if note, isStr := rawNote.(string); isStr {
			if trimmed := strings.TrimSpace(note); trimmed != "" {
				a.Attributes["note"] = trimmed
			}
		}
	}
	coreauth.ApplyCustomHeadersFromMetadata(a)
	coreauth.SetOAuthModelAliasesAttribute(a, perAccountModelAliases)
	applyOpenAPIModelsAttribute(a, metadata)
	ApplyAuthExcludedModelsMeta(a, perAccountExcluded, "oauth")
	applyFingerprintProfileAttribute(a, metadata)
	// For Kimi auth files, preserve domain and base_url attributes.
	if provider == "kimi" || provider == "kimi-ai" || provider == "kimi.ai" || provider == "kimi.com" {
		if bu, ok := metadata["base_url"].(string); ok && strings.TrimSpace(bu) != "" {
			a.Attributes["base_url"] = strings.TrimSpace(bu)
		}
		if dom, ok := metadata["domain"].(string); ok && strings.TrimSpace(dom) != "" {
			a.Attributes["domain"] = strings.TrimSpace(dom)
		}
		resolvedDomain := kimiauth.ResolveKimiDomainFromAuth(a)
		if a.Attributes["domain"] == "" {
			a.Attributes["domain"] = resolvedDomain
		} else {
			a.Attributes["domain"] = kimiauth.NormalizeKimiDomain(a.Attributes["domain"])
		}
		if a.Attributes["base_url"] == "" {
			a.Attributes["base_url"] = kimiauth.ResolveKimiAPIBaseURL(resolvedDomain)
		}
	}
	// For codex auth files, extract plan_type from metadata or JWT id_token.
	if provider == "codex" {
		if ptRaw, ok := metadata["plan_type"].(string); ok && strings.TrimSpace(ptRaw) != "" {
			a.Attributes["plan_type"] = strings.TrimSpace(ptRaw)
		} else if idTokenRaw, ok := metadata["id_token"].(string); ok && strings.TrimSpace(idTokenRaw) != "" {
			if claims, errParse := codex.ParseJWTToken(idTokenRaw); errParse == nil && claims != nil {
				a.Attributes["plan_type"] = claims.GetPlanType()
			} else {
				a.Attributes["plan_type"] = codex.DefaultPlanType
			}
		}
	}
	a.BindFileSnapshot(fullPath, data)
	return []*coreauth.Auth{a}, nil
}

func parsePluginFileAuths(parser PluginAuthParser, req pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
	if parser == nil {
		return nil, false, nil
	}
	if multiParser, ok := parser.(PluginMultiAuthParser); ok {
		return multiParser.ParseAuths(context.Background(), req)
	}
	auth, handled, errParse := parser.ParseAuth(context.Background(), req)
	if errParse != nil || !handled || auth == nil {
		return nil, handled, errParse
	}
	return []*coreauth.Auth{auth}, true, nil
}

func compactPluginAuths(auths []*coreauth.Auth) []*coreauth.Auth {
	if len(auths) == 0 {
		return nil
	}
	out := auths[:0]
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		if errWeight := coreauth.ValidateAuthWeight(auth); errWeight != nil {
			continue
		}
		out = append(out, auth)
	}
	return out
}

// extractOAuthModelAliasesFromMetadata reads per-account model aliases from OAuth JSON metadata.
// "model_aliases" is canonical; "model-aliases" remains a legacy alias.
// Entries under "models" that specify both "name" and "alias" are also accepted.
func extractOAuthModelAliasesFromMetadata(metadata map[string]any) []config.OAuthModelAlias {
	if metadata == nil {
		return nil
	}
	var combined []config.OAuthModelAlias
	for _, key := range []string{"model_aliases", "model-aliases", "models"} {
		raw, ok := metadata[key]
		if !ok || raw == nil {
			continue
		}
		data, errMarshal := json.Marshal(raw)
		if errMarshal != nil {
			continue
		}
		var aliases []config.OAuthModelAlias
		if errUnmarshal := json.Unmarshal(data, &aliases); errUnmarshal != nil {
			continue
		}
		combined = append(combined, aliases...)
	}
	if len(combined) == 0 {
		return nil
	}
	cfg := config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"auth": combined,
		},
	}
	cfg.SanitizeOAuthModelAlias()
	return cfg.OAuthModelAlias["auth"]
}

func applyOpenAPIModelsAttribute(auth *coreauth.Auth, metadata map[string]any) {
	if auth == nil || metadata == nil {
		return
	}
	var names []string
	seen := make(map[string]struct{})
	add := func(raw string) {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			return
		}
		key := strings.ToLower(trimmed)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		names = append(names, trimmed)
	}
	for _, key := range []string{"openapi_models", "openapi-models", "models"} {
		raw, ok := metadata[key]
		if !ok || raw == nil {
			continue
		}
		switch v := raw.(type) {
		case string:
			for _, part := range strings.Split(v, ",") {
				add(part)
			}
		case []string:
			for _, item := range v {
				add(item)
			}
		case []any:
			for _, item := range v {
				switch elem := item.(type) {
				case string:
					add(elem)
				case map[string]any:
					if name, okName := elem["name"].(string); okName {
						add(name)
					}
				}
			}
		}
	}
	if len(names) == 0 {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes["openapi_models"] = strings.Join(names, ",")
}

// ExtractExcludedModelsFromMetadata reads per-account excluded models from the OAuth JSON metadata.
// "excluded_models" is canonical; "excluded-models" remains a legacy alias. The value must be a
// string array; a single string is also accepted for older hand-written files and is split on
// commas, because model IDs never contain commas. Any other type, or a non-string array element,
// is an error. For non-string elements the valid string entries are still returned with the
// error, so lenient readers can keep the usable rules while strict writers reject the value.
func ExtractExcludedModelsFromMetadata(metadata map[string]any) ([]string, error) {
	if metadata == nil {
		return nil, nil
	}
	key := "excluded_models"
	raw, ok := metadata[key]
	if !ok {
		key = "excluded-models"
		raw, ok = metadata[key]
	}
	if !ok || raw == nil {
		return nil, nil
	}
	var stringSlice []string
	var errInvalid error
	switch v := raw.(type) {
	case string:
		stringSlice = strings.Split(v, ",")
	case []string:
		stringSlice = v
	case []interface{}:
		stringSlice = make([]string, 0, len(v))
		for index, item := range v {
			s, okString := item.(string)
			if !okString {
				if errInvalid == nil {
					errInvalid = fmt.Errorf("metadata %s[%d] must be a string, got %T", key, index, item)
				}
				continue
			}
			stringSlice = append(stringSlice, s)
		}
	default:
		return nil, fmt.Errorf("metadata %s must be a string array, got %T", key, raw)
	}
	result := make([]string, 0, len(stringSlice))
	for _, s := range stringSlice {
		if trimmed := strings.TrimSpace(s); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result, errInvalid
}
