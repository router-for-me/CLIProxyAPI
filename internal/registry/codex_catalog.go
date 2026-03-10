package registry

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

const codexModelsFetchTimeout = 15 * time.Second

var codexModelsURLs = []string{
	"https://raw.githubusercontent.com/router-for-me/models/refs/heads/main/models.json",
	"https://models.router-for.me/models.json",
}

//go:embed models/codex_models.json
var embeddedCodexModelsJSON []byte

type codexModelsJSON struct {
	CodexFree []*ModelInfo `json:"codex-free"`
	CodexTeam []*ModelInfo `json:"codex-team"`
	CodexPlus []*ModelInfo `json:"codex-plus"`
	CodexPro  []*ModelInfo `json:"codex-pro"`
}

type codexCatalogStore struct {
	mu   sync.RWMutex
	data *codexModelsJSON
}

var globalCodexCatalog = &codexCatalogStore{}
var codexUpdaterOnce sync.Once

func init() {
	if err := loadCodexModelsCatalogFromBytes(embeddedCodexModelsJSON, "embed"); err != nil {
		panic(fmt.Sprintf("registry: failed to parse embedded codex catalog: %v", err))
	}
}

func StartCodexModelsUpdater(ctx context.Context, cfg *sdkconfig.SDKConfig, onUpdated func()) {
	codexUpdaterOnce.Do(func() {
		go func() {
			if err := refreshCodexModelsCatalog(ctx, cfg); err != nil {
				log.Warnf("codex models refresh failed, using embedded catalog: %v", err)
				return
			}
			if onUpdated != nil {
				onUpdated()
			}
		}()
	})
}

func GetCodexFreeModels() []*ModelInfo {
	return cloneCodexModelInfos(getCodexCatalog().CodexFree)
}

func GetCodexTeamModels() []*ModelInfo {
	return cloneCodexModelInfos(getCodexCatalog().CodexTeam)
}

func GetCodexPlusModels() []*ModelInfo {
	return cloneCodexModelInfos(getCodexCatalog().CodexPlus)
}

func GetCodexProModels() []*ModelInfo {
	return cloneCodexModelInfos(getCodexCatalog().CodexPro)
}

func GetCodexModelsForPlan(planType string) []*ModelInfo {
	switch NormalizeCodexPlanType(planType) {
	case "pro":
		return GetCodexProModels()
	case "plus":
		return GetCodexPlusModels()
	case "team":
		return GetCodexTeamModels()
	case "free":
		fallthrough
	default:
		return GetCodexFreeModels()
	}
}

func GetCodexModelsUnion() []*ModelInfo {
	catalog := getCodexCatalog()
	sections := [][]*ModelInfo{catalog.CodexFree, catalog.CodexTeam, catalog.CodexPlus, catalog.CodexPro}
	seen := make(map[string]struct{})
	out := make([]*ModelInfo, 0)
	for _, models := range sections {
		for _, model := range models {
			if model == nil || strings.TrimSpace(model.ID) == "" {
				continue
			}
			if _, ok := seen[model.ID]; ok {
				continue
			}
			seen[model.ID] = struct{}{}
			out = append(out, cloneModelInfo(model))
		}
	}
	return out
}

func NormalizeCodexPlanType(planType string) string {
	switch strings.ToLower(strings.TrimSpace(planType)) {
	case "free":
		return "free"
	case "team", "business", "enterprise", "edu", "education":
		return "team"
	case "plus":
		return "plus"
	case "pro":
		return "pro"
	default:
		return ""
	}
}

func ResolveCodexPlanType(attributes map[string]string, metadata map[string]any) string {
	if attributes != nil {
		for _, key := range []string{"plan_type", "chatgpt_plan_type"} {
			if plan := NormalizeCodexPlanType(attributes[key]); plan != "" {
				return plan
			}
		}
	}
	plan, _ := EnsureCodexPlanTypeMetadata(metadata)
	return plan
}

func EnsureCodexPlanTypeMetadata(metadata map[string]any) (string, bool) {
	if metadata == nil {
		return "", false
	}
	for _, key := range []string{"plan_type", "chatgpt_plan_type"} {
		if raw, ok := metadata[key].(string); ok {
			if plan := NormalizeCodexPlanType(raw); plan != "" {
				if current, _ := metadata["plan_type"].(string); NormalizeCodexPlanType(current) != plan {
					metadata["plan_type"] = plan
					return plan, true
				}
				return plan, false
			}
		}
	}
	idToken := firstString(metadata, "id_token")
	if idToken == "" {
		idToken = nestedString(metadata, "token", "id_token")
	}
	if idToken == "" {
		idToken = nestedString(metadata, "tokens", "id_token")
	}
	if idToken == "" {
		return "", false
	}
	plan, err := extractCodexPlanTypeFromJWT(idToken)
	if err != nil {
		return "", false
	}
	if plan == "" {
		return "", false
	}
	metadata["plan_type"] = plan
	return plan, true
}

func getCodexCatalog() *codexModelsJSON {
	globalCodexCatalog.mu.RLock()
	data := globalCodexCatalog.data
	globalCodexCatalog.mu.RUnlock()
	if data != nil {
		return data
	}
	return &codexModelsJSON{}
}

func refreshCodexModelsCatalog(ctx context.Context, cfg *sdkconfig.SDKConfig) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if cfg == nil {
		cfg = &sdkconfig.SDKConfig{}
	}
	client := newCodexCatalogHTTPClient(cfg)
	var errs []string
	for _, rawURL := range codexModelsURLs {
		url := strings.TrimSpace(rawURL)
		if url == "" {
			continue
		}
		requestCtx, cancel := context.WithTimeout(ctx, codexModelsFetchTimeout)
		req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, url, nil)
		if err != nil {
			cancel()
			errs = append(errs, fmt.Sprintf("%s: create request: %v", url, err))
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			errs = append(errs, fmt.Sprintf("%s: %v", url, err))
			continue
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		cancel()
		if readErr != nil {
			errs = append(errs, fmt.Sprintf("%s: read body: %v", url, readErr))
			continue
		}
		if resp.StatusCode != http.StatusOK {
			errs = append(errs, fmt.Sprintf("%s: status %d", url, resp.StatusCode))
			continue
		}
		if err := loadCodexModelsCatalogFromBytes(body, url); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		log.Infof("codex models catalog refreshed from %s", url)
		return nil
	}
	if len(errs) == 0 {
		return fmt.Errorf("no codex catalog source URLs configured")
	}
	return fmt.Errorf("%s", strings.Join(errs, "; "))
}

func loadCodexModelsCatalogFromBytes(data []byte, source string) error {
	var parsed codexModelsJSON
	if err := json.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("%s: decode codex catalog: %w", source, err)
	}
	if err := validateCodexModelsCatalog(&parsed); err != nil {
		return fmt.Errorf("%s: validate codex catalog: %w", source, err)
	}
	globalCodexCatalog.mu.Lock()
	globalCodexCatalog.data = &parsed
	globalCodexCatalog.mu.Unlock()
	return nil
}

func validateCodexModelsCatalog(data *codexModelsJSON) error {
	if data == nil {
		return fmt.Errorf("catalog is nil")
	}
	sections := []struct {
		name   string
		models []*ModelInfo
	}{
		{name: "codex-free", models: data.CodexFree},
		{name: "codex-team", models: data.CodexTeam},
		{name: "codex-plus", models: data.CodexPlus},
		{name: "codex-pro", models: data.CodexPro},
	}
	for _, section := range sections {
		if len(section.models) == 0 {
			return fmt.Errorf("%s section is empty", section.name)
		}
		seen := make(map[string]struct{}, len(section.models))
		for i, model := range section.models {
			if model == nil {
				return fmt.Errorf("%s[%d] is null", section.name, i)
			}
			id := strings.TrimSpace(model.ID)
			if id == "" {
				return fmt.Errorf("%s[%d] has empty id", section.name, i)
			}
			if _, ok := seen[id]; ok {
				return fmt.Errorf("%s contains duplicate model id %q", section.name, id)
			}
			seen[id] = struct{}{}
		}
	}
	return nil
}

func firstString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	if value, ok := metadata[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func nestedString(metadata map[string]any, parent, key string) string {
	if metadata == nil {
		return ""
	}
	raw, ok := metadata[parent]
	if !ok {
		return ""
	}
	child, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	value, _ := child[key].(string)
	return strings.TrimSpace(value)
}

func extractCodexPlanTypeFromJWT(token string) (string, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid jwt format")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", err
	}
	var claims struct {
		Auth struct {
			PlanType string `json:"chatgpt_plan_type"`
		} `json:"https://api.openai.com/auth"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", err
	}
	return NormalizeCodexPlanType(claims.Auth.PlanType), nil
}

func newCodexCatalogHTTPClient(cfg *sdkconfig.SDKConfig) *http.Client {
	client := &http.Client{Timeout: codexModelsFetchTimeout}
	if cfg == nil || strings.TrimSpace(cfg.ProxyURL) == "" {
		return client
	}
	proxyURL, err := url.Parse(strings.TrimSpace(cfg.ProxyURL))
	if err != nil {
		return client
	}
	switch proxyURL.Scheme {
	case "http", "https":
		client.Transport = &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	case "socks5":
		var auth *proxy.Auth
		if proxyURL.User != nil {
			password, _ := proxyURL.User.Password()
			auth = &proxy.Auth{User: proxyURL.User.Username(), Password: password}
		}
		dialer, err := proxy.SOCKS5("tcp", proxyURL.Host, auth, proxy.Direct)
		if err != nil {
			return client
		}
		client.Transport = &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			},
		}
	}
	return client
}

func cloneCodexModelInfos(models []*ModelInfo) []*ModelInfo {
	if len(models) == 0 {
		return nil
	}
	out := make([]*ModelInfo, len(models))
	for i, model := range models {
		out[i] = cloneModelInfo(model)
	}
	return out
}
