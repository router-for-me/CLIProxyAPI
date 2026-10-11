package claudemaster

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchProfilesRejectsInvalidSeriesBeforePreflight(t *testing.T) {
	profile := Profile{Provider: "claude", AuthDir: "/profiles/one/auth", AuthID: "credential.json"}
	for _, tc := range []struct {
		name     string
		profiles []Profile
	}{
		{name: "empty", profiles: nil},
		{name: "duplicate", profiles: []Profile{profile, profile}},
		{name: "mixed", profiles: []Profile{profile, {Provider: "codex", AuthDir: "/profiles/two/auth", AuthID: "credential.json"}}},
		{name: "codex-single", profiles: []Profile{{Provider: "codex", AuthDir: "/profiles/one/auth", AuthID: "credential.json"}}},
		{name: "codex-series", profiles: []Profile{{Provider: "codex", AuthDir: "/profiles/one/auth", AuthID: "credential.json"}, {Provider: "codex", AuthDir: "/profiles/two/auth", AuthID: "credential.json"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, err := LaunchProfiles(t.Context(), tc.profiles, nil)
			if err == nil || code != 1 {
				t.Fatalf("invalid series accepted: code=%d err=%v", code, err)
			}
		})
	}
}

func environmentMap(env []string) map[string]string {
	out := map[string]string{}
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		out[key] = value
	}
	return out
}

func TestBackupAPIKeyEnvironmentNames(t *testing.T) {
	for _, name := range []string{BackupAPIKeyEnvironment, "ANTHROPIC_API_KEY", "_KEY_2", "a"} {
		if err := ValidateBackupAPIKeyEnv(name); err != nil {
			t.Fatalf("valid environment name rejected: %q", name)
		}
	}
	for _, name := range []string{"", "2KEY", "KEY=value-canary", "KEY-NAME", " KEY", "KEY\n", "ÅKEY"} {
		if err := ValidateBackupAPIKeyEnv(name); err == nil || strings.Contains(err.Error(), name) && name != "" {
			t.Fatalf("invalid environment name accepted or exposed: %q", name)
		}
	}
}

func TestLaunchEnvironmentConsumesOnlyBackupKeySources(t *testing.T) {
	for _, source := range []string{"", BackupAPIKeyEnvironment, "ANTHROPIC_API_KEY", "CUSTOM_BACKUP_KEY"} {
		t.Run(source, func(t *testing.T) {
			environ := []string{"PATH=/usr/bin", "USER=alice", BackupAPIKeyEnvironment + "=dedicated-secret"}
			if source != "" && source != BackupAPIKeyEnvironment {
				environ = append(environ, source+"=selected-secret", source+"=duplicate-secret")
			}
			filtered, err := launchEnvironment(environ, source)
			if err != nil {
				t.Fatal(err)
			}
			got := environmentMap(filtered)
			if len(got) != 2 || got["PATH"] != "/usr/bin" || got["USER"] != "alice" {
				t.Fatal("backup sources were inherited or unrelated environment was removed")
			}
			child, err := ChildEnvironment(filtered, nil, "proxy", "ca")
			if err != nil {
				t.Fatal("consumed key still triggered native provider-mode rejection")
			}
			if strings.Contains(strings.Join(child, "\n"), "secret") {
				t.Fatal("backup secret reached the native child")
			}
			if environ[2] != BackupAPIKeyEnvironment+"=dedicated-secret" {
				t.Fatal("parent environment was mutated")
			}
		})
	}
	filtered, err := launchEnvironment([]string{"ANTHROPIC_API_KEY=unrelated-secret"}, "CUSTOM_BACKUP_KEY")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ChildEnvironment(filtered, nil, "proxy", "ca"); err == nil {
		t.Fatal("an unselected native API key bypassed normal master-login validation")
	}
}

func TestPreflightConsumesBackupKeyBeforeSettingsDiscovery(t *testing.T) {
	home, binDir := nativeBinaryTestEnvironment(t)
	t.Chdir(home)
	writeNativeTestExecutable(t, filepath.Join(binDir, "claude"))
	// Fake Git checks only presence, never echoes credentials. Both repository-discovery
	// invocations must receive the filtered environment before native startup occurs.
	git := "#!/bin/sh\n" +
		"if [ \"${CLAUDE_MASTER_BACKUP_API_KEY+set}\" = set ] || [ \"${ANTHROPIC_API_KEY+set}\" = set ]; then exit 7; fi\n" +
		"case \"$3\" in\n" +
		"rev-parse) printf '%s\\n' \"$2\" ;;\n" +
		"worktree) printf 'worktree %s\\000' \"$2\" ;;\n" +
		"*) exit 8 ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(git), 0o700); err != nil {
		t.Fatal(err)
	}
	// A marker makes a Git error fail settings validation instead of silently treating
	// this checkout as a non-Git project.
	if err := os.Mkdir(filepath.Join(home, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(BackupAPIKeyEnvironment, "dedicated-canary")
	if _, err := Preflight(t.Context(), nil); err != nil {
		t.Fatalf("default backup key reached preflight discovery: %v", err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "selected-canary")
	environ, err := launchEnvironment(os.Environ(), "ANTHROPIC_API_KEY")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := preflight(t.Context(), nil, environ); err != nil {
		t.Fatalf("selected backup key reached preflight discovery: %v", err)
	}
	if os.Getenv("ANTHROPIC_API_KEY") != "selected-canary" {
		t.Fatal("launcher mutated the parent environment")
	}
}

func TestChildEnvironmentPreservesMasterAndScopesProxy(t *testing.T) {
	const proxyURL = "http://claude-master:token@127.0.0.1:1234"
	env, err := ChildEnvironment([]string{"PATH=/usr/bin", "USER=alice", "HTTPS_PROXY=http://old", "NO_PROXY=*", "no_proxy=api.anthropic.com", "CLAUDE_CODE_CHILD_SESSION=parent", "CLAUDE_CODE_SESSION_ID=old", "REMOTE_CLAW_SECRET_FILE=private", "CLAUDE_MASTER_TEST_SECRET=secret", "CLAUDE_CODE_CLIENT_CERT=/home/user/own.pem", "CLAUDE_CODE_CLIENT_KEY=/home/user/own.key", "CLAUDE_CODE_CLIENT_KEY_PASSPHRASE=hunter2"}, []string{"--remote-control"}, proxyURL, "/process/ca.pem")
	if err != nil {
		t.Fatal(err)
	}
	got := environmentMap(env)
	for _, key := range []string{"NO_PROXY", "no_proxy", "CLAUDE_CODE_CHILD_SESSION", "CLAUDE_CODE_SESSION_ID", "REMOTE_CLAW_SECRET_FILE", "CLAUDE_MASTER_TEST_SECRET"} {
		if _, ok := got[key]; ok {
			t.Errorf("leaked %s", key)
		}
	}
	// The local forwarder presents the client certificate; Claude holds none, and never one the
	// caller had configured.
	for _, key := range []string{"CLAUDE_CODE_CLIENT_CERT", "CLAUDE_CODE_CLIENT_KEY", "CLAUDE_CODE_CLIENT_KEY_PASSPHRASE"} {
		if _, ok := got[key]; ok {
			t.Errorf("the child was given %s", key)
		}
	}
	if got["PATH"] != "/usr/bin" || got["USER"] != "alice" || got["HTTPS_PROXY"] != proxyURL || got["https_proxy"] != proxyURL || got["NODE_EXTRA_CA_CERTS"] != "/process/ca.pem" || got["DISABLE_AUTOUPDATER"] != "1" {
		t.Fatal("incorrect child environment")
	}
}

func TestChildEnvironmentDisablesOnlyItsOwnUpdater(t *testing.T) {
	parent := []string{"DISABLE_AUTOUPDATER=0", "DISABLE_AUTOUPDATER=false"}
	env, err := ChildEnvironment(parent, nil, "proxy", "ca")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, value := range env {
		if strings.HasPrefix(value, "DISABLE_AUTOUPDATER=") {
			count++
			if value != "DISABLE_AUTOUPDATER=1" {
				t.Fatal("child updater was not disabled")
			}
		}
	}
	if count != 1 || parent[0] != "DISABLE_AUTOUPDATER=0" || parent[1] != "DISABLE_AUTOUPDATER=false" {
		t.Fatal("duplicate child override or parent environment mutation")
	}
}

func TestChildEnvironmentRejectsBypassConfiguration(t *testing.T) {
	for _, key := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CONFIG_DIR", "NODE_OPTIONS", "NODE_TLS_REJECT_UNAUTHORIZED"} {
		t.Run(key, func(t *testing.T) {
			_, err := ChildEnvironment([]string{key + "=sensitive-canary"}, nil, "proxy", "ca")
			if err == nil {
				t.Fatal("accepted bypass")
			}
			if strings.Contains(err.Error(), "sensitive-canary") {
				t.Fatal("secret leaked in error")
			}
		})
	}
	for _, arg := range []string{"--settings=canary", "--setting-sources", "--sdk-url=wss://bad", "--api-key=canary", "--base-url=https://bad"} {
		if _, err := ChildEnvironment(nil, []string{arg}, "proxy", "ca"); err == nil {
			t.Errorf("accepted flag %s", arg)
		}
	}
}

func TestChildEnvironmentKeepsHeapSizeNodeOptions(t *testing.T) {
	env, err := ChildEnvironment([]string{"NODE_OPTIONS=--max-old-space-size=3072"}, nil, "proxy", "ca")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range env {
		if entry == "NODE_OPTIONS=--max-old-space-size=3072" {
			found = true
		}
	}
	if !found {
		t.Fatal("heap-size NODE_OPTIONS was dropped")
	}
	for _, value := range []string{"--max-old-space-size=3072 --require /tmp/sensitive-canary.js", "--use-openssl-ca"} {
		if _, err := ChildEnvironment([]string{"NODE_OPTIONS=" + value}, nil, "proxy", "ca"); err == nil {
			t.Errorf("accepted NODE_OPTIONS %q", value)
		}
	}
	path := writeLauncherSettings(t, `{"env":{"NODE_OPTIONS":"--max-old-space-size=3072"}}`)
	if _, err := ChildEnvironment(nil, []string{"--settings", path}, "proxy", "ca"); err != nil {
		t.Fatalf("refused heap-size NODE_OPTIONS in --settings: %v", err)
	}
}

func writeLauncherSettings(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestChildEnvironmentAllowsSettingsThatDoNotConflict(t *testing.T) {
	hooks := `{"hooks":{"SessionStart":[{"matcher":"*","hooks":[{"type":"command","command":"/bin/true"}]}]},"preferredNotifChannel":"terminal_bell","env":{"HARMLESS_FLAG":"1"},"model":"sonnet"}`
	path := writeLauncherSettings(t, hooks)
	for _, args := range [][]string{{"--settings", path}, {"--settings=" + path}, {"--settings", hooks}, {"--settings=" + hooks}, {"-p", "x", "--settings", path, "--remote-control"}} {
		if _, err := ChildEnvironment(nil, args, "proxy", "ca"); err != nil {
			t.Errorf("refused harmless settings %v: %v", args[:1], err)
		}
	}
}

func TestChildEnvironmentRefusesSettingsKnownToConflict(t *testing.T) {
	cases := map[string]string{
		"apiKeyHelper":        `{"apiKeyHelper":"/bin/echo sensitive-canary"}`,
		"awsAuthRefresh":      `{"awsAuthRefresh":"aws sso login"}`,
		"awsCredentialExport": `{"awsCredentialExport":"x"}`,
		"gcpAuthRefresh":      `{"gcpAuthRefresh":"x"}`,
		"forceLoginMethod":    `{"forceLoginMethod":"console"}`,
		"forceLoginOrgUUID":   `{"forceLoginOrgUUID":"00000000-0000-0000-0000-000000000000"}`,
		"env base url":        `{"env":{"ANTHROPIC_BASE_URL":"https://sensitive-canary.invalid"}}`,
		"env api key":         `{"env":{"ANTHROPIC_API_KEY":"sensitive-canary"}}`,
		"env bedrock":         `{"env":{"CLAUDE_CODE_USE_BEDROCK":"1"}}`,
		"env https proxy":     `{"env":{"HTTPS_PROXY":"http://sensitive-canary.invalid:1"}}`,
		"env no proxy":        `{"env":{"NO_PROXY":"*"}}`,
		"env extra ca":        `{"env":{"NODE_EXTRA_CA_CERTS":"/tmp/sensitive-canary.pem"}}`,
		"env node options":    `{"env":{"NODE_OPTIONS":"--require /tmp/sensitive-canary.js"}}`,
		"conflict with hooks": `{"hooks":{},"apiKeyHelper":"x"}`,
	}
	for name, content := range cases {
		for form, args := range map[string][]string{"file": {"--settings", writeLauncherSettings(t, content)}, "inline": {"--settings=" + content}} {
			t.Run(name+"/"+form, func(t *testing.T) {
				_, err := ChildEnvironment(nil, args, "proxy", "ca")
				if err == nil {
					t.Fatal("accepted conflicting settings")
				}
				if strings.Contains(err.Error(), "sensitive-canary") {
					t.Fatal("a settings value leaked in the error")
				}
			})
		}
	}
}

func TestChildEnvironmentRefusesSettingsItCannotUnderstand(t *testing.T) {
	dir := t.TempDir()
	big := writeLauncherSettings(t, `{"padding":"`+strings.Repeat("x", launcherSettingsLimit)+`"}`)
	for name, args := range map[string][]string{
		"missing file":           {"--settings", filepath.Join(dir, "absent.json")},
		"a directory":            {"--settings", dir},
		"not json":               {"--settings", writeLauncherSettings(t, "not json")},
		"json but not an object": {"--settings", writeLauncherSettings(t, `["x"]`)},
		"env not an object":      {"--settings", writeLauncherSettings(t, `{"env":"x"}`)},
		"too large":              {"--settings", big},
		"no value":               {"--settings"},
		"bare word":              {"--settings=canary"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ChildEnvironment(nil, args, "proxy", "ca"); err == nil {
				t.Fatal("accepted settings it could not understand")
			}
		})
	}
	for _, flag := range []string{"--setting-sources", "--sdk-url=wss://bad", "--api-key=canary", "--base-url=https://bad", "--cwd=/x", "--worktree", "-w"} {
		if _, err := ChildEnvironment(nil, []string{flag}, "proxy", "ca"); err == nil {
			t.Errorf("accepted flag %s", flag)
		}
	}
}

func TestProcessCertificateOnlyTrustsAnthropicAndHasNoDiskKeys(t *testing.T) {
	certs, err := newProcessCertificate()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(certs.dir) })
	st, err := os.Stat(certs.dir)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatal("certificate directory is not private")
	}
	st, err = os.Stat(certs.caPath)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatal("certificate is not private")
	}
	entries, err := os.ReadDir(certs.dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "ca.pem" {
		t.Fatal("unexpected private key or artifact on disk")
	}
	data, err := os.ReadFile(certs.caPath)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("missing CA")
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(certs.leaf.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "api.anthropic.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "other.example"}); err == nil {
		t.Fatal("certificate covers an unrelated host")
	}
}
