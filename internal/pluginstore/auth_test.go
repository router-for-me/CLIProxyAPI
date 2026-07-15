package pluginstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPluginStoreAuthMatchesURLHostAndPathBoundaries(t *testing.T) {
	t.Setenv("PLUGIN_STORE_TOKEN", "secret-token")
	auth := []AuthConfig{{
		Match:    "https://downloads.example/private",
		ApplyTo:  []string{RequestKindArtifact},
		Type:     AuthTypeBearer,
		TokenEnv: "PLUGIN_STORE_TOKEN",
	}}

	tests := []struct {
		name     string
		url      string
		wantAuth bool
	}{
		{name: "exact path", url: "https://downloads.example/private", wantAuth: true},
		{name: "child path", url: "https://downloads.example/private/plugin.zip", wantAuth: true},
		{name: "sibling prefix", url: "https://downloads.example/private2/plugin.zip", wantAuth: false},
		{name: "similar host", url: "https://downloads.example.evil/private/plugin.zip", wantAuth: false},
		{name: "different scheme", url: "http://downloads.example/private/plugin.zip", wantAuth: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := http.Header{}
			if errAuth := applyPluginStoreAuth(headers, auth, tt.url, RequestKindArtifact); errAuth != nil {
				t.Fatalf("applyPluginStoreAuth() error = %v", errAuth)
			}
			gotAuth := headers.Get("Authorization") != ""
			if gotAuth != tt.wantAuth {
				t.Fatalf("Authorization set = %v, want %v", gotAuth, tt.wantAuth)
			}
		})
	}
}

func TestPluginStoreGitHubTokenUsesExplicitTokenEnv(t *testing.T) {
	t.Setenv("PLUGIN_STORE_TOKEN", "secret-token")
	headers := http.Header{}
	auth := []AuthConfig{{
		Match:    "https://api.github.com/repos/author-name/sample-provider/releases/",
		ApplyTo:  []string{RequestKindArtifact},
		Type:     AuthTypeGitHubToken,
		TokenEnv: "PLUGIN_STORE_TOKEN",
	}}

	if errAuth := applyPluginStoreAuth(headers, auth, "https://api.github.com/repos/author-name/sample-provider/releases/assets/1", RequestKindArtifact); errAuth != nil {
		t.Fatalf("applyPluginStoreAuth() error = %v", errAuth)
	}
	if gotAuth := headers.Get("Authorization"); gotAuth != "Bearer secret-token" {
		t.Fatalf("Authorization = %q, want Bearer secret-token", gotAuth)
	}
}

func TestPluginAuthConfiguredCoversInstallRequestKinds(t *testing.T) {
	t.Setenv("PLUGIN_STORE_TOKEN", "secret-token")

	source := Source{URL: "https://registry.example/registry.json"}
	directPlugin := Plugin{
		ID:      "sample-provider",
		Version: "1.0.0",
		Install: InstallPlan{
			Type: InstallTypeDirect,
			Artifacts: []Artifact{{
				GOOS:   "linux",
				GOARCH: "amd64",
				URL:    "https://downloads.example/private/sample-provider.zip",
				SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			}},
		},
	}
	gitHubPlugin := Plugin{
		ID:         "sample-provider",
		Repository: "https://github.com/author-name/sample-provider",
	}

	tests := []struct {
		name   string
		plugin Plugin
		auth   []AuthConfig
	}{
		{
			name:   "registry",
			plugin: gitHubPlugin,
			auth: []AuthConfig{{
				Match:    "https://registry.example/",
				ApplyTo:  []string{RequestKindRegistry},
				Type:     AuthTypeBearer,
				TokenEnv: "PLUGIN_STORE_TOKEN",
			}},
		},
		{
			name:   "direct artifact",
			plugin: directPlugin,
			auth: []AuthConfig{{
				Match:    "https://downloads.example/private/",
				ApplyTo:  []string{RequestKindArtifact},
				Type:     AuthTypeBearer,
				TokenEnv: "PLUGIN_STORE_TOKEN",
			}},
		},
		{
			name:   "github metadata",
			plugin: gitHubPlugin,
			auth: []AuthConfig{{
				Match:    "https://api.github.com/repos/author-name/sample-provider/releases/",
				ApplyTo:  []string{RequestKindMetadata},
				Type:     AuthTypeBearer,
				TokenEnv: "PLUGIN_STORE_TOKEN",
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !PluginAuthConfigured(source, tt.plugin, tt.auth) {
				t.Fatal("PluginAuthConfigured() = false, want true")
			}
		})
	}
}

func TestPluginStoreAuthHeaderIsReevaluatedAcrossRedirect(t *testing.T) {
	t.Setenv("PLUGIN_STORE_HEADER", "secret-token")

	var initialHeader string
	var redirectedHeader string
	artifactData := []byte("artifact-data")
	sum := sha256.Sum256(artifactData)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedHeader = r.Header.Get("X-Plugin-Token")
		_, _ = w.Write(artifactData)
	}))
	t.Cleanup(target.Close)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		initialHeader = r.Header.Get("X-Plugin-Token")
		http.Redirect(w, r, target.URL+"/artifact.zip", http.StatusFound)
	}))
	t.Cleanup(source.Close)

	client := Client{
		HTTPClient: source.Client(),
		Auth: []AuthConfig{
			{
				Match:          source.URL + "/private/",
				ApplyTo:        []string{RequestKindArtifact},
				Type:           AuthTypeHeader,
				HeaderName:     "X-Plugin-Token",
				HeaderValueEnv: "PLUGIN_STORE_HEADER",
				AllowInsecure:  true,
			},
			{
				Match:         target.URL + "/",
				ApplyTo:       []string{RequestKindArtifact},
				Type:          AuthTypeNone,
				AllowInsecure: true,
			},
		},
	}
	data, errDownload := client.DownloadArtifact(context.Background(), Artifact{
		GOOS:   "linux",
		GOARCH: "amd64",
		URL:    source.URL + "/private/artifact.zip",
		SHA256: hex.EncodeToString(sum[:]),
	})
	if errDownload != nil {
		t.Fatalf("DownloadArtifact() error = %v", errDownload)
	}
	if string(data) != string(artifactData) {
		t.Fatalf("DownloadArtifact() = %q, want %q", data, artifactData)
	}
	if initialHeader != "secret-token" {
		t.Fatalf("initial auth header = %q, want secret-token", initialHeader)
	}
	if redirectedHeader != "" {
		t.Fatalf("redirected auth header = %q, want empty", redirectedHeader)
	}
}

func TestAuthResolverCommandSourceTrimsAndCaches(t *testing.T) {
	resolver := NewAuthResolver()
	calls := 0
	restore := setAuthCommandRunnerForTest(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls++
		return []byte("  command-secret \n"), nil
	})
	t.Cleanup(restore)

	auth := []AuthConfig{{
		Match:        "https://plugins.example/private/",
		Type:         AuthTypeBearer,
		TokenCommand: "  resolve-token  ",
	}}
	for range 2 {
		headers := http.Header{}
		if errAuth := applyPluginStoreAuthContext(context.Background(), resolver, headers, auth, "https://plugins.example/private/a.zip", RequestKindArtifact); errAuth != nil {
			t.Fatalf("applyPluginStoreAuthContext() error = %v", errAuth)
		}
		if got := headers.Get("Authorization"); got != "Bearer command-secret" {
			t.Fatalf("Authorization = %q, want trimmed command result", got)
		}
	}
	if calls != 1 {
		t.Fatalf("command calls = %d, want 1", calls)
	}
}

func TestPluginStoreAuthHeaderIsAppliedToMatchingRedirect(t *testing.T) {
	t.Setenv("PLUGIN_STORE_HEADER", "secret-token")

	var redirectedHeader string
	artifactData := []byte("artifact-data")
	sum := sha256.Sum256(artifactData)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/private/start.zip" {
			http.Redirect(w, r, "/private/artifact.zip", http.StatusFound)
			return
		}
		redirectedHeader = r.Header.Get("X-Plugin-Token")
		_, _ = io.WriteString(w, string(artifactData))
	}))
	t.Cleanup(server.Close)

	client := Client{
		HTTPClient: server.Client(),
		Auth: []AuthConfig{{
			Match:          server.URL + "/private/",
			ApplyTo:        []string{RequestKindArtifact},
			Type:           AuthTypeHeader,
			HeaderName:     "X-Plugin-Token",
			HeaderValueEnv: "PLUGIN_STORE_HEADER",
			AllowInsecure:  true,
		}},
	}
	if _, errDownload := client.DownloadArtifact(context.Background(), Artifact{
		GOOS:   "linux",
		GOARCH: "amd64",
		URL:    server.URL + "/private/start.zip",
		SHA256: hex.EncodeToString(sum[:]),
	}); errDownload != nil {
		t.Fatalf("DownloadArtifact() error = %v", errDownload)
	}
	if redirectedHeader != "secret-token" {
		t.Fatalf("redirected auth header = %q, want secret-token", redirectedHeader)
	}
}

func TestNormalizeAuthConfigsTrimsCommandSources(t *testing.T) {
	normalized := NormalizeAuthConfigs([]AuthConfig{{
		Match:              " https://plugins.example/ ",
		TokenCommand:       " token ",
		UsernameCommand:    " username ",
		PasswordCommand:    " password ",
		HeaderValueCommand: " header ",
	}})
	if len(normalized) != 1 {
		t.Fatalf("NormalizeAuthConfigs() length = %d, want 1", len(normalized))
	}
	item := normalized[0]
	if item.TokenCommand != "token" || item.UsernameCommand != "username" || item.PasswordCommand != "password" || item.HeaderValueCommand != "header" {
		t.Fatalf("NormalizeAuthConfigs() command fields = %#v, want trimmed values", item)
	}
}

func TestAuthResolverCommandSourcesApplyAllSecretTypes(t *testing.T) {
	restore := setAuthCommandRunnerForTest(func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch args[len(args)-1] {
		case "token":
			return []byte(" token-value\n"), nil
		case "username":
			return []byte(" user "), nil
		case "password":
			return []byte(" password "), nil
		case "header":
			return []byte(" header-value "), nil
		default:
			t.Fatalf("unexpected command %q", args[len(args)-1])
			return nil, nil
		}
	})
	t.Cleanup(restore)

	tests := []struct {
		name string
		auth AuthConfig
		want string
	}{
		{name: "bearer", auth: AuthConfig{Type: AuthTypeBearer, TokenCommand: "token"}, want: "Bearer token-value"},
		{name: "github token", auth: AuthConfig{Type: AuthTypeGitHubToken, TokenCommand: "token"}, want: "Bearer token-value"},
		{name: "basic", auth: AuthConfig{Type: AuthTypeBasic, UsernameCommand: "username", PasswordCommand: "password"}, want: "Basic dXNlcjpwYXNzd29yZA=="},
		{name: "header", auth: AuthConfig{Type: AuthTypeHeader, HeaderName: "X-Token", HeaderValueCommand: "header"}, want: "header-value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.auth.Match = "https://plugins.example/"
			headers := http.Header{}
			if errAuth := applyPluginStoreAuthContext(context.Background(), NewAuthResolver(), headers, []AuthConfig{tt.auth}, "https://plugins.example/a", RequestKindArtifact); errAuth != nil {
				t.Fatalf("applyPluginStoreAuthContext() error = %v", errAuth)
			}
			got := headers.Get("Authorization")
			if tt.auth.Type == AuthTypeHeader {
				got = headers.Get("X-Token")
			}
			if got != tt.want {
				t.Fatalf("auth header = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAuthResolverRejectsAmbiguousAndMissingSourcesWithoutSecrets(t *testing.T) {
	secret := "do-not-leak"
	tests := []struct {
		name string
		auth AuthConfig
		want string
	}{
		{name: "ambiguous", auth: AuthConfig{Type: AuthTypeBearer, TokenEnv: "TOKEN", TokenCommand: secret}, want: "source is ambiguous"},
		{name: "missing", auth: AuthConfig{Type: AuthTypeBearer}, want: "missing token-env"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.auth.Match = "https://plugins.example/"
			errAuth := applyPluginStoreAuthContext(context.Background(), NewAuthResolver(), http.Header{}, []AuthConfig{tt.auth}, "https://plugins.example/a", RequestKindArtifact)
			if errAuth == nil || !strings.Contains(errAuth.Error(), tt.want) {
				t.Fatalf("applyPluginStoreAuthContext() error = %v, want %q", errAuth, tt.want)
			}
			if strings.Contains(errAuth.Error(), secret) {
				t.Fatalf("applyPluginStoreAuthContext() leaked secret in %q", errAuth)
			}
		})
	}
}

func TestAuthResolverCachesFailuresAndCoalescesConcurrentCommands(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	unblock := make(chan struct{})
	restore := setAuthCommandRunnerForTest(func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-unblock
		return []byte(""), nil
	})
	t.Cleanup(restore)

	resolver := NewAuthResolver()
	auth := []AuthConfig{{Match: "https://plugins.example/", Type: AuthTypeBearer, TokenCommand: "resolve"}}
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			errAuth := applyPluginStoreAuthContext(context.Background(), resolver, http.Header{}, auth, "https://plugins.example/a", RequestKindArtifact)
			if errAuth == nil || !strings.Contains(errAuth.Error(), "returned empty output") {
				t.Errorf("applyPluginStoreAuthContext() error = %v", errAuth)
			}
		}()
	}
	<-started
	close(unblock)
	group.Wait()
	if calls.Load() != 1 {
		t.Fatalf("command calls = %d, want 1", calls.Load())
	}
	if errAuth := applyPluginStoreAuthContext(context.Background(), resolver, http.Header{}, auth, "https://plugins.example/a", RequestKindArtifact); errAuth == nil {
		t.Fatal("applyPluginStoreAuthContext() error = nil, want cached failure")
	}
	if calls.Load() != 1 {
		t.Fatalf("cached failure command calls = %d, want 1", calls.Load())
	}
}

func TestAuthResolverDoesNotShareCacheAcrossResolvers(t *testing.T) {
	var calls atomic.Int32
	restore := setAuthCommandRunnerForTest(func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		calls.Add(1)
		return []byte("value"), nil
	})
	t.Cleanup(restore)
	auth := []AuthConfig{{Match: "https://plugins.example/", Type: AuthTypeBearer, TokenCommand: "resolve"}}
	for range 2 {
		if errAuth := applyPluginStoreAuthContext(context.Background(), NewAuthResolver(), http.Header{}, auth, "https://plugins.example/a", RequestKindArtifact); errAuth != nil {
			t.Fatalf("applyPluginStoreAuthContext() error = %v", errAuth)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("command calls = %d, want 2", calls.Load())
	}
}

func TestAuthConfiguredContextAgreesWithApplication(t *testing.T) {
	restore := setAuthCommandRunnerForTest(func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte("value"), nil
	})
	t.Cleanup(restore)
	resolver := NewAuthResolver()
	auth := []AuthConfig{{Match: "https://plugins.example/", Type: AuthTypeBearer, TokenCommand: "resolve"}}
	if !AuthConfiguredContext(context.Background(), resolver, auth, "https://plugins.example/a", RequestKindArtifact) {
		t.Fatal("AuthConfiguredContext() = false, want true")
	}
	headers := http.Header{}
	if errAuth := applyPluginStoreAuthContext(context.Background(), resolver, headers, auth, "https://plugins.example/a", RequestKindArtifact); errAuth != nil {
		t.Fatalf("applyPluginStoreAuthContext() error = %v", errAuth)
	}
	if headers.Get("Authorization") != "Bearer value" {
		t.Fatalf("Authorization = %q, want bearer value", headers.Get("Authorization"))
	}
}

func TestAuthCommandUsesCallerBoundedTimeout(t *testing.T) {
	restore := setAuthCommandRunnerForTest(func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("command context has no deadline")
		}
		remaining := time.Until(deadline)
		if remaining <= 0 || remaining > time.Second {
			t.Fatalf("command deadline remaining = %v, want within caller timeout", remaining)
		}
		return []byte("value"), nil
	})
	t.Cleanup(restore)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	auth := []AuthConfig{{Match: "https://plugins.example/", Type: AuthTypeBearer, TokenCommand: "resolve"}}
	if errAuth := applyPluginStoreAuthContext(ctx, NewAuthResolver(), http.Header{}, auth, "https://plugins.example/a", RequestKindArtifact); errAuth != nil {
		t.Fatalf("applyPluginStoreAuthContext() error = %v", errAuth)
	}
}

func TestAuthCommandFailuresDoNotLeakCommandOutputOrErrors(t *testing.T) {
	secret := "do-not-leak"
	restore := setAuthCommandRunnerForTest(func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, fmt.Errorf("failed with %s", secret)
	})
	t.Cleanup(restore)
	auth := []AuthConfig{{Match: "https://plugins.example/", Type: AuthTypeBearer, TokenCommand: "printf " + secret}}
	errAuth := applyPluginStoreAuthContext(context.Background(), NewAuthResolver(), http.Header{}, auth, "https://plugins.example/a", RequestKindArtifact)
	if errAuth == nil || !strings.Contains(errAuth.Error(), "token-command failed") {
		t.Fatalf("applyPluginStoreAuthContext() error = %v, want generic command failure", errAuth)
	}
	if strings.Contains(errAuth.Error(), secret) {
		t.Fatalf("applyPluginStoreAuthContext() leaked secret in %q", errAuth)
	}
}

func TestClientRedirectReusesCommandResolution(t *testing.T) {
	var calls atomic.Int32
	restore := setAuthCommandRunnerForTest(func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		calls.Add(1)
		return []byte("command-token"), nil
	})
	t.Cleanup(restore)

	var headers []string
	artifactData := []byte("artifact-data")
	sum := sha256.Sum256(artifactData)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = append(headers, r.Header.Get("X-Plugin-Token"))
		if r.URL.Path == "/private/start.zip" {
			http.Redirect(w, r, "/private/artifact.zip", http.StatusFound)
			return
		}
		_, _ = w.Write(artifactData)
	}))
	t.Cleanup(server.Close)

	client := Client{HTTPClient: server.Client(), Auth: []AuthConfig{{
		Match:              server.URL + "/private/",
		ApplyTo:            []string{RequestKindArtifact},
		Type:               AuthTypeHeader,
		HeaderName:         "X-Plugin-Token",
		HeaderValueCommand: "resolve",
		AllowInsecure:      true,
	}}}
	if _, errDownload := client.DownloadArtifact(context.Background(), Artifact{
		GOOS: "linux", GOARCH: "amd64", URL: server.URL + "/private/start.zip", SHA256: hex.EncodeToString(sum[:]),
	}); errDownload != nil {
		t.Fatalf("DownloadArtifact() error = %v", errDownload)
	}
	if calls.Load() != 1 {
		t.Fatalf("command calls = %d, want 1", calls.Load())
	}
	if len(headers) != 2 || headers[0] != "command-token" || headers[1] != "command-token" {
		t.Fatalf("redirect headers = %q, want command token on both requests", headers)
	}
}

func TestAuthCommandShellMatchesPlatform(t *testing.T) {
	name, args := authCommandShell("secret command")
	if runtime.GOOS == "windows" {
		if name != "cmd.exe" || len(args) != 2 || args[0] != "/C" {
			t.Fatalf("authCommandShell() = %q, %q; want cmd.exe /C", name, args)
		}
		return
	}
	if name != "/bin/sh" || len(args) != 2 || args[0] != "-c" {
		t.Fatalf("authCommandShell() = %q, %q; want /bin/sh -c", name, args)
	}
}

func TestAuthCommandInternalTimeoutIsStableAndCached(t *testing.T) {
	var calls atomic.Int32
	restore := setAuthCommandRunnerForTest(func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		calls.Add(1)
		<-ctx.Done()
		return nil, errors.New("signal: killed")
	})
	t.Cleanup(restore)

	resolver := newAuthResolver(time.Millisecond)
	auth := []AuthConfig{{Match: "https://plugins.example/", Type: AuthTypeBearer, TokenCommand: "resolve"}}
	for range 2 {
		errAuth := applyPluginStoreAuthContext(context.Background(), resolver, http.Header{}, auth, "https://plugins.example/a", RequestKindArtifact)
		if errAuth == nil || errAuth.Error() != "plugin store auth token-command timed out" {
			t.Fatalf("applyPluginStoreAuthContext() error = %v, want stable timeout", errAuth)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("command calls = %d, want cached timeout", calls.Load())
	}
}

func TestAuthCommandCallerCancellationDoesNotPoisonCache(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	restore := setAuthCommandRunnerForTest(func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
		}
		return []byte("resolved"), nil
	})
	t.Cleanup(restore)

	resolver := NewAuthResolver()
	auth := []AuthConfig{{Match: "https://plugins.example/", Type: AuthTypeBearer, TokenCommand: "resolve"}}
	ctx, cancel := context.WithCancel(context.Background())
	leader := make(chan error, 1)
	go func() {
		leader <- applyPluginStoreAuthContext(ctx, resolver, http.Header{}, auth, "https://plugins.example/a", RequestKindArtifact)
	}()
	<-started
	cancel()
	if errAuth := <-leader; !errors.Is(errAuth, context.Canceled) {
		t.Fatalf("leader error = %v, want context.Canceled", errAuth)
	}

	headers := http.Header{}
	if errAuth := applyPluginStoreAuthContext(context.Background(), resolver, headers, auth, "https://plugins.example/a", RequestKindArtifact); errAuth != nil {
		t.Fatalf("retry error = %v", errAuth)
	}
	if got := headers.Get("Authorization"); got != "Bearer resolved" {
		t.Fatalf("Authorization = %q, want resolved retry", got)
	}
	if calls.Load() != 2 {
		t.Fatalf("command calls = %d, want retry after caller cancellation", calls.Load())
	}
}

func TestAuthCommandLiveFollowerRetriesAfterLeaderCancellation(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	restore := setAuthCommandRunnerForTest(func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return []byte("resolved"), nil
	})
	t.Cleanup(restore)

	resolver := NewAuthResolver()
	auth := []AuthConfig{{Match: "https://plugins.example/", Type: AuthTypeBearer, TokenCommand: "resolve"}}
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	t.Cleanup(cancelLeader)
	leader := make(chan error, 1)
	go func() {
		leader <- applyPluginStoreAuthContext(leaderCtx, resolver, http.Header{}, auth, "https://plugins.example/a", RequestKindArtifact)
	}()
	<-started

	followerWaiting := make(chan struct{})
	resolver.onWait = func() {
		close(followerWaiting)
	}
	follower := make(chan error, 1)
	headers := http.Header{}
	go func() {
		follower <- applyPluginStoreAuthContext(context.Background(), resolver, headers, auth, "https://plugins.example/a", RequestKindArtifact)
	}()
	<-followerWaiting
	cancelLeader()

	if errAuth := <-leader; !errors.Is(errAuth, context.Canceled) {
		t.Fatalf("leader error = %v, want context.Canceled", errAuth)
	}
	if errAuth := <-follower; errAuth != nil {
		t.Fatalf("live follower error = %v", errAuth)
	}
	if got := headers.Get("Authorization"); got != "Bearer resolved" {
		t.Fatalf("Authorization = %q, want resolved retry", got)
	}
	if calls.Load() != 2 {
		t.Fatalf("command calls = %d, want one live follower retry", calls.Load())
	}
}

func TestAuthCommandCallerDeadlineDoesNotPoisonCache(t *testing.T) {
	var calls atomic.Int32
	restore := setAuthCommandRunnerForTest(func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		if calls.Add(1) == 1 {
			<-ctx.Done()
		}
		return []byte("resolved"), nil
	})
	t.Cleanup(restore)

	resolver := NewAuthResolver()
	auth := []AuthConfig{{Match: "https://plugins.example/", Type: AuthTypeBearer, TokenCommand: "resolve"}}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now())
	t.Cleanup(cancel)
	if errAuth := applyPluginStoreAuthContext(ctx, resolver, http.Header{}, auth, "https://plugins.example/a", RequestKindArtifact); !errors.Is(errAuth, context.DeadlineExceeded) {
		t.Fatalf("leader error = %v, want context.DeadlineExceeded", errAuth)
	}
	if errAuth := applyPluginStoreAuthContext(context.Background(), resolver, http.Header{}, auth, "https://plugins.example/a", RequestKindArtifact); errAuth != nil {
		t.Fatalf("retry error = %v", errAuth)
	}
	if calls.Load() != 2 {
		t.Fatalf("command calls = %d, want retry after caller deadline", calls.Load())
	}
}

func TestAuthCommandFailureIsStableAndDoesNotLeakSecrets(t *testing.T) {
	secret := "secret-expression-stdout-stderr"
	restore := setAuthCommandRunnerForTest(func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(secret), fmt.Errorf("start failed: %s", secret)
	})
	t.Cleanup(restore)

	auth := []AuthConfig{{Match: "https://plugins.example/", Type: AuthTypeBearer, TokenCommand: secret}}
	errAuth := applyPluginStoreAuthContext(context.Background(), NewAuthResolver(), http.Header{}, auth, "https://plugins.example/a", RequestKindArtifact)
	if errAuth == nil || errAuth.Error() != "plugin store auth token-command failed" {
		t.Fatalf("applyPluginStoreAuthContext() error = %v, want stable command failure", errAuth)
	}
	if strings.Contains(errAuth.Error(), secret) {
		t.Fatalf("applyPluginStoreAuthContext() leaked secret in %q", errAuth)
	}
}
