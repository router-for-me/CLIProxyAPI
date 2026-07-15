// Package pluginstore exposes plugin registry and artifact installation helpers
// for embedders such as CLIProxyAPIHome.
package pluginstore

import (
	"context"
	"net/http"
	"strings"

	internalpluginstore "github.com/router-for-me/CLIProxyAPI/v7/internal/pluginstore"
)

const (
	DefaultRegistryURL = internalpluginstore.DefaultRegistryURL
	DefaultSourceID    = internalpluginstore.DefaultSourceID
	DefaultSourceName  = internalpluginstore.DefaultSourceName
	SchemaVersion      = internalpluginstore.SchemaVersion
	SchemaVersionV2    = internalpluginstore.SchemaVersionV2

	InstallTypeGitHubRelease = internalpluginstore.InstallTypeGitHubRelease
	InstallTypeDirect        = internalpluginstore.InstallTypeDirect

	RequestKindRegistry = internalpluginstore.RequestKindRegistry
	RequestKindMetadata = internalpluginstore.RequestKindMetadata
	RequestKindArtifact = internalpluginstore.RequestKindArtifact

	AuthTypeNone        = internalpluginstore.AuthTypeNone
	AuthTypeBearer      = internalpluginstore.AuthTypeBearer
	AuthTypeBasic       = internalpluginstore.AuthTypeBasic
	AuthTypeHeader      = internalpluginstore.AuthTypeHeader
	AuthTypeGitHubToken = internalpluginstore.AuthTypeGitHubToken
)

type Source = internalpluginstore.Source
type Registry = internalpluginstore.Registry
type Plugin = internalpluginstore.Plugin
type Version = internalpluginstore.Version
type Release = internalpluginstore.Release
type ReleaseAsset = internalpluginstore.ReleaseAsset
type InstallOptions = internalpluginstore.InstallOptions
type InstallResult = internalpluginstore.InstallResult
type InstallPlan = internalpluginstore.InstallPlan
type Artifact = internalpluginstore.Artifact
type Platform = internalpluginstore.Platform
type Manifest = internalpluginstore.Manifest
type AuthConfig = internalpluginstore.AuthConfig
type AuthResolver = internalpluginstore.AuthResolver

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

var ErrLoadedPluginLocked = internalpluginstore.ErrLoadedPluginLocked

type Client struct {
	inner internalpluginstore.Client
}

func NewClient(httpClient HTTPDoer, registryURL string) Client {
	return Client{inner: internalpluginstore.Client{
		HTTPClient:  httpClient,
		RegistryURL: strings.TrimSpace(registryURL),
	}}
}

func NewClientWithAuth(httpClient HTTPDoer, registryURL string, auth []AuthConfig) Client {
	return Client{inner: internalpluginstore.Client{
		HTTPClient:  httpClient,
		RegistryURL: strings.TrimSpace(registryURL),
		Auth:        internalpluginstore.NormalizeAuthConfigs(auth),
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

func PluginInstallType(plugin Plugin) string {
	return internalpluginstore.PluginInstallType(plugin)
}

func PluginPlatforms(plugin Plugin) []Platform {
	return internalpluginstore.PluginPlatforms(plugin)
}

func PluginArtifacts(plugin Plugin) []Artifact {
	return internalpluginstore.PluginArtifacts(plugin)
}

func NormalizeAuthConfigs(auth []AuthConfig) []AuthConfig {
	return internalpluginstore.NormalizeAuthConfigs(auth)
}

func ValidateAuthConfigs(auth []AuthConfig) error {
	return internalpluginstore.ValidateAuthConfigs(auth)
}

func NewAuthResolver() *AuthResolver {
	return internalpluginstore.NewAuthResolver()
}

func AuthConfigured(auth []AuthConfig, requestURL string, kind string) bool {
	return internalpluginstore.AuthConfigured(auth, requestURL, kind)
}

func AuthConfiguredContext(ctx context.Context, resolver *AuthResolver, auth []AuthConfig, requestURL string, kind string) bool {
	return internalpluginstore.AuthConfiguredContext(ctx, resolver, auth, requestURL, kind)
}

func PluginAuthConfigured(source Source, plugin Plugin, auth []AuthConfig) bool {
	return internalpluginstore.PluginAuthConfigured(source, plugin, auth)
}

func PluginAuthConfiguredContext(ctx context.Context, resolver *AuthResolver, source Source, plugin Plugin, auth []AuthConfig) bool {
	return internalpluginstore.PluginAuthConfiguredContext(ctx, resolver, source, plugin, auth)
}

func UpdateAvailable(installed, latest string) bool {
	return internalpluginstore.UpdateAvailable(installed, latest)
}

func ReleaseVersion(release Release) (string, error) {
	return internalpluginstore.ReleaseVersion(release)
}

func ManifestFromRelease(source Source, plugin Plugin, release Release) (Manifest, error) {
	return internalpluginstore.ManifestFromRelease(source, plugin, release)
}

func ManifestFromPlugin(source Source, plugin Plugin) (Manifest, error) {
	return internalpluginstore.ManifestFromPlugin(source, plugin)
}

func (c Client) FetchRegistry(ctx context.Context) (Registry, error) {
	return c.inner.FetchRegistry(ctx)
}

func (c Client) FetchRegistryWithResolver(ctx context.Context, resolver *AuthResolver) (Registry, error) {
	return c.inner.FetchRegistryWithResolver(ctx, resolver)
}

func (c Client) FetchLatestRelease(ctx context.Context, plugin Plugin) (Release, error) {
	return c.inner.FetchLatestRelease(ctx, plugin)
}

func (c Client) FetchLatestReleaseWithResolver(ctx context.Context, resolver *AuthResolver, plugin Plugin) (Release, error) {
	return c.inner.FetchLatestReleaseWithResolver(ctx, resolver, plugin)
}

func (c Client) FetchReleaseByTag(ctx context.Context, plugin Plugin, tag string) (Release, error) {
	return c.inner.FetchReleaseByTag(ctx, plugin, tag)
}

func (c Client) FetchReleaseByTagWithResolver(ctx context.Context, resolver *AuthResolver, plugin Plugin, tag string) (Release, error) {
	return c.inner.FetchReleaseByTagWithResolver(ctx, resolver, plugin, tag)
}

func (c Client) Install(ctx context.Context, plugin Plugin, options InstallOptions) (InstallResult, error) {
	return c.inner.Install(ctx, plugin, options)
}

func (c Client) InstallWithResolver(ctx context.Context, resolver *AuthResolver, plugin Plugin, options InstallOptions) (InstallResult, error) {
	return c.inner.InstallWithResolver(ctx, resolver, plugin, options)
}

func (c Client) InstallVersion(ctx context.Context, plugin Plugin, releaseTag string, version string, options InstallOptions) (InstallResult, error) {
	return c.inner.InstallVersion(ctx, plugin, releaseTag, version, options)
}

func (c Client) InstallVersionWithResolver(ctx context.Context, resolver *AuthResolver, plugin Plugin, releaseTag string, version string, options InstallOptions) (InstallResult, error) {
	return c.inner.InstallVersionWithResolver(ctx, resolver, plugin, releaseTag, version, options)
}

func (c Client) InstallManifest(ctx context.Context, manifest Manifest, options InstallOptions) (InstallResult, error) {
	return c.inner.InstallManifest(ctx, manifest, options)
}

func (c Client) InstallManifestWithResolver(ctx context.Context, resolver *AuthResolver, manifest Manifest, options InstallOptions) (InstallResult, error) {
	return c.inner.InstallManifestWithResolver(ctx, resolver, manifest, options)
}
