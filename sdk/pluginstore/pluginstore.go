// Package pluginstore exposes plugin registry and artifact installation helpers
// for embedders such as CLIProxyAPIHome.
package pluginstore

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	internalpluginstore "github.com/router-for-me/CLIProxyAPI/v7/internal/pluginstore"
)

const (
	DefaultRegistryURL = internalpluginstore.DefaultRegistryURL
	DefaultSourceID    = internalpluginstore.DefaultSourceID
	DefaultSourceName  = internalpluginstore.DefaultSourceName
	SchemaVersion      = internalpluginstore.SchemaVersion
)

type Source = internalpluginstore.Source
type Registry = internalpluginstore.Registry
type Plugin = internalpluginstore.Plugin
type Release = internalpluginstore.Release
type ReleaseAsset = internalpluginstore.ReleaseAsset
type InstallOptions = internalpluginstore.InstallOptions
type InstallResult = internalpluginstore.InstallResult

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

var ErrLoadedPluginLocked = internalpluginstore.ErrLoadedPluginLocked

type Client struct {
	inner internalpluginstore.Client
}

type Manifest struct {
	ID          string   `yaml:"id,omitempty" json:"id,omitempty"`
	Name        string   `yaml:"name,omitempty" json:"name,omitempty"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Author      string   `yaml:"author,omitempty" json:"author,omitempty"`
	Version     string   `yaml:"version,omitempty" json:"version,omitempty"`
	ReleaseTag  string   `yaml:"release-tag,omitempty" json:"release_tag,omitempty"`
	Repository  string   `yaml:"repository,omitempty" json:"repository,omitempty"`
	Logo        string   `yaml:"logo,omitempty" json:"logo,omitempty"`
	Homepage    string   `yaml:"homepage,omitempty" json:"homepage,omitempty"`
	License     string   `yaml:"license,omitempty" json:"license,omitempty"`
	Tags        []string `yaml:"tags,omitempty" json:"tags,omitempty"`
	SourceID    string   `yaml:"source-id,omitempty" json:"source_id,omitempty"`
	SourceName  string   `yaml:"source-name,omitempty" json:"source_name,omitempty"`
	SourceURL   string   `yaml:"source-url,omitempty" json:"source_url,omitempty"`
}

func NewClient(httpClient HTTPDoer, registryURL string) Client {
	return Client{inner: internalpluginstore.Client{
		HTTPClient:  httpClient,
		RegistryURL: strings.TrimSpace(registryURL),
	}}
}

func DefaultSource() Source {
	return internalpluginstore.DefaultSource()
}

func NormalizeSources(registryURLs []string) ([]Source, error) {
	return internalpluginstore.NormalizeSources(registryURLs)
}

func SourceID(registryURL string) string {
	return internalpluginstore.SourceID(registryURL)
}

func ValidatePlugin(plugin Plugin) error {
	return internalpluginstore.ValidatePlugin(plugin)
}

func UpdateAvailable(installed, latest string) bool {
	return internalpluginstore.UpdateAvailable(installed, latest)
}

func ReleaseVersion(release Release) (string, error) {
	return internalpluginstore.ReleaseVersion(release)
}

func ManifestFromRelease(source Source, plugin Plugin, release Release) (Manifest, error) {
	version, errVersion := internalpluginstore.ReleaseVersion(release)
	if errVersion != nil {
		return Manifest{}, errVersion
	}
	return Manifest{
		ID:          strings.TrimSpace(plugin.ID),
		Name:        strings.TrimSpace(plugin.Name),
		Description: strings.TrimSpace(plugin.Description),
		Author:      strings.TrimSpace(plugin.Author),
		Version:     version,
		ReleaseTag:  strings.TrimSpace(release.TagName),
		Repository:  strings.TrimSpace(plugin.Repository),
		Logo:        strings.TrimSpace(plugin.Logo),
		Homepage:    strings.TrimSpace(plugin.Homepage),
		License:     strings.TrimSpace(plugin.License),
		Tags:        append([]string(nil), plugin.Tags...),
		SourceID:    strings.TrimSpace(source.ID),
		SourceName:  strings.TrimSpace(source.Name),
		SourceURL:   strings.TrimSpace(source.URL),
	}, nil
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
	}
}

func (m Manifest) Validate() error {
	version := strings.TrimSpace(m.Version)
	if version == "" {
		return fmt.Errorf("missing required field version")
	}
	releaseTag := strings.TrimSpace(m.ReleaseTag)
	if releaseTag == "" {
		return fmt.Errorf("missing required field release-tag")
	}
	if errValidate := internalpluginstore.ValidatePlugin(m.Plugin()); errValidate != nil {
		return errValidate
	}
	releaseVersion, errVersion := internalpluginstore.ReleaseVersion(internalpluginstore.Release{TagName: releaseTag})
	if errVersion != nil {
		return errVersion
	}
	if releaseVersion != version {
		return fmt.Errorf("release-tag %q resolves version %q, want %q", releaseTag, releaseVersion, version)
	}
	return nil
}

func (c Client) FetchRegistry(ctx context.Context) (Registry, error) {
	return c.inner.FetchRegistry(ctx)
}

func (c Client) FetchLatestRelease(ctx context.Context, plugin Plugin) (Release, error) {
	return c.inner.FetchLatestRelease(ctx, plugin)
}

func (c Client) FetchReleaseByTag(ctx context.Context, plugin Plugin, tag string) (Release, error) {
	return c.inner.FetchReleaseByTag(ctx, plugin, tag)
}

func (c Client) Install(ctx context.Context, plugin Plugin, options InstallOptions) (InstallResult, error) {
	return c.inner.Install(ctx, plugin, options)
}

func (c Client) InstallVersion(ctx context.Context, plugin Plugin, releaseTag string, version string, options InstallOptions) (InstallResult, error) {
	return c.inner.InstallVersion(ctx, plugin, releaseTag, version, options)
}

func (c Client) InstallManifest(ctx context.Context, manifest Manifest, options InstallOptions) (InstallResult, error) {
	if errValidate := manifest.Validate(); errValidate != nil {
		return InstallResult{}, errValidate
	}
	return c.InstallVersion(ctx, manifest.Plugin(), manifest.ReleaseTag, manifest.Version, options)
}
