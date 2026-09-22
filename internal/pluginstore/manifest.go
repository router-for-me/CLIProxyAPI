package pluginstore

import (
	"fmt"
	"net/url"
	"strings"
)

type Manifest struct {
	SchemaVersion int         `yaml:"schema-version,omitempty" json:"schema_version,omitempty"`
	ID            string      `yaml:"id,omitempty" json:"id,omitempty"`
	Name          string      `yaml:"name,omitempty" json:"name,omitempty"`
	Description   string      `yaml:"description,omitempty" json:"description,omitempty"`
	Author        string      `yaml:"author,omitempty" json:"author,omitempty"`
	Version       string      `yaml:"version,omitempty" json:"version,omitempty"`
	ReleaseTag    string      `yaml:"release-tag,omitempty" json:"release_tag,omitempty"`
	Repository    string      `yaml:"repository,omitempty" json:"repository,omitempty"`
	Logo          string      `yaml:"logo,omitempty" json:"logo,omitempty"`
	Homepage      string      `yaml:"homepage,omitempty" json:"homepage,omitempty"`
	License       string      `yaml:"license,omitempty" json:"license,omitempty"`
	Tags          []string    `yaml:"tags,omitempty" json:"tags,omitempty"`
	SourceID      string      `yaml:"source-id,omitempty" json:"source_id,omitempty"`
	SourceName    string      `yaml:"source-name,omitempty" json:"source_name,omitempty"`
	SourceURL     string      `yaml:"source-url,omitempty" json:"source_url,omitempty"`
	Install       InstallPlan `yaml:"install,omitempty" json:"install,omitempty"`
}

func ManifestFromRelease(source Source, plugin Plugin, release Release) (Manifest, error) {
	version, errVersion := ReleaseVersion(release)
	if errVersion != nil {
		return Manifest{}, errVersion
	}
	return manifestFromPlugin(source, plugin, Manifest{
		Version:    version,
		ReleaseTag: strings.TrimSpace(release.TagName),
		Repository: strings.TrimSpace(plugin.Repository),
		Install:    InstallPlan{Type: InstallTypeGitHubRelease},
	}), nil
}

func ManifestFromPlugin(source Source, plugin Plugin) (Manifest, error) {
	if errValidate := ValidatePlugin(plugin); errValidate != nil {
		return Manifest{}, errValidate
	}
	switch PluginInstallType(plugin) {
	case InstallTypeDirect:
		manifest := manifestFromPlugin(source, plugin, Manifest{
			SchemaVersion: SchemaVersionV2,
			Version:       strings.TrimSpace(plugin.Version),
			Install:       NormalizeInstallPlan(plugin.Install),
		})
		if errValidate := manifest.Validate(); errValidate != nil {
			return Manifest{}, errValidate
		}
		return manifest, nil
	case InstallTypeGitHubRelease:
		return Manifest{}, fmt.Errorf("github-release manifest requires a resolved release")
	default:
		return Manifest{}, fmt.Errorf("unsupported install type %q", plugin.Install.Type)
	}
}

func manifestFromPlugin(source Source, plugin Plugin, base Manifest) Manifest {
	base.ID = strings.TrimSpace(plugin.ID)
	base.Name = strings.TrimSpace(plugin.Name)
	base.Description = strings.TrimSpace(plugin.Description)
	base.Author = strings.TrimSpace(plugin.Author)
	base.Logo = strings.TrimSpace(plugin.Logo)
	base.Homepage = strings.TrimSpace(plugin.Homepage)
	base.License = strings.TrimSpace(plugin.License)
	base.Tags = append([]string(nil), plugin.Tags...)
	base.SourceID = strings.TrimSpace(source.ID)
	base.SourceName = strings.TrimSpace(source.Name)
	base.SourceURL = strings.TrimSpace(source.URL)
	return base
}

func (m Manifest) Plugin() Plugin {
	return Plugin{
		ID:          strings.TrimSpace(m.ID),
		Name:        strings.TrimSpace(m.Name),
		Description: strings.TrimSpace(m.Description),
		Author:      strings.TrimSpace(m.Author),
		Version:     strings.TrimSpace(m.Version),
		Repository:  strings.TrimSpace(m.Repository),
		Logo:        strings.TrimSpace(m.Logo),
		Homepage:    strings.TrimSpace(m.Homepage),
		License:     strings.TrimSpace(m.License),
		Tags:        append([]string(nil), m.Tags...),
		Install:     NormalizeInstallPlan(m.Install),
	}
}

func (m Manifest) InstallType() string {
	installType := strings.ToLower(strings.TrimSpace(m.Install.Type))
	if installType == "" {
		return InstallTypeGitHubRelease
	}
	return installType
}

func (m Manifest) Validate() error {
	version := strings.TrimSpace(m.Version)
	if version == "" {
		return fmt.Errorf("missing required field version")
	}
	if !validPluginVersion(normalizeVersion(version)) {
		return fmt.Errorf("invalid plugin version %q", m.Version)
	}
	switch m.InstallType() {
	case InstallTypeDirect:
		if m.SchemaVersion != 0 && m.SchemaVersion != SchemaVersionV2 {
			return fmt.Errorf("unsupported schema-version %d", m.SchemaVersion)
		}
		if errID := validateManifestPluginID(m.ID); errID != nil {
			return errID
		}
		plan := NormalizeInstallPlan(m.Install)
		plan.Type = InstallTypeDirect
		if len(plan.Artifacts) > 0 {
			if errValidate := ValidateInstallPlan(plan); errValidate != nil {
				return errValidate
			}
			return validatePinnedArtifactURLs(plan.Artifacts)
		}
		return validateManifestSourceURL(m.SourceURL)
	case InstallTypeGitHubRelease:
		releaseTag := strings.TrimSpace(m.ReleaseTag)
		if releaseTag == "" {
			return fmt.Errorf("missing required field release-tag")
		}
		plugin := m.Plugin()
		plugin.Install = InstallPlan{Type: InstallTypeGitHubRelease}
		if errValidate := ValidatePlugin(plugin); errValidate != nil {
			return errValidate
		}
		releaseVersion, errVersion := ReleaseVersion(Release{TagName: releaseTag})
		if errVersion != nil {
			return errVersion
		}
		if releaseVersion != normalizeVersion(version) {
			return fmt.Errorf("release-tag %q resolves version %q, want %q", releaseTag, releaseVersion, normalizeVersion(version))
		}
		return nil
	default:
		return fmt.Errorf("unsupported install type %q", m.Install.Type)
	}
}

func validatePinnedArtifactURLs(artifacts []Artifact) error {
	for index, artifact := range artifacts {
		parsed, errParse := url.Parse(strings.TrimSpace(artifact.URL))
		if errParse != nil {
			return fmt.Errorf("artifacts[%d]: invalid artifact url", index)
		}
		if parsed.User != nil {
			return fmt.Errorf("artifacts[%d]: pinned artifact url must not contain credentials", index)
		}
		if parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("artifacts[%d]: pinned artifact url must not contain query or fragment", index)
		}
	}
	return nil
}

func validateManifestPluginID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("missing required field id")
	}
	if !validPluginID(id) {
		return fmt.Errorf("invalid plugin id %q", id)
	}
	return nil
}

func validateManifestSourceURL(sourceURL string) error {
	sourceURL = strings.TrimSpace(sourceURL)
	if sourceURL == "" {
		return fmt.Errorf("missing required field source-url")
	}
	parsed, errParse := url.Parse(sourceURL)
	if errParse != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("invalid source-url")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return fmt.Errorf("source-url must use http or https")
	}
	if hasSensitiveQueryParameter(parsed) {
		return fmt.Errorf("source-url contains sensitive query parameter")
	}
	return nil
}
