package selfupdate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
)

const (
	maxReleaseJSON = 8 << 20
	maxChecksums   = 1 << 20
	maxSignature   = 4 << 10
	maxArchive     = 512 << 20
	maxNotes       = 32 << 10
)

// release is the subset of a GitHub release the updater uses.
type release struct {
	Tag        string
	Version    string
	HTMLURL    string
	Body       string
	Prerelease bool
	Draft      bool
	Assets     map[string]string // name -> download URL
}

func parseRelease(data []byte) (*release, error) {
	if !ojson.ValidBytes(data) {
		return nil, fmt.Errorf("release response is not valid JSON")
	}
	r := ojson.ParseBytes(data)
	tag := strings.TrimSpace(r.Get("tag_name").String())
	if tag == "" {
		return nil, fmt.Errorf("release response has no tag_name")
	}
	rel := &release{
		Tag:        tag,
		Version:    strings.TrimPrefix(tag, "v"),
		HTMLURL:    r.Get("html_url").String(),
		Body:       r.Get("body").String(),
		Prerelease: r.Get("prerelease").Bool(),
		Draft:      r.Get("draft").Bool(),
		Assets:     map[string]string{},
	}
	r.Get("assets").ForEach(func(_, a ojson.Result) bool {
		name := a.Get("name").String()
		url := a.Get("browser_download_url").String()
		if name != "" && url != "" {
			rel.Assets[name] = url
		}
		return true
	})
	return rel, nil
}

func userAgent() string { return "proxx-selfupdate/" + buildinfo.Version }

// fetchRelease GETs the release endpoint. With a non-empty etag it sends If-None-Match; notModified is
// true on 304.
func fetchRelease(ctx context.Context, client *http.Client, etag string) (rel *release, newETag string, notModified bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseURL(), nil)
	if err != nil {
		return nil, "", false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", userAgent())
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", false, fmt.Errorf("fetch release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotModified {
		return nil, etag, true, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", false, fmt.Errorf("fetch release: unexpected status %d", resp.StatusCode)
	}
	data, err := readLimited(resp.Body, maxReleaseJSON)
	if err != nil {
		return nil, "", false, fmt.Errorf("read release: %w", err)
	}
	rel, err = parseRelease(data)
	if err != nil {
		return nil, "", false, err
	}
	return rel, resp.Header.Get("ETag"), false, nil
}

// download fetches a release asset, refusing bodies larger than limit.
func download(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", userAgent())
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return readLimited(resp.Body, limit)
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return data, nil
}

func commandOutput(ctx context.Context, bin string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, bin, args...).CombinedOutput()
}
