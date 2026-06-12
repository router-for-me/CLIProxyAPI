package pluginstore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
)

const (
	DefaultRegistryURL = "https://raw.githubusercontent.com/router-for-me/CLIProxyAPI-Plugins-Store/main/registry.json"
	SchemaVersion      = 1
)

var pluginVersionPattern = regexp.MustCompile(`^[0-9][0-9A-Za-z.+-]*$`)

type Registry struct {
	SchemaVersion int      `json:"schema_version"`
	Plugins       []Plugin `json:"plugins"`
}

type Plugin struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Author      string   `json:"author"`
	Version     string   `json:"version"`
	Repository  string   `json:"repository"`
	Logo        string   `json:"logo,omitempty"`
	Homepage    string   `json:"homepage,omitempty"`
	License     string   `json:"license,omitempty"`
	Tags        []string `json:"tags,omitempty"`
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
		for tagIndex := range plugin.Tags {
			plugin.Tags[tagIndex] = strings.TrimSpace(plugin.Tags[tagIndex])
		}
	}
}

func ValidateRegistry(registry Registry) error {
	if registry.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema_version %d", registry.SchemaVersion)
	}
	seen := make(map[string]struct{}, len(registry.Plugins))
	for index, plugin := range registry.Plugins {
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
		"version":     plugin.Version,
		"repository":  plugin.Repository,
	}
	for field, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("missing required field %s", field)
		}
	}
	if !pluginhost.ValidatePluginID(strings.TrimSpace(plugin.ID)) {
		return fmt.Errorf("invalid plugin id %q", plugin.ID)
	}
	if !validPluginVersion(strings.TrimSpace(plugin.Version)) {
		return fmt.Errorf("invalid plugin version %q", plugin.Version)
	}
	if _, _, errRepository := GitHubRepositoryParts(plugin.Repository); errRepository != nil {
		return errRepository
	}
	return nil
}

func validPluginVersion(version string) bool {
	return version != "" && !strings.HasPrefix(version, "v") && pluginVersionPattern.MatchString(version)
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
