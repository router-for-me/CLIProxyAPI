package pluginstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/httpfetch"
	log "github.com/sirupsen/logrus"
)

const userAgent = "CLIProxyAPI"
const maxPluginStoreRedirects = 10

// HTTPDoer abstracts the HTTP client used to execute requests.
type HTTPDoer = httpfetch.Doer

type Client struct {
	HTTPClient            HTTPDoer
	RegistryURL           string
	UserAgent             string
	Auth                  []AuthConfig
	ResolvedAuth          []ResolvedAuthConfig
	ResolvedAuthExpiresAt time.Time
}

type Release struct {
	TagName string         `json:"tag_name"`
	Assets  []ReleaseAsset `json:"assets"`
}

type ReleaseAsset struct {
	APIURL             string `json:"url"`
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func (c Client) FetchRegistry(ctx context.Context) (Registry, error) {
	registryURL := strings.TrimSpace(c.RegistryURL)
	if registryURL == "" {
		registryURL = DefaultRegistryURL
	}
	data, errDownload := c.get(ctx, registryURL, "application/json", RequestKindRegistry, 0)
	if errDownload != nil {
		return Registry{}, errDownload
	}
	registry, errParse := ParseRegistry(data)
	if errParse != nil {
		return Registry{}, errParse
	}
	return registry, nil
}

// FetchLatestRelease returns the latest published release of the plugin's
// GitHub repository, mirroring the WebUI panel update check.
func (c Client) FetchLatestRelease(ctx context.Context, plugin Plugin) (Release, error) {
	owner, repo, errRepository := GitHubRepositoryParts(plugin.Repository)
	if errRepository != nil {
		return Release{}, errRepository
	}
	releaseURL := fmt.Sprintf(
		"https://api.github.com/repos/%s/%s/releases/latest",
		url.PathEscape(owner),
		url.PathEscape(repo),
	)
	data, errDownload := c.get(ctx, releaseURL, "application/vnd.github+json", RequestKindMetadata, 0)
	if errDownload != nil {
		return Release{}, errDownload
	}
	var release Release
	if errDecode := json.Unmarshal(data, &release); errDecode != nil {
		return Release{}, fmt.Errorf("decode release: %w", errDecode)
	}
	return release, nil
}

// FetchReleaseByTag returns a published release by its exact GitHub tag.
func (c Client) FetchReleaseByTag(ctx context.Context, plugin Plugin, tag string) (Release, error) {
	owner, repo, errRepository := GitHubRepositoryParts(plugin.Repository)
	if errRepository != nil {
		return Release{}, errRepository
	}
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return Release{}, fmt.Errorf("release tag is required")
	}
	releaseURL := fmt.Sprintf(
		"https://api.github.com/repos/%s/%s/releases/tags/%s",
		url.PathEscape(owner),
		url.PathEscape(repo),
		url.PathEscape(tag),
	)
	data, errDownload := c.get(ctx, releaseURL, "application/vnd.github+json", RequestKindMetadata, 0)
	if errDownload != nil {
		return Release{}, errDownload
	}
	var release Release
	if errDecode := json.Unmarshal(data, &release); errDecode != nil {
		return Release{}, fmt.Errorf("decode release: %w", errDecode)
	}
	return release, nil
}

// ReleaseVersion derives the plugin version from the release tag, stripping a
// leading "v"/"V" and validating the result.
func ReleaseVersion(release Release) (string, error) {
	version := normalizeVersion(release.TagName)
	if !validPluginVersion(version) {
		return "", fmt.Errorf("invalid release tag %q", release.TagName)
	}
	return version, nil
}

func (c Client) DownloadAsset(ctx context.Context, asset ReleaseAsset) ([]byte, error) {
	downloadURL := strings.TrimSpace(asset.BrowserDownloadURL)
	apiURL := strings.TrimSpace(asset.APIURL)
	if downloadURL == "" || c.releaseAssetAPIAuthenticated(apiURL) {
		if apiURL != "" {
			downloadURL = apiURL
		}
	}
	if downloadURL == "" {
		return nil, fmt.Errorf("asset %q missing download url", asset.Name)
	}
	return c.get(ctx, downloadURL, "application/octet-stream", RequestKindArtifact, 0)
}

func (c Client) releaseAssetAPIAuthenticated(apiURL string) bool {
	apiURL = strings.TrimSpace(apiURL)
	if apiURL == "" {
		return false
	}
	if item, ok := matchingResolvedAuthConfig(c.ResolvedAuth, apiURL, RequestKindArtifact); ok {
		return resolvedAuthConfigured(item)
	}
	return AuthConfigured(c.Auth, apiURL, RequestKindArtifact)
}

func (c Client) get(ctx context.Context, requestURL string, accept string, kind string, maxSize int64) ([]byte, error) {
	currentURL := strings.TrimSpace(requestURL)
	for redirects := 0; ; redirects++ {
		if errURL := validatePluginStoreRequestURL(c.Auth, currentURL, kind); errURL != nil {
			return nil, errURL
		}
		if errExpiry := validateResolvedAuthExpiry(c.ResolvedAuth, c.ResolvedAuthExpiresAt, time.Now().UTC(), currentURL, kind); errExpiry != nil {
			return nil, errExpiry
		}
		headers := http.Header{
			"Accept":     []string{accept},
			"User-Agent": []string{c.userAgent()},
		}
		authenticated, errAuth := applyPluginStoreAuthForClient(headers, c.ResolvedAuth, c.Auth, currentURL, kind)
		if errAuth != nil {
			return nil, errAuth
		}
		resp, errDo := pluginStoreGetNoRedirect(ctx, c.httpClient(), currentURL, headers)
		if authenticated {
			for name := range headers {
				headers.Del(name)
			}
			if resp != nil && resp.Request != nil {
				resp.Request.Header = nil
			}
		}
		if errDo != nil {
			return nil, errDo
		}
		if pluginStoreRedirectStatus(resp.StatusCode) {
			nextURL, errRedirect := pluginStoreRedirectURL(resp, currentURL)
			if errClose := resp.Body.Close(); errClose != nil {
				log.WithError(errClose).Debug("failed to close plugin store redirect body")
			}
			if errRedirect != nil {
				return nil, errRedirect
			}
			if redirects >= maxPluginStoreRedirects {
				return nil, fmt.Errorf("stopped after %d redirects", maxPluginStoreRedirects)
			}
			currentURL = nextURL
			continue
		}
		return readPluginStoreResponse(resp, maxSize, authenticated)
	}
}

func (c Client) httpClient() HTTPDoer {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c Client) userAgent() string {
	if strings.TrimSpace(c.UserAgent) != "" {
		return strings.TrimSpace(c.UserAgent)
	}
	return userAgent
}

func pluginStoreGetNoRedirect(ctx context.Context, client HTTPDoer, requestURL string, headers http.Header) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if errRequest != nil {
		return nil, fmt.Errorf("create request: %w", errRequest)
	}
	req.Header = headers.Clone()
	resp, errDo := pluginStoreNoRedirectClient(client).Do(req)
	if errDo != nil {
		return nil, pluginStoreRequestError(requestURL, errDo)
	}
	return resp, nil
}

func pluginStoreNoRedirectClient(client HTTPDoer) HTTPDoer {
	httpClient, ok := client.(*http.Client)
	if !ok {
		return client
	}
	clone := *httpClient
	clone.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &clone
}

func pluginStoreRedirectStatus(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

func pluginStoreRedirectURL(resp *http.Response, requestURL string) (string, error) {
	location := strings.TrimSpace(resp.Header.Get("Location"))
	if location == "" {
		return "", fmt.Errorf("redirect missing Location header")
	}
	base, errBase := url.Parse(requestURL)
	if errBase != nil {
		return "", fmt.Errorf("parse redirect base: %w", errBase)
	}
	next, errNext := base.Parse(location)
	if errNext != nil {
		return "", fmt.Errorf("parse redirect location: %w", errNext)
	}
	if next.Scheme == "" || next.Host == "" {
		return "", fmt.Errorf("redirect location is not absolute")
	}
	return next.String(), nil
}

func readPluginStoreResponse(resp *http.Response, maxSize int64, authenticated bool) ([]byte, error) {
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("failed to close plugin store response body")
		}
	}()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		if authenticated {
			return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	reader := io.Reader(resp.Body)
	if maxSize > 0 {
		reader = io.LimitReader(resp.Body, maxSize+1)
	}
	data, errRead := io.ReadAll(reader)
	if errRead != nil {
		return nil, fmt.Errorf("read response: %w", errRead)
	}
	if maxSize > 0 && int64(len(data)) > maxSize {
		return nil, fmt.Errorf("response exceeds maximum allowed size of %d bytes", maxSize)
	}
	return data, nil
}

func pluginStoreRequestError(requestURL string, err error) error {
	parsed, errParse := url.Parse(strings.TrimSpace(requestURL))
	safeURL := "plugin store url"
	if errParse == nil && parsed.Scheme != "" && parsed.Host != "" {
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		parsed.Fragment = ""
		safeURL = parsed.String()
	}
	var urlError *url.Error
	if errors.As(err, &urlError) && urlError.Err != nil {
		err = urlError.Err
	}
	return fmt.Errorf("request %s failed: %w", safeURL, err)
}

func SelectReleaseAssets(release Release, id, version, goos, goarch string) (ReleaseAsset, ReleaseAsset, error) {
	archiveName := ArchiveName(id, version, goos, goarch)
	var archiveAsset ReleaseAsset
	var checksumAsset ReleaseAsset
	for _, asset := range release.Assets {
		switch strings.TrimSpace(asset.Name) {
		case archiveName:
			archiveAsset = asset
		case "checksums.txt":
			checksumAsset = asset
		}
	}
	if strings.TrimSpace(archiveAsset.Name) == "" {
		return ReleaseAsset{}, ReleaseAsset{}, fmt.Errorf("release asset %s not found", archiveName)
	}
	if strings.TrimSpace(checksumAsset.Name) == "" {
		return ReleaseAsset{}, ReleaseAsset{}, fmt.Errorf("release asset checksums.txt not found")
	}
	return archiveAsset, checksumAsset, nil
}

func ArchiveName(id, version, goos, goarch string) string {
	return fmt.Sprintf(
		"%s_%s_%s_%s.zip",
		strings.TrimSpace(id),
		strings.TrimSpace(version),
		strings.TrimSpace(goos),
		strings.TrimSpace(goarch),
	)
}
