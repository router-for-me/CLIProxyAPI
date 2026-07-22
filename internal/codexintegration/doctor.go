package codexintegration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// DoctorSeverity controls both human display and process exit status.
type DoctorSeverity string

const (
	DoctorInfo     DoctorSeverity = "info"
	DoctorWarning  DoctorSeverity = "warning"
	DoctorBlocking DoctorSeverity = "blocking"
)

// DoctorCheck is a stable, actionable diagnostic item.
type DoctorCheck struct {
	Layer       string         `json:"layer"`
	Code        string         `json:"code"`
	Severity    DoctorSeverity `json:"severity"`
	Message     string         `json:"message"`
	Remediation string         `json:"remediation,omitempty"`
}

// DoctorReport is the machine-readable doctor result.
type DoctorReport struct {
	SchemaVersion int           `json:"schema_version"`
	Status        string        `json:"status"`
	Checks        []DoctorCheck `json:"checks"`
}

// ExitCode returns 0 for clean, 1 for warning, and 2 for blocking reports.
func (report DoctorReport) ExitCode() int {
	exitCode := 0
	for _, check := range report.Checks {
		switch check.Severity {
		case DoctorBlocking:
			return 2
		case DoctorWarning:
			exitCode = 1
		}
	}
	return exitCode
}

// DoctorOptions controls local endpoint and explicit upstream probes.
type DoctorOptions struct {
	ProbeModels        bool
	HTTPClient         *http.Client
	OpenCodexAddress   string
	SkipOpenCodexCheck bool
}

// RunDoctor performs read-only checks. Upstream requests require ProbeModels.
func RunDoctor(ctx context.Context, lifecycle *Lifecycle, models []map[string]any, providers ModelProvidersFunc, options DoctorOptions) DoctorReport {
	checks := make([]DoctorCheck, 0, 24)
	add := func(layer, code string, severity DoctorSeverity, message, remediation string) {
		checks = append(checks, DoctorCheck{Layer: layer, Code: code, Severity: severity, Message: message, Remediation: remediation})
	}

	addConfigChecks(lifecycle, &checks, add)
	addListenerChecks(ctx, lifecycle, options, add)
	addCatalogChecks(lifecycle, models, providers, add)
	addCacheChecks(lifecycle, add)
	addAuthChecks(lifecycle, add)
	addEndpointCheck(ctx, lifecycle, options.HTTPClient, add)
	if !options.SkipOpenCodexCheck {
		address := strings.TrimSpace(options.OpenCodexAddress)
		if address == "" {
			address = "127.0.0.1:10100"
		}
		if canDial(ctx, address, 300*time.Millisecond) {
			add("process", "opencodex.listener_detected", DoctorWarning, "A listener is active on the default OpenCodex port.", "Stop OpenCodex after CLIProxyAPI cutover is verified.")
		} else {
			add("process", "opencodex.listener_absent", DoctorInfo, "No listener is active on the default OpenCodex port.", "")
		}
	}
	if options.ProbeModels {
		addModelProbes(ctx, lifecycle, options.HTTPClient, add)
	}

	report := DoctorReport{SchemaVersion: 1, Checks: checks}
	switch report.ExitCode() {
	case 0:
		report.Status = "clean"
	case 1:
		report.Status = "warning"
	default:
		report.Status = "blocking"
	}
	return report
}

func addConfigChecks(lifecycle *Lifecycle, checks *[]DoctorCheck, add func(string, string, DoctorSeverity, string, string)) {
	data, mode, exists, err := readRegularFile(lifecycle.Paths.ConfigFile, 0o600)
	if err != nil {
		add("config", "config.unreadable", DoctorBlocking, "Codex config cannot be read safely.", "Remove symlinks or repair file permissions, then rerun doctor.")
		return
	}
	if !exists {
		add("config", "config.missing", DoctorBlocking, "Codex config is missing.", "Run -codex-setup, review the preview, then rerun with -apply.")
		return
	}
	if mode.Perm()&0o077 != 0 {
		add("config", "config.permissions", DoctorWarning, "Codex config is readable by group or other users.", "Restrict the file permissions to the previous mode or 0600.")
	}
	normalized := strings.ReplaceAll(string(data), "\r\n", "\n")
	_, managed, markerErr := removeManagedBlock(normalized)
	if markerErr != nil {
		add("config", "config.marker_malformed", DoctorBlocking, "The CLIProxyAPI managed marker block is malformed.", "Restore the marker pair from backup before setup or restore.")
	} else if managed {
		add("config", "config.managed", DoctorInfo, "Codex config contains the CLIProxyAPI managed root block.", "")
	} else {
		add("config", "config.unmanaged", DoctorBlocking, "Codex config is not managed by CLIProxyAPI.", "Run -codex-setup and apply the reviewed change.")
	}
	if strings.Contains(normalized, openCodexMarker) {
		add("config", "config.opencodex_marker", DoctorWarning, "Codex config still contains an OpenCodex injection marker.", "Use -codex-setup -codex-migrate-opencodex -apply during the migration window.")
	}
	_ = checks
}

func addListenerChecks(ctx context.Context, lifecycle *Lifecycle, options DoctorOptions, add func(string, string, DoctorSeverity, string, string)) {
	if !lifecycle.Config.CodexIntegration.LoopbackAccess {
		add("access", "access.loopback_disabled", DoctorBlocking, "Loopback bearer access is disabled.", "Set codex-integration.loopback-access to true.")
	} else {
		add("access", "access.loopback_enabled", DoctorInfo, "Loopback bearer access is enabled for data-plane routes.", "")
	}
	address := net.JoinHostPort(strings.Trim(lifecycle.Config.Host, "[]"), fmt.Sprintf("%d", lifecycle.Config.Port))
	if canDial(ctx, address, 500*time.Millisecond) {
		add("listener", "listener.ready", DoctorInfo, "CLIProxyAPI is accepting loopback connections.", "")
	} else {
		add("listener", "listener.unavailable", DoctorBlocking, "CLIProxyAPI is not accepting connections at the configured loopback address.", "Start or restart CLIProxyAPI with the integration-enabled config.")
	}
}

func addCatalogChecks(lifecycle *Lifecycle, models []map[string]any, providers ModelProvidersFunc, add func(string, string, DoctorSeverity, string, string)) {
	data, mode, exists, err := readRegularFile(lifecycle.Paths.CatalogFile, 0o600)
	if err != nil {
		add("catalog", "catalog.unreadable", DoctorBlocking, "The managed model catalog cannot be read safely.", "Remove symlinks or repair permissions, then run -codex-sync.")
		return
	}
	if !exists {
		add("catalog", "catalog.missing", DoctorBlocking, "The managed model catalog is missing.", "Run -codex-setup -apply.")
		return
	}
	if mode.Perm()&0o077 != 0 {
		add("catalog", "catalog.permissions", DoctorWarning, "The managed catalog is readable by group or other users.", "Restrict catalog permissions to 0600.")
	}
	if err = registry.ValidateCodexClientModelsJSON(data); err != nil {
		add("catalog", "catalog.invalid", DoctorBlocking, "The managed model catalog is invalid.", "Run -codex-sync; if it fails, keep the last-good catalog and inspect logs.")
		return
	}
	if !hasExpectedFeaturedModels(data) {
		add("catalog", "catalog.featured_mismatch", DoctorBlocking, "The catalog does not contain the required five featured models in order.", "Run -codex-sync after all mapped upstream models are available.")
		return
	}
	expected, compileErr := CompileCatalog(models, providers, lifecycle.Config.CodexIntegration)
	if compileErr != nil {
		add("catalog", "catalog.source_unavailable", DoctorBlocking, "At least one configured upstream model is unavailable from the current model source.", "Start CLIProxyAPI, verify provider credentials and model IDs, then rerun doctor.")
		return
	}
	expectedData, marshalErr := expected.Marshal()
	if marshalErr != nil {
		add("catalog", "catalog.compile_failed", DoctorBlocking, "The expected catalog could not be encoded.", "Inspect CLIProxyAPI logs and configuration.")
		return
	}
	if !bytes.Equal(data, expectedData) {
		add("catalog", "catalog.stale", DoctorWarning, "The managed catalog differs from the current model and mapping revisions.", "Run -codex-sync, review the preview, then apply it.")
	} else {
		add("catalog", "catalog.current", DoctorInfo, "The managed catalog matches the current model and mapping revisions.", "")
	}
}

func hasExpectedFeaturedModels(data []byte) bool {
	var payload struct {
		Models []map[string]any `json:"models"`
	}
	if json.Unmarshal(data, &payload) != nil || len(payload.Models) < 5 {
		return false
	}
	want := []string{
		"gpt-5.6-sol",
		"xai/grok-4.5",
		"antigravity/gemini-3.6-flash",
		"antigravity/gemini-3.1-pro",
		"antigravity/claude-opus-4-6-thinking",
	}
	for index, slug := range want {
		if current, _ := payload.Models[index]["slug"].(string); current != slug {
			return false
		}
	}
	return true
}

func addCacheChecks(lifecycle *Lifecycle, add func(string, string, DoctorSeverity, string, string)) {
	cacheInfo, cacheErr := os.Lstat(lifecycle.Paths.CacheFile)
	if os.IsNotExist(cacheErr) {
		add("cache", "cache.absent", DoctorInfo, "Codex has no model cache file; it will rebuild it on launch.", "")
		return
	}
	if cacheErr != nil || cacheInfo.Mode()&os.ModeSymlink != 0 {
		add("cache", "cache.unsafe", DoctorBlocking, "The Codex model cache path is unsafe or unreadable.", "Remove the cache symlink or repair the path before restarting Codex.")
		return
	}
	catalogInfo, err := os.Stat(lifecycle.Paths.CatalogFile)
	if err == nil && cacheInfo.ModTime().Before(catalogInfo.ModTime()) {
		add("cache", "cache.restart_required", DoctorWarning, "The Codex model cache predates the managed catalog.", "Restart Codex to rebuild the model picker cache.")
	} else {
		add("cache", "cache.current", DoctorInfo, "The Codex model cache is newer than the managed catalog.", "")
	}
}

func addAuthChecks(lifecycle *Lifecycle, add func(string, string, DoctorSeverity, string, string)) {
	providers, warnings, blocking := scanAuthProviders(lifecycle.Config.AuthDir)
	for _, message := range blocking {
		add("oauth", "oauth.storage_unsafe", DoctorBlocking, message, "Repair auth directory ownership, permissions, or symlinks; do not copy token contents into logs.")
	}
	for _, message := range warnings {
		add("oauth", "oauth.storage_warning", DoctorWarning, message, "Restrict credential file permissions to 0600.")
	}
	required := map[string]bool{"codex": true}
	for _, mapping := range lifecycle.Config.CodexIntegration.Models {
		if mapping.Visible {
			required[mapping.Provider] = true
		}
	}
	if len(lifecycle.Config.CodexKey) > 0 {
		providers["codex"]++
	}
	if len(lifecycle.Config.XAIKey) > 0 {
		providers["xai"]++
	}
	names := make([]string, 0, len(required))
	for provider := range required {
		names = append(names, provider)
	}
	sort.Strings(names)
	for _, provider := range names {
		if providers[provider] == 0 {
			add("oauth", "oauth."+provider+"_missing", DoctorBlocking, "No credential metadata was found for provider "+provider+".", "Log in to "+provider+" through CLIProxyAPI, then rerun doctor.")
		} else {
			add("oauth", "oauth."+provider+"_present", DoctorInfo, "Credential metadata is present for provider "+provider+".", "")
		}
	}
}

func scanAuthProviders(authDir string) (map[string]int, []string, []string) {
	providers := make(map[string]int)
	entries, err := os.ReadDir(authDir)
	if err != nil {
		return providers, nil, []string{"The auth directory cannot be read."}
	}
	dirInfo, err := os.Lstat(authDir)
	if err != nil || dirInfo.Mode()&os.ModeSymlink != 0 {
		return providers, nil, []string{"The auth directory is a symbolic link or cannot be inspected safely."}
	}
	dirOwner, hasDirOwner := numericOwner(dirInfo)
	warnings := make([]string, 0)
	blocking := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		path := filepath.Join(authDir, entry.Name())
		info, statErr := os.Lstat(path)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			blocking = append(blocking, "An auth record is a symbolic link or non-regular file.")
			continue
		}
		if owner, ok := numericOwner(info); hasDirOwner && ok && owner != dirOwner {
			blocking = append(blocking, "An auth record has a different owner from the auth directory.")
			continue
		}
		if info.Mode().Perm()&0o077 != 0 {
			warnings = append(warnings, "An auth record is readable by group or other users.")
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			blocking = append(blocking, "An auth record cannot be read.")
			continue
		}
		var metadata struct {
			Type     string `json:"type"`
			Provider string `json:"provider"`
		}
		if json.Unmarshal(data, &metadata) != nil {
			warnings = append(warnings, "An auth record is not valid JSON metadata.")
			continue
		}
		provider := strings.ToLower(strings.TrimSpace(metadata.Type))
		if provider == "" {
			provider = strings.ToLower(strings.TrimSpace(metadata.Provider))
		}
		if provider != "" {
			providers[provider]++
		}
	}
	return providers, deduplicateStrings(warnings), deduplicateStrings(blocking)
}

func numericOwner(info os.FileInfo) (uint64, bool) {
	if info == nil || info.Sys() == nil {
		return 0, false
	}
	value := reflect.ValueOf(info.Sys())
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return 0, false
	}
	uid := value.FieldByName("Uid")
	if !uid.IsValid() || !uid.CanUint() {
		return 0, false
	}
	return uid.Uint(), true
}

func addEndpointCheck(ctx context.Context, lifecycle *Lifecycle, client *http.Client, add func(string, string, DoctorSeverity, string, string)) {
	status, err := endpointRequest(ctx, client, http.MethodGet, strings.TrimSuffix(lifecycle.baseURL(), "/")+"/models", nil)
	if err != nil {
		add("endpoint", "endpoint.models_unreachable", DoctorBlocking, "The local /v1/models endpoint is unreachable.", "Start CLIProxyAPI and verify the configured port.")
		return
	}
	if status != http.StatusOK {
		add("endpoint", "endpoint.models_rejected", DoctorBlocking, fmt.Sprintf("The local /v1/models endpoint returned HTTP %d.", status), "Verify loopback access is enabled and restart CLIProxyAPI.")
		return
	}
	add("endpoint", "endpoint.models_ready", DoctorInfo, "The local /v1/models endpoint accepts loopback bearer access.", "")
}

func addModelProbes(ctx context.Context, lifecycle *Lifecycle, client *http.Client, add func(string, string, DoctorSeverity, string, string)) {
	targets := []string{"gpt-5.6-sol"}
	for _, mapping := range lifecycle.Config.CodexIntegration.Models {
		if mapping.Featured && mapping.Visible {
			targets = append(targets, mapping.Slug)
		}
	}
	for _, model := range targets {
		payload, _ := json.Marshal(map[string]any{
			"model": model, "input": "Reply with OK.", "max_output_tokens": 16, "stream": false,
		})
		status, err := endpointRequest(ctx, client, http.MethodPost, strings.TrimSuffix(lifecycle.baseURL(), "/")+"/responses", payload)
		codeModel := strings.NewReplacer("/", "_", ".", "_").Replace(model)
		if err != nil {
			add("probe", "probe."+codeModel+".timeout", DoctorBlocking, "The explicit model probe did not complete for "+model+".", "Check provider connectivity and retry the explicit probe.")
		} else if status < 200 || status >= 300 {
			add("probe", "probe."+codeModel+".failed", DoctorBlocking, fmt.Sprintf("The explicit model probe returned HTTP %d for %s.", status, model), "Refresh the provider credential or inspect quota and upstream status.")
		} else {
			add("probe", "probe."+codeModel+".ready", DoctorInfo, "The explicit model probe succeeded for "+model+".", "")
		}
	}
}

func endpointRequest(ctx context.Context, client *http.Client, method, url string, body []byte) (int, error) {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer codex-doctor-local")
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	_ = response.Body.Close()
	return response.StatusCode, nil
}

func canDial(ctx context.Context, address string, timeout time.Duration) bool {
	dialer := net.Dialer{Timeout: timeout}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

func deduplicateStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func writeDoctorText(output io.Writer, report DoctorReport) error {
	if _, err := fmt.Fprintf(output, "Codex Integration doctor: %s\n", report.Status); err != nil {
		return err
	}
	for _, check := range report.Checks {
		if _, err := fmt.Fprintf(output, "[%s] %s: %s\n", check.Severity, check.Code, check.Message); err != nil {
			return err
		}
		if check.Remediation != "" {
			if _, err := fmt.Fprintf(output, "  fix: %s\n", check.Remediation); err != nil {
				return err
			}
		}
	}
	return nil
}
