// Integration tests for -roo-login and -kilo-login flags.
// Runs the cliproxyapi-plusplus binary with fake roo/kilo in PATH.
package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func findOrBuildBinary(t *testing.T) string {
	t.Helper()
	// Prefer existing binary in repo root
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// When running from test/, parent is repo root
	repoRoot := filepath.Dir(wd)
	if filepath.Base(wd) != "test" {
		repoRoot = wd
	}
	binary := filepath.Join(repoRoot, "cli-proxy-api-plus")
	if info, err := os.Stat(binary); err == nil && !info.IsDir() {
		return binary
	}
	// Build it
	out := filepath.Join(repoRoot, "cli-proxy-api-plus-integration-test")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/server")
	cmd.Dir = repoRoot
	if outB, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build binary: %v\n%s", err, outB)
	}
	return out
}

func TestRooLoginFlag_WithFakeRoo(t *testing.T) {
	binary := findOrBuildBinary(t)
	tmp := t.TempDir()
	fakeRoo := filepath.Join(tmp, "roo")
	script := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(fakeRoo, []byte(script), 0755); err != nil {
		t.Fatalf("write fake roo: %v", err)
	}
	origPath := os.Getenv("PATH")
	defer func() { _ = os.Setenv("PATH", origPath) }()
	_ = os.Setenv("PATH", tmp+string(filepath.ListSeparator)+origPath)

	cmd := exec.Command(binary, "-roo-login")
	cmd.Env = append(os.Environ(), "PATH="+tmp+string(filepath.ListSeparator)+origPath)
	cmd.Stdout = nil
	cmd.Stderr = nil
	err := cmd.Run()
	if err != nil {
		t.Errorf("-roo-login with fake roo in PATH: %v", err)
	}
}

func TestKiloLoginFlag_WithFakeKilo(t *testing.T) {
	binary := findOrBuildBinary(t)
	tmp := t.TempDir()
	fakeKilo := filepath.Join(tmp, "kilo")
	script := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(fakeKilo, []byte(script), 0755); err != nil {
		t.Fatalf("write fake kilo: %v", err)
	}
	origPath := os.Getenv("PATH")
	defer func() { _ = os.Setenv("PATH", origPath) }()
	_ = os.Setenv("PATH", tmp+string(filepath.ListSeparator)+origPath)

	cmd := exec.Command(binary, "-kilo-login")
	cmd.Env = append(os.Environ(), "PATH="+tmp+string(filepath.ListSeparator)+origPath)
	cmd.Stdout = nil
	cmd.Stderr = nil
	err := cmd.Run()
	if err != nil {
		t.Errorf("-kilo-login with fake kilo in PATH: %v", err)
	}
}

func TestRooLoginFlag_WithoutRoo_ExitsNonZero(t *testing.T) {
	binary := findOrBuildBinary(t)
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8317\n"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	// Empty PATH + temp HOME with no ~/.local/bin/roo so roo is not found
	env := make([]string, 0, len(os.Environ())+3)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "PATH=") && !strings.HasPrefix(e, "HOME=") {
			env = append(env, e)
		}
	}
	env = append(env, "PATH=", "HOME="+tmp)
	cmd := exec.Command(binary, "-config", configPath, "-roo-login")
	cmd.Env = env
	cmd.Stdout = nil
	cmd.Stderr = nil
	err := cmd.Run()
	if err == nil {
		t.Error("-roo-login without roo in PATH or ~/.local/bin should exit non-zero")
	}
}
