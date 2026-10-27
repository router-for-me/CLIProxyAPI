package browser

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestKunKunDriverLaunchArguments(t *testing.T) {
	authURL := "https://auth.example.com/oauth?redirect_uri=http%3A%2F%2Flocalhost%3A54545%2Fcallback&state=abc"

	tests := []struct {
		name         string
		cfg          config.KunKunBrowserConfig
		opts         LaunchOptions
		wantBin      string
		wantPrefix   []string
		wantNoPrefix bool
		wantProxy    string
		wantDevice   string
		wantHeadless bool
		wantCallback string
	}{
		{
			name:         "uv inserts run python",
			cfg:          config.KunKunBrowserConfig{PythonBin: "uv", ProfilesDir: t.TempDir()},
			opts:         LaunchOptions{AuthURL: authURL, ProxyURL: "http://127.0.0.1:8080", ProfileID: "claude-1"},
			wantBin:      "uv",
			wantPrefix:   []string{"run", "python", "main.py", "--oauth-launch", "--url", authURL},
			wantProxy:    "http://127.0.0.1:8080",
			wantDevice:   "browser",
			wantCallback: "http://localhost:54545/callback",
		},
		{
			name:         "uv absolute path still inserts run python",
			cfg:          config.KunKunBrowserConfig{PythonBin: "/usr/local/bin/uv", ProfilesDir: t.TempDir()},
			opts:         LaunchOptions{AuthURL: authURL, ProxyURL: "socks5://127.0.0.1:1080", ProfileID: "codex-1", DeviceMode: "qq"},
			wantBin:      "/usr/local/bin/uv",
			wantPrefix:   []string{"run", "python", "main.py", "--oauth-launch", "--url", authURL},
			wantProxy:    "socks5://127.0.0.1:1080",
			wantDevice:   "qq",
			wantCallback: "http://localhost:54545/callback",
		},
		{
			name:         "plain python bin",
			cfg:          config.KunKunBrowserConfig{PythonBin: "python3", ProfilesDir: t.TempDir()},
			opts:         LaunchOptions{AuthURL: authURL, ProfileID: "p-1"},
			wantBin:      "python3",
			wantPrefix:   []string{"main.py", "--oauth-launch", "--url", authURL},
			wantProxy:    "-",
			wantDevice:   "browser",
			wantCallback: "http://localhost:54545/callback",
		},
		{
			name:         "headless from options and config",
			cfg:          config.KunKunBrowserConfig{PythonBin: "uv", ProfilesDir: t.TempDir(), Headless: true},
			opts:         LaunchOptions{AuthURL: authURL, ProfileID: "p-2", Headless: true},
			wantBin:      "uv",
			wantPrefix:   []string{"run", "python", "main.py", "--oauth-launch", "--url", authURL},
			wantProxy:    "-",
			wantDevice:   "browser",
			wantHeadless: true,
			wantCallback: "http://localhost:54545/callback",
		},
		{
			name:         "no redirect uri omits callback prefix",
			cfg:          config.KunKunBrowserConfig{PythonBin: "uv", ProfilesDir: t.TempDir()},
			opts:         LaunchOptions{AuthURL: "https://auth.example.com/device?code=123", ProfileID: "p-3"},
			wantBin:      "uv",
			wantPrefix:   []string{"run", "python", "main.py", "--oauth-launch", "--url", "https://auth.example.com/device?code=123"},
			wantProxy:    "-",
			wantDevice:   "browser",
			wantNoPrefix: true,
		},
		{
			name:         "explicit callback prefix wins",
			cfg:          config.KunKunBrowserConfig{PythonBin: "uv", ProfilesDir: t.TempDir()},
			opts:         LaunchOptions{AuthURL: authURL, ProfileID: "p-4", CallbackPrefix: "http://127.0.0.1:9999/done"},
			wantBin:      "uv",
			wantPrefix:   []string{"run", "python", "main.py", "--oauth-launch", "--url", authURL},
			wantProxy:    "-",
			wantDevice:   "browser",
			wantCallback: "http://127.0.0.1:9999/done",
		},
		{
			name:         "configured device mode is the default",
			cfg:          config.KunKunBrowserConfig{PythonBin: "uv", ProfilesDir: t.TempDir(), DefaultDeviceMode: "wechat"},
			opts:         LaunchOptions{AuthURL: authURL, ProfileID: "p-5"},
			wantBin:      "uv",
			wantPrefix:   []string{"run", "python", "main.py", "--oauth-launch", "--url", authURL},
			wantProxy:    "-",
			wantDevice:   "wechat",
			wantCallback: "http://localhost:54545/callback",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			driver := NewKunKunDriver(tc.cfg)
			bin, args, userDataDir, errArgs := driver.launchArguments(tc.opts)
			if errArgs != nil {
				t.Fatalf("launchArguments() error = %v", errArgs)
			}
			if bin != tc.wantBin {
				t.Fatalf("bin = %q, want %q", bin, tc.wantBin)
			}
			if len(args) < len(tc.wantPrefix) {
				t.Fatalf("args = %v, want prefix %v", args, tc.wantPrefix)
			}
			for i, want := range tc.wantPrefix {
				if args[i] != want {
					t.Fatalf("args[%d] = %q, want %q (args=%v)", i, args[i], want, args)
				}
			}
			if got := argumentValue(args, "--proxy"); got != tc.wantProxy {
				t.Fatalf("--proxy = %q, want %q", got, tc.wantProxy)
			}
			if got := argumentValue(args, "--device-mode"); got != tc.wantDevice {
				t.Fatalf("--device-mode = %q, want %q", got, tc.wantDevice)
			}
			if got := argumentValue(args, "--callback-prefix"); got != tc.wantCallback {
				t.Fatalf("--callback-prefix = %q, want %q", got, tc.wantCallback)
			}
			if tc.wantNoPrefix && hasArgument(args, "--callback-prefix") {
				t.Fatalf("args contain --callback-prefix but none expected: %v", args)
			}
			if hasArgument(args, "--headless") != tc.wantHeadless {
				t.Fatalf("--headless presence = %v, want %v", hasArgument(args, "--headless"), tc.wantHeadless)
			}
			if !filepath.IsAbs(userDataDir) {
				t.Fatalf("user data dir = %q, want absolute", userDataDir)
			}
			if got := argumentValue(args, "--user-data-dir"); got != userDataDir {
				t.Fatalf("--user-data-dir = %q, want %q", got, userDataDir)
			}
			if tc.opts.UserDataDir == "" && tc.opts.ProfileID != "" {
				wantDir := filepath.Join(tc.cfg.ProfilesDir, tc.opts.ProfileID)
				if userDataDir != wantDir {
					t.Fatalf("user data dir = %q, want %q", userDataDir, wantDir)
				}
			}
		})
	}
}

func TestKunKunDriverLaunchCompletedStatus(t *testing.T) {
	requireShell(t)
	projectRoot := t.TempDir()
	writeMainPy(t, projectRoot, "echo \"starting browser\"\necho \"OAUTH_STATUS:completed\"\n")

	driver := NewKunKunDriver(config.KunKunBrowserConfig{
		ProjectRoot:          projectRoot,
		PythonBin:            "sh",
		ProfilesDir:          t.TempDir(),
		LaunchTimeoutSeconds: 10,
	})
	opts := LaunchOptions{AuthURL: "https://example.com/oauth", ProfileID: "claude-complete"}

	if errLaunch := driver.Launch(context.Background(), opts); errLaunch != nil {
		t.Fatalf("Launch() error = %v, want nil", errLaunch)
	}

	profileDir := filepath.Join(driver.cfg.ProfilesDir, opts.ProfileID)
	if info, errStat := os.Stat(profileDir); errStat != nil || !info.IsDir() {
		t.Fatalf("profile directory %q was not created: %v", profileDir, errStat)
	}
}

func TestKunKunDriverLaunchStatusFailures(t *testing.T) {
	requireShell(t)

	tests := []struct {
		name    string
		script  string
		wantErr string
	}{
		{
			name:    "failed status",
			script:  "echo \"OAUTH_STATUS:failed\"\nexit 2\n",
			wantErr: "reported authorization failure",
		},
		{
			name:    "closed status",
			script:  "echo \"OAUTH_STATUS:closed\"\n",
			wantErr: "closed before authorization completed",
		},
		{
			name:    "nonzero exit without status",
			script:  "echo \"boom\" >&2\nexit 7\n",
			wantErr: "exited with code 7",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			projectRoot := t.TempDir()
			writeMainPy(t, projectRoot, tc.script)
			driver := NewKunKunDriver(config.KunKunBrowserConfig{
				ProjectRoot:          projectRoot,
				PythonBin:            "sh",
				ProfilesDir:          t.TempDir(),
				LaunchTimeoutSeconds: 10,
			})
			errLaunch := driver.Launch(context.Background(), LaunchOptions{
				AuthURL:   "https://example.com/oauth",
				ProfileID: "claude-fail",
			})
			if errLaunch == nil || !strings.Contains(errLaunch.Error(), tc.wantErr) {
				t.Fatalf("Launch() error = %v, want containing %q", errLaunch, tc.wantErr)
			}
		})
	}
}

func TestKunKunDriverLaunchTimeoutKillsProcess(t *testing.T) {
	requireShell(t)
	projectRoot := t.TempDir()
	writeMainPy(t, projectRoot, "sleep 10\necho \"OAUTH_STATUS:completed\"\n")

	driver := NewKunKunDriver(config.KunKunBrowserConfig{
		ProjectRoot:          projectRoot,
		PythonBin:            "sh",
		ProfilesDir:          t.TempDir(),
		LaunchTimeoutSeconds: 1,
	})

	start := time.Now()
	errLaunch := driver.Launch(context.Background(), LaunchOptions{
		AuthURL:   "https://example.com/oauth",
		ProfileID: "claude-timeout",
	})
	elapsed := time.Since(start)

	if errLaunch == nil || !strings.Contains(errLaunch.Error(), "timed out") {
		t.Fatalf("Launch() error = %v, want timeout error", errLaunch)
	}
	if elapsed >= 5*time.Second {
		t.Fatalf("Launch() took %s, want the fake process to be killed promptly", elapsed)
	}
}

func TestKunKunDriverLaunchStartFailure(t *testing.T) {
	driver := NewKunKunDriver(config.KunKunBrowserConfig{
		ProjectRoot:          t.TempDir(),
		PythonBin:            filepath.Join(t.TempDir(), "missing-python"),
		ProfilesDir:          t.TempDir(),
		LaunchTimeoutSeconds: 5,
	})
	errLaunch := driver.Launch(context.Background(), LaunchOptions{
		AuthURL:   "https://example.com/oauth",
		ProfileID: "claude-missing",
	})
	if errLaunch == nil || !strings.Contains(errLaunch.Error(), "start kunkun oauth browser") {
		t.Fatalf("Launch() error = %v, want start failure", errLaunch)
	}
}

func TestKunKunDriverServiceModeNotImplemented(t *testing.T) {
	driver := NewKunKunDriver(config.KunKunBrowserConfig{
		DriverMode:  config.KunKunDriverModeService,
		ProjectRoot: t.TempDir(),
		PythonBin:   "uv",
	})
	errLaunch := driver.Launch(context.Background(), LaunchOptions{AuthURL: "https://example.com/oauth"})
	if errLaunch == nil || !strings.Contains(errLaunch.Error(), "not implemented") {
		t.Fatalf("Launch() error = %v, want not implemented", errLaunch)
	}
}

func TestKunKunDriverMissingAuthURL(t *testing.T) {
	driver := NewKunKunDriver(config.KunKunBrowserConfig{PythonBin: "uv", ProfilesDir: t.TempDir()})
	if errLaunch := driver.Launch(context.Background(), LaunchOptions{}); errLaunch == nil {
		t.Fatal("Launch() error = nil, want missing URL error")
	}
}

func TestScanOAuthStatus(t *testing.T) {
	status, errScan := scanOAuthStatus(strings.NewReader("noise\nOAUTH_STATUS:unknown\nOAUTH_STATUS:failed\n"))
	if errScan != nil {
		t.Fatalf("scanOAuthStatus() error = %v", errScan)
	}
	if status != oauthStatusFailed {
		t.Fatalf("status = %q, want %q", status, oauthStatusFailed)
	}
}

func TestCallbackPrefixFromAuthURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "redirect uri", url: "https://auth.example.com/x?redirect_uri=http%3A%2F%2F127.0.0.1%3A54545%2Fcallback", want: "http://127.0.0.1:54545/callback"},
		{name: "missing redirect uri", url: "https://auth.example.com/x?state=1", want: ""},
		{name: "invalid url", url: "://bad", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := callbackPrefixFromAuthURL(tc.url); got != tc.want {
				t.Fatalf("callbackPrefixFromAuthURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

func argumentValue(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

func hasArgument(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag {
			return true
		}
	}
	return false
}

func requireShell(t *testing.T) {
	t.Helper()
	if _, errLook := exec.LookPath("sh"); errLook != nil {
		t.Skip("sh is not available on this platform")
	}
}

func writeMainPy(t *testing.T, dir, script string) {
	t.Helper()
	if errWrite := os.WriteFile(filepath.Join(dir, "main.py"), []byte(script), 0o600); errWrite != nil {
		t.Fatalf("WriteFile(main.py) error = %v", errWrite)
	}
}
