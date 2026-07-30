package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	codexFingerprintFetchTimeout    = 30 * time.Second
	codexFingerprintRefreshInterval = time.Hour
	maxCodexFingerprintSourceSize   = 1 << 20
)

type codexFingerprintSourceSet struct {
	DistTagsURL          string
	DefaultClientURL     string
	ClientURL            string
	ResponsesMetadataURL string
	RequestHeadersURL    string
	CommitURL            string
}

var codexFingerprintSources = codexFingerprintSourceSet{
	DistTagsURL:          "https://registry.npmjs.org/-/package/@openai%2Fcodex/dist-tags",
	DefaultClientURL:     "https://raw.githubusercontent.com/openai/codex/main/codex-rs/login/src/auth/default_client.rs",
	ClientURL:            "https://raw.githubusercontent.com/openai/codex/main/codex-rs/core/src/client.rs",
	ResponsesMetadataURL: "https://raw.githubusercontent.com/openai/codex/main/codex-rs/core/src/responses_metadata.rs",
	RequestHeadersURL:    "https://raw.githubusercontent.com/openai/codex/main/codex-rs/codex-api/src/requests/headers.rs",
	CommitURL:            "https://api.github.com/repos/openai/codex/commits/main",
}

var codexFingerprintUpdaterOnce sync.Once

// StartCodexFingerprintUpdater starts the official Codex application profile
// refresh loop. It is safe to call multiple times.
func StartCodexFingerprintUpdater(ctx context.Context) {
	codexFingerprintUpdaterOnce.Do(func() {
		go runCodexFingerprintUpdater(ctx)
	})
}

func runCodexFingerprintUpdater(ctx context.Context) {
	tryRefreshCodexFingerprint(ctx, "startup Codex fingerprint refresh")
	ticker := time.NewTicker(codexFingerprintRefreshInterval)
	defer ticker.Stop()
	log.Infof("periodic Codex fingerprint refresh started (interval=%s)", codexFingerprintRefreshInterval)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tryRefreshCodexFingerprint(ctx, "periodic Codex fingerprint refresh")
		}
	}
}

func tryRefreshCodexFingerprint(ctx context.Context, label string) {
	current := GetCodexFingerprintProfile()
	client := &http.Client{Timeout: codexFingerprintFetchTimeout}
	candidate, errFetch := fetchCodexFingerprintProfile(ctx, client, codexFingerprintSources, current)
	if errFetch != nil {
		log.Warnf("%s failed, keeping current profile: %v", label, errFetch)
		return
	}
	changed, errUpdate := codexFingerprintCatalogStore.update(candidate)
	if errUpdate != nil {
		log.Warnf("%s rejected, keeping current profile: %v", label, errUpdate)
		return
	}
	if !changed {
		log.Infof("%s completed, no changes detected", label)
		return
	}
	log.Infof("%s completed (version=%s source=%s)", label, candidate.Version, candidate.SourceRevision)
}

func fetchCodexFingerprintProfile(
	ctx context.Context,
	client *http.Client,
	sources codexFingerprintSourceSet,
	base CodexFingerprintProfile,
) (CodexFingerprintProfile, error) {
	if client == nil {
		client = &http.Client{Timeout: codexFingerprintFetchTimeout}
	}

	commitPayload, errFetch := fetchCodexFingerprintSource(ctx, client, sources.CommitURL, "commit source")
	if errFetch != nil {
		return CodexFingerprintProfile{}, errFetch
	}
	var commit map[string]any
	if errJSON := json.Unmarshal(commitPayload, &commit); errJSON != nil {
		return CodexFingerprintProfile{}, fmt.Errorf("decode Codex commit source: %w", errJSON)
	}
	sourceRevision, _ := commit["sha"].(string)
	sourceRevision = strings.TrimSpace(sourceRevision)
	if sourceRevision == "" {
		return CodexFingerprintProfile{}, fmt.Errorf("Codex commit source is missing sha")
	}
	sources = pinCodexFingerprintSourcesToRevision(sources, sourceRevision)

	distTags, errFetch := fetchCodexFingerprintSource(ctx, client, sources.DistTagsURL, "NPM dist-tags")
	if errFetch != nil {
		return CodexFingerprintProfile{}, errFetch
	}
	var tags map[string]string
	if errJSON := json.Unmarshal(distTags, &tags); errJSON != nil {
		return CodexFingerprintProfile{}, fmt.Errorf("decode Codex NPM dist-tags: %w", errJSON)
	}
	version := strings.TrimSpace(tags["latest"])
	if _, errVersion := parseCodexReleaseVersion(version); errVersion != nil {
		return CodexFingerprintProfile{}, errVersion
	}
	if comparison, errCompare := compareCodexReleaseVersions(version, base.Version); errCompare != nil {
		return CodexFingerprintProfile{}, errCompare
	} else if comparison < 0 {
		return CodexFingerprintProfile{}, fmt.Errorf("Codex fingerprint version downgrade %s -> %s rejected", base.Version, version)
	}

	defaultClient, errFetch := fetchCodexFingerprintSource(ctx, client, sources.DefaultClientURL, "default client source")
	if errFetch != nil {
		return CodexFingerprintProfile{}, errFetch
	}
	originator, errConstant := extractRustStringConstant(defaultClient, "DEFAULT_ORIGINATOR")
	if errConstant != nil {
		return CodexFingerprintProfile{}, errConstant
	}
	if !strings.Contains(string(defaultClient), "get_codex_user_agent") ||
		!strings.Contains(string(defaultClient), "CARGO_PKG_VERSION") {
		return CodexFingerprintProfile{}, fmt.Errorf("default client source is missing the official User-Agent contract")
	}

	clientSource, errFetch := fetchCodexFingerprintSource(ctx, client, sources.ClientURL, "client source")
	if errFetch != nil {
		return CodexFingerprintProfile{}, errFetch
	}
	installationHeader, errConstant := extractRustStringConstant(clientSource, "X_CODEX_INSTALLATION_ID_HEADER")
	if errConstant != nil {
		return CodexFingerprintProfile{}, errConstant
	}
	turnStateHeader, errConstant := extractRustStringConstant(clientSource, "X_CODEX_TURN_STATE_HEADER")
	if errConstant != nil {
		return CodexFingerprintProfile{}, errConstant
	}
	turnMetadataHeader, errConstant := extractRustStringConstant(clientSource, "X_CODEX_TURN_METADATA_HEADER")
	if errConstant != nil {
		return CodexFingerprintProfile{}, errConstant
	}
	parentThreadHeader, errConstant := extractRustStringConstant(clientSource, "X_CODEX_PARENT_THREAD_ID_HEADER")
	if errConstant != nil {
		return CodexFingerprintProfile{}, errConstant
	}
	windowHeader, errConstant := extractRustStringConstant(clientSource, "X_CODEX_WINDOW_ID_HEADER")
	if errConstant != nil {
		return CodexFingerprintProfile{}, errConstant
	}
	subagentHeader, errConstant := extractRustStringConstant(clientSource, "X_OPENAI_SUBAGENT_HEADER")
	if errConstant != nil {
		return CodexFingerprintProfile{}, errConstant
	}
	timingHeader, errConstant := extractRustStringConstant(clientSource, "X_RESPONSESAPI_INCLUDE_TIMING_METRICS_HEADER")
	if errConstant != nil {
		return CodexFingerprintProfile{}, errConstant
	}
	websocketBeta, errConstant := extractRustStringConstant(clientSource, "RESPONSES_WEBSOCKETS_V2_BETA_HEADER_VALUE")
	if errConstant != nil {
		return CodexFingerprintProfile{}, errConstant
	}
	if !strings.Contains(string(clientSource), "\"x-client-request-id\"") {
		return CodexFingerprintProfile{}, fmt.Errorf("client source is missing x-client-request-id")
	}

	metadataSource, errFetch := fetchCodexFingerprintSource(ctx, client, sources.ResponsesMetadataURL, "responses metadata source")
	if errFetch != nil {
		return CodexFingerprintProfile{}, errFetch
	}
	metadataNames := []string{
		"INSTALLATION_ID_KEY",
		"SESSION_ID_KEY",
		"THREAD_ID_KEY",
		"TURN_ID_KEY",
		"WINDOW_ID_KEY",
		"REQUEST_KIND_KEY",
		"TURN_STARTED_AT_UNIX_MS_KEY",
		"PARENT_THREAD_ID_KEY",
		"PARENT_TURN_ID_KEY",
		"SUBAGENT_KIND_KEY",
	}
	metadataValues := make(map[string]string, len(metadataNames))
	for _, name := range metadataNames {
		value, errMetadata := extractRustStringConstant(metadataSource, name)
		if errMetadata != nil {
			return CodexFingerprintProfile{}, errMetadata
		}
		metadataValues[name] = value
	}

	requestHeadersSource, errFetch := fetchCodexFingerprintSource(ctx, client, sources.RequestHeadersURL, "request headers source")
	if errFetch != nil {
		return CodexFingerprintProfile{}, errFetch
	}
	sessionHeader, errHeader := extractRustSessionHeader(requestHeadersSource, "session_id")
	if errHeader != nil {
		return CodexFingerprintProfile{}, errHeader
	}
	threadHeader, errHeader := extractRustSessionHeader(requestHeadersSource, "thread_id")
	if errHeader != nil {
		return CodexFingerprintProfile{}, errHeader
	}

	candidate := base
	candidate.SourceRevision = sourceRevision
	candidate.Version = version
	candidate.Originator = originator
	candidate.WebsocketBeta = websocketBeta
	candidate.Headers = CodexFingerprintHeaders{
		InstallationID:  installationHeader,
		TurnState:       turnStateHeader,
		TurnMetadata:    turnMetadataHeader,
		ParentThreadID:  parentThreadHeader,
		WindowID:        windowHeader,
		Subagent:        subagentHeader,
		TimingMetrics:   timingHeader,
		ClientRequestID: "x-client-request-id",
		SessionID:       sessionHeader,
		ThreadID:        threadHeader,
	}
	candidate.MetadataKeys = CodexFingerprintMetadataKeys{
		InstallationID:      metadataValues["INSTALLATION_ID_KEY"],
		SessionID:           metadataValues["SESSION_ID_KEY"],
		ThreadID:            metadataValues["THREAD_ID_KEY"],
		TurnID:              metadataValues["TURN_ID_KEY"],
		WindowID:            metadataValues["WINDOW_ID_KEY"],
		RequestKind:         metadataValues["REQUEST_KIND_KEY"],
		TurnStartedAtUnixMS: metadataValues["TURN_STARTED_AT_UNIX_MS_KEY"],
		ParentThreadID:      metadataValues["PARENT_THREAD_ID_KEY"],
		ParentTurnID:        metadataValues["PARENT_TURN_ID_KEY"],
		SubagentKind:        metadataValues["SUBAGENT_KIND_KEY"],
	}
	if errValidate := validateCodexFingerprintProfile(candidate); errValidate != nil {
		return CodexFingerprintProfile{}, errValidate
	}
	return candidate, nil
}

func fetchCodexFingerprintSource(ctx context.Context, client *http.Client, sourceURL, label string) ([]byte, error) {
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(sourceURL), nil)
	if errRequest != nil {
		return nil, fmt.Errorf("create %s request: %w", label, errRequest)
	}
	req.Header.Set("Accept", "application/json,text/plain")
	req.Header.Set("User-Agent", "CLIProxyAPI-Codex-Fingerprint")
	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("fetch %s: %w", label, errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("close %s response failed: %v", label, errClose)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s returned %s", label, resp.Status)
	}
	data, errRead := io.ReadAll(io.LimitReader(resp.Body, maxCodexFingerprintSourceSize+1))
	if errRead != nil {
		return nil, fmt.Errorf("read %s: %w", label, errRead)
	}
	if len(data) > maxCodexFingerprintSourceSize {
		return nil, fmt.Errorf("%s exceeded %d bytes", label, maxCodexFingerprintSourceSize)
	}
	return data, nil
}

func extractRustStringConstant(source []byte, name string) (string, error) {
	pattern := "(?m)(?:pub(?:\\(crate\\))?\\s+)?const\\s+" + regexp.QuoteMeta(name) + "\\s*:\\s*&str\\s*=\\s*\"([^\"]+)\"\\s*;"
	matches := regexp.MustCompile(pattern).FindSubmatch(source)
	if len(matches) != 2 {
		return "", fmt.Errorf("official Codex source is missing %s", name)
	}
	value := strings.TrimSpace(string(matches[1]))
	if value == "" {
		return "", fmt.Errorf("official Codex source constant %s is empty", name)
	}
	return value, nil
}

func extractRustSessionHeader(source []byte, variable string) (string, error) {
	pattern := "(?s)if\\s+let\\s+Some\\(id\\)\\s*=\\s*" + regexp.QuoteMeta(variable) +
		"\\s*\\{.*?insert_header\\(\\s*&mut\\s+headers\\s*,\\s*\"([^\"]+)\"\\s*,\\s*&id\\s*\\)"
	matches := regexp.MustCompile(pattern).FindSubmatch(source)
	if len(matches) != 2 {
		return "", fmt.Errorf("official Codex request headers source is missing %s", variable)
	}
	value := strings.TrimSpace(string(matches[1]))
	if value == "" {
		return "", fmt.Errorf("official Codex request header for %s is empty", variable)
	}
	return value, nil
}

func pinCodexFingerprintSourcesToRevision(sources codexFingerprintSourceSet, revision string) codexFingerprintSourceSet {
	sources.DefaultClientURL = pinCodexFingerprintSourceURL(sources.DefaultClientURL, revision)
	sources.ClientURL = pinCodexFingerprintSourceURL(sources.ClientURL, revision)
	sources.ResponsesMetadataURL = pinCodexFingerprintSourceURL(sources.ResponsesMetadataURL, revision)
	sources.RequestHeadersURL = pinCodexFingerprintSourceURL(sources.RequestHeadersURL, revision)
	return sources
}

func pinCodexFingerprintSourceURL(sourceURL, revision string) string {
	parsed, errParse := url.Parse(strings.TrimSpace(sourceURL))
	if errParse != nil || !strings.EqualFold(parsed.Hostname(), "raw.githubusercontent.com") {
		return sourceURL
	}
	parts := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
	if len(parts) < 4 || parts[0] != "openai" || parts[1] != "codex" || parts[2] != "main" {
		return sourceURL
	}
	parts[2] = strings.TrimSpace(revision)
	parsed.Path = "/" + strings.Join(parts, "/")
	return parsed.String()
}
