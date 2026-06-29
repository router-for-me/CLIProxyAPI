package pluginstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

const (
	DefaultRegistryURL = "https://raw.githubusercontent.com/router-for-me/CLIProxyAPI-Plugins-Store/main/registry.json"
	DefaultSourceID    = "official"
	DefaultSourceName  = "Official"
	SchemaVersion      = 1
	SchemaVersionV2    = 2

	InstallTypeGitHubRelease = "github-release"
	InstallTypeDirect        = "direct"
)

var pluginVersionPattern = regexp.MustCompile(`^[0-9][0-9A-Za-z.+-]*$`)
var pluginIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type Source struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

type Registry struct {
	SchemaVersion int      `json:"schema_version"`
	Plugins       []Plugin `json:"plugins"`
}

type Plugin struct {
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	Description  string      `json:"description"`
	Author       string      `json:"author"`
	Version      string      `json:"version"`
	Versions     []Version   `json:"versions,omitempty"`
	Repository   string      `json:"repository,omitempty"`
	Logo         string      `json:"logo,omitempty"`
	Homepage     string      `json:"homepage,omitempty"`
	License      string      `json:"license,omitempty"`
	Tags         []string    `json:"tags,omitempty"`
	Install      InstallPlan `json:"install,omitempty"`
	AuthRequired bool        `json:"auth_required,omitempty"`
}

type Version struct {
	Version string      `json:"version"`
	Install InstallPlan `json:"install,omitempty"`
}

type InstallPlan struct {
	Type      string     `yaml:"type,omitempty" json:"type,omitempty"`
	Artifacts []Artifact `yaml:"artifacts,omitempty" json:"artifacts,omitempty"`
}

type Artifact struct {
	GOOS   string `yaml:"goos,omitempty" json:"goos,omitempty"`
	GOARCH string `yaml:"goarch,omitempty" json:"goarch,omitempty"`
	URL    string `yaml:"url,omitempty" json:"url,omitempty"`
	SHA256 string `yaml:"sha256,omitempty" json:"sha256,omitempty"`
	Size   int64  `yaml:"size,omitempty" json:"size,omitempty"`
}

type Platform struct {
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
}

func DefaultSource() Source {
	return Source{
		ID:   DefaultSourceID,
		Name: DefaultSourceName,
		URL:  DefaultRegistryURL,
	}
}

func NormalizeSources(registryURLs []string) ([]Source, error) {
	out := []Source{DefaultSource()}
	seenIDs := map[string]string{DefaultSourceID: DefaultRegistryURL}
	seenURLs := map[string]struct{}{DefaultRegistryURL: {}}
	for _, registryURL := range registryURLs {
		registryURL = strings.TrimSpace(registryURL)
		if registryURL == "" {
			continue
		}
		if _, exists := seenURLs[registryURL]; exists {
			continue
		}
		source := Source{
			ID:   SourceID(registryURL),
			Name: SourceName(registryURL),
			URL:  registryURL,
		}
		if existingURL, exists := seenIDs[source.ID]; exists {
			return nil, fmt.Errorf("plugin store source id collision for %q and %q", existingURL, registryURL)
		}
		seenIDs[source.ID] = registryURL
		seenURLs[registryURL] = struct{}{}
		out = append(out, source)
	}
	return out, nil
}

func SourceID(registryURL string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(registryURL)))
	return "source-" + hex.EncodeToString(sum[:])[:12]
}

func SourceName(registryURL string) string {
	parsed, errParse := url.Parse(strings.TrimSpace(registryURL))
	if errParse != nil || strings.TrimSpace(parsed.Host) == "" {
		return strings.TrimSpace(registryURL)
	}
	return parsed.Host
}

func ParseRegistry(data []byte) (Registry, error) {
	var registry Registry
	decoder := json.NewDecoder(bytes.NewReader(data))
	if errDecode := decoder.Decode(&registry); errDecode != nil {
		return Registry{}, fmt.Errorf("decode registry: %w", errDecode)
	}
	normalizeRegistry(&registry)
	if errValidate := ValidateRegistry(registry); errValidate != nil {
		return Registry{}, errValidate
	}
	return registry, nil
}

func normalizeRegistry(registry *Registry) {
	if registry == nil {
		return
	}
	for index := range registry.Plugins {
		plugin := &registry.Plugins[index]
		plugin.ID = strings.TrimSpace(plugin.ID)
		plugin.Name = strings.TrimSpace(plugin.Name)
		plugin.Description = strings.TrimSpace(plugin.Description)
		plugin.Author = strings.TrimSpace(plugin.Author)
		plugin.Version = strings.TrimSpace(plugin.Version)
		plugin.Repository = strings.TrimSpace(plugin.Repository)
		plugin.Logo = strings.TrimSpace(plugin.Logo)
		plugin.Homepage = strings.TrimSpace(plugin.Homepage)
		plugin.License = strings.TrimSpace(plugin.License)
		plugin.Install = NormalizeInstallPlan(plugin.Install)
		for versionIndex := range plugin.Versions {
			version := &plugin.Versions[versionIndex]
			version.Version = normalizeVersion(version.Version)
			version.Install = NormalizeInstallPlan(version.Install)
		}
		for tagIndex := range plugin.Tags {
			plugin.Tags[tagIndex] = strings.TrimSpace(plugin.Tags[tagIndex])
		}
	}
}

func ValidateRegistry(registry Registry) error {
	if registry.SchemaVersion != SchemaVersion && registry.SchemaVersion != SchemaVersionV2 {
		return fmt.Errorf("unsupported schema_version %d", registry.SchemaVersion)
	}
	seen := make(map[string]struct{}, len(registry.Plugins))
	for index, plugin := range registry.Plugins {
		if registry.SchemaVersion == SchemaVersion && PluginInstallType(plugin) == InstallTypeDirect {
			return fmt.Errorf("plugins[%d]: direct install requires schema_version %d", index, SchemaVersionV2)
		}
		if errValidate := ValidatePlugin(plugin); errValidate != nil {
			return fmt.Errorf("plugins[%d]: %w", index, errValidate)
		}
		id := strings.TrimSpace(plugin.ID)
		if _, exists := seen[id]; exists {
			return fmt.Errorf("plugins[%d]: duplicate plugin id %q", index, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func ValidatePlugin(plugin Plugin) error {
	required := map[string]string{
		"id":          plugin.ID,
		"name":        plugin.Name,
		"description": plugin.Description,
		"author":      plugin.Author,
	}
	installType := PluginInstallType(plugin)
	if installType == InstallTypeGitHubRelease {
		required["repository"] = plugin.Repository
	}
	for field, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("missing required field %s", field)
		}
	}
	if !validPluginID(strings.TrimSpace(plugin.ID)) {
		return fmt.Errorf("invalid plugin id %q", plugin.ID)
	}
	// The version is optional since the latest release is the source of truth;
	// when present it is only used as a display fallback and must be valid.
	if version := strings.TrimSpace(plugin.Version); version != "" && !validPluginVersion(version) {
		return fmt.Errorf("invalid plugin version %q", plugin.Version)
	}
	switch installType {
	case InstallTypeGitHubRelease:
		if _, _, errRepository := GitHubRepositoryParts(plugin.Repository); errRepository != nil {
			return errRepository
		}
	case InstallTypeDirect:
		if strings.TrimSpace(plugin.Version) == "" {
			return fmt.Errorf("missing required field version")
		}
		if errPlan := ValidateInstallPlan(plugin.Install); errPlan != nil {
			return errPlan
		}
		if errVersions := ValidatePluginVersions(plugin); errVersions != nil {
			return errVersions
		}
	default:
		return fmt.Errorf("unsupported install type %q", plugin.Install.Type)
	}
	return nil
}

func ValidatePluginVersions(plugin Plugin) error {
	if len(plugin.Versions) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(plugin.Versions))
	for index, version := range plugin.Versions {
		version.Version = normalizeVersion(version.Version)
		if !validPluginVersion(version.Version) {
			return fmt.Errorf("versions[%d]: invalid plugin version %q", index, version.Version)
		}
		if _, exists := seen[version.Version]; exists {
			return fmt.Errorf("versions[%d]: duplicate plugin version %q", index, version.Version)
		}
		seen[version.Version] = struct{}{}
		installType := strings.ToLower(strings.TrimSpace(version.Install.Type))
		if installType == "" {
			installType = PluginInstallType(plugin)
			version.Install.Type = installType
		}
		if installType != PluginInstallType(plugin) {
			return fmt.Errorf("versions[%d]: install type %q does not match plugin install type %q", index, installType, PluginInstallType(plugin))
		}
		if errPlan := ValidateInstallPlan(version.Install); errPlan != nil {
			return fmt.Errorf("versions[%d]: %w", index, errPlan)
		}
	}
	return nil
}

func PluginInstallType(plugin Plugin) string {
	installType := strings.ToLower(strings.TrimSpace(plugin.Install.Type))
	if installType == "" {
		return InstallTypeGitHubRelease
	}
	return installType
}

func NormalizeInstallPlan(plan InstallPlan) InstallPlan {
	plan.Type = strings.ToLower(strings.TrimSpace(plan.Type))
	for index := range plan.Artifacts {
		artifact := &plan.Artifacts[index]
		artifact.GOOS = normalizeGOOS(artifact.GOOS)
		artifact.GOARCH = normalizeGOARCH(artifact.GOARCH)
		artifact.URL = strings.TrimSpace(artifact.URL)
		artifact.SHA256 = strings.ToLower(strings.TrimSpace(artifact.SHA256))
	}
	return plan
}

func ValidateInstallPlan(plan InstallPlan) error {
	plan = NormalizeInstallPlan(plan)
	if plan.Type == "" {
		return fmt.Errorf("missing install type")
	}
	if plan.Type != InstallTypeDirect && plan.Type != InstallTypeGitHubRelease {
		return fmt.Errorf("unsupported install type %q", plan.Type)
	}
	if plan.Type != InstallTypeDirect {
		return nil
	}
	if len(plan.Artifacts) == 0 {
		return fmt.Errorf("direct install requires at least one artifact")
	}
	for index, artifact := range plan.Artifacts {
		if errArtifact := ValidateArtifact(artifact); errArtifact != nil {
			return fmt.Errorf("artifacts[%d]: %w", index, errArtifact)
		}
	}
	return nil
}

func ValidateArtifact(artifact Artifact) error {
	artifact.GOOS = normalizeGOOS(artifact.GOOS)
	artifact.GOARCH = normalizeGOARCH(artifact.GOARCH)
	artifact.URL = strings.TrimSpace(artifact.URL)
	artifact.SHA256 = strings.ToLower(strings.TrimSpace(artifact.SHA256))
	if artifact.GOOS == "" {
		return fmt.Errorf("missing goos")
	}
	if artifact.GOARCH == "" {
		return fmt.Errorf("missing goarch")
	}
	if artifact.URL == "" {
		return fmt.Errorf("missing url")
	}
	parsed, errParse := url.Parse(artifact.URL)
	if errParse != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("invalid artifact url")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return fmt.Errorf("artifact url must use http or https")
	}
	if hasSensitiveQueryParameter(parsed) {
		return fmt.Errorf("artifact url contains sensitive query parameter")
	}
	if artifact.SHA256 == "" {
		return fmt.Errorf("missing sha256")
	}
	if len(artifact.SHA256) != sha256.Size*2 {
		return fmt.Errorf("invalid sha256 length")
	}
	if _, errDecode := hex.DecodeString(artifact.SHA256); errDecode != nil {
		return fmt.Errorf("invalid sha256: %w", errDecode)
	}
	if artifact.Size < 0 {
		return fmt.Errorf("invalid size")
	}
	return nil
}

func PluginPlatforms(plugin Plugin) []Platform {
	if PluginInstallType(plugin) != InstallTypeDirect {
		return nil
	}
	artifacts := PluginArtifacts(plugin)
	seen := make(map[Platform]struct{}, len(artifacts))
	platforms := make([]Platform, 0, len(artifacts))
	for _, artifact := range artifacts {
		platform := Platform{GOOS: artifact.GOOS, GOARCH: artifact.GOARCH}
		if platform.GOOS == "" || platform.GOARCH == "" {
			continue
		}
		if _, exists := seen[platform]; exists {
			continue
		}
		seen[platform] = struct{}{}
		platforms = append(platforms, platform)
	}
	return platforms
}

func PluginArtifacts(plugin Plugin) []Artifact {
	if PluginInstallType(plugin) != InstallTypeDirect {
		return nil
	}
	artifacts := append([]Artifact(nil), NormalizeInstallPlan(plugin.Install).Artifacts...)
	for _, version := range plugin.Versions {
		artifacts = append(artifacts, NormalizeInstallPlan(version.Install).Artifacts...)
	}
	return artifacts
}

func normalizeGOOS(goos string) string {
	switch strings.ToLower(strings.TrimSpace(goos)) {
	case "mac", "macos", "osx":
		return "darwin"
	default:
		return strings.ToLower(strings.TrimSpace(goos))
	}
}

func normalizeGOARCH(goarch string) string {
	switch strings.ToLower(strings.TrimSpace(goarch)) {
	case "x64", "x86_64":
		return "amd64"
	case "aarch64":
		return "arm64"
	default:
		return strings.ToLower(strings.TrimSpace(goarch))
	}
}

func hasSensitiveQueryParameter(parsed *url.URL) bool {
	if parsed == nil || parsed.RawQuery == "" {
		return false
	}
	for key := range parsed.Query() {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "token", "access_token", "access_key", "secret", "secret_key", "api_key":
			return true
		}
	}
	return false
}

func validPluginVersion(version string) bool {
	return version != "" && !strings.HasPrefix(version, "v") && pluginVersionPattern.MatchString(version)
}

func validPluginID(id string) bool {
	return pluginIDPattern.MatchString(id)
}

func GitHubRepositoryParts(repository string) (string, string, error) {
	repository = strings.TrimSpace(repository)
	parsed, errParse := url.Parse(repository)
	if errParse != nil {
		return "", "", fmt.Errorf("invalid repository URL: %w", errParse)
	}
	if parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", fmt.Errorf("repository must be https://github.com/{owner}/{repo}")
	}
	segments := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(segments) != 2 || segments[0] == "" || segments[1] == "" {
		return "", "", fmt.Errorf("repository must be https://github.com/{owner}/{repo}")
	}
	owner, errOwner := url.PathUnescape(segments[0])
	if errOwner != nil {
		return "", "", fmt.Errorf("invalid repository owner: %w", errOwner)
	}
	repo, errRepo := url.PathUnescape(segments[1])
	if errRepo != nil {
		return "", "", fmt.Errorf("invalid repository name: %w", errRepo)
	}
	if strings.HasSuffix(repo, ".git") {
		return "", "", fmt.Errorf("repository must be https://github.com/{owner}/{repo}")
	}
	return owner, repo, nil
}

func (r Registry) PluginByID(id string) (Plugin, bool) {
	id = strings.TrimSpace(id)
	for _, plugin := range r.Plugins {
		if strings.TrimSpace(plugin.ID) == id {
			return plugin, true
		}
	}
	return Plugin{}, false
}
