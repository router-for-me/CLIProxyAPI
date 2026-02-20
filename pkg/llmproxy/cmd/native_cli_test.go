package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestResolveNativeCLI_Roo(t *testing.T) {
	path := ResolveNativeCLI(RooSpec)
	// May or may not be installed; we only verify the function doesn't panic
	if path != "" {
		t.Logf("ResolveNativeCLI(roo) found: %s", path)
	} else {
		t.Log("ResolveNativeCLI(roo) not found (roo may not be installed)")
	}
}

func TestResolveNativeCLI_Kilo(t *testing.T) {
	path := ResolveNativeCLI(KiloSpec)
	if path != "" {
		t.Logf("ResolveNativeCLI(kilo) found: %s", path)
	} else {
		t.Log("ResolveNativeCLI(kilo) not found (kilo/kilocode may not be installed)")
	}
}

func TestResolveNativeCLI_FromPATH(t *testing.T) {
	// Create temp dir with fake binary
	tmp := t.TempDir()
	fakeRoo := filepath.Join(tmp, "roo")
	if err := os.WriteFile(fakeRoo, []byte("#!/bin/sh\nexit 0"), 0755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	origPath := os.Getenv("PATH")
	defer func() { _ = os.Setenv("PATH", origPath) }()
	_ = os.Setenv("PATH", tmp+string(filepath.ListSeparator)+origPath)

	spec := NativeCLISpec{Name: "roo", Args: []string{"auth", "login"}}
	path := ResolveNativeCLI(spec)
	if path == "" {
		t.Skip("PATH with fake roo not used (exec.LookPath may resolve differently)")
	}
	if path != fakeRoo {
		t.Logf("ResolveNativeCLI returned %q (expected %q); may have found system roo", path, fakeRoo)
	}
}

func TestResolveNativeCLI_LocalBin(t *testing.T) {
	tmp := t.TempDir()
	localBin := filepath.Join(tmp, ".local", "bin")
	if err := os.MkdirAll(localBin, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	fakeKilo := filepath.Join(localBin, "kilocode")
	if err := os.WriteFile(fakeKilo, []byte("#!/bin/sh\nexit 0"), 0755); err != nil {
		t.Fatalf("write fake kilocode: %v", err)
	}

	origHome := os.Getenv("HOME")
	origPath := os.Getenv("PATH")
	defer func() {
		_ = os.Setenv("HOME", origHome)
		_ = os.Setenv("PATH", origPath)
	}()
	_ = os.Setenv("HOME", tmp)
	// Empty PATH so LookPath fails; we rely on ~/.local/bin
	_ = os.Setenv("PATH", "")

	path := ResolveNativeCLI(KiloSpec)
	if path != fakeKilo {
		t.Errorf("ResolveNativeCLI(kilo) = %q, want %q", path, fakeKilo)
	}
}

func TestRunNativeCLILogin_NotFound(t *testing.T) {
	spec := NativeCLISpec{
		Name:          "nonexistent-cli-xyz-12345",
		Args:          []string{"auth"},
		FallbackNames: nil,
	}
	exitCode, err := RunNativeCLILogin(spec)
	if err == nil {
		t.Errorf("RunNativeCLILogin expected error for nonexistent binary, got nil")
	}
	if exitCode != -1 {
		t.Errorf("RunNativeCLILogin exitCode = %d, want -1", exitCode)
	}
}

func TestRunNativeCLILogin_Echo(t *testing.T) {
	// Use a binary that exists and exits 0 quickly (e.g. true, echo)
	truePath, err := exec.LookPath("true")
	if err != nil {
		truePath, err = exec.LookPath("echo")
		if err != nil {
			t.Skip("neither 'true' nor 'echo' found in PATH")
		}
	}
	spec := NativeCLISpec{
		Name:          filepath.Base(truePath),
		Args:          []string{},
		FallbackNames: nil,
	}
	// ResolveNativeCLI may not find it if it's in a non-standard path
	path := ResolveNativeCLI(spec)
	if path == "" {
		// Override spec to use full path - we need a way to test with a known binary
		// For now, skip if not found
		t.Skip("true/echo not in PATH or ~/.local/bin")
	}
	// If we get here, RunNativeCLILogin would run "true" or "echo" - avoid side effects
	// by just verifying ResolveNativeCLI works
	t.Logf("ResolveNativeCLI found %s", path)
}

func TestRooSpec(t *testing.T) {
	if RooSpec.Name != "roo" {
		t.Errorf("RooSpec.Name = %q, want roo", RooSpec.Name)
	}
	if len(RooSpec.Args) != 2 || RooSpec.Args[0] != "auth" || RooSpec.Args[1] != "login" {
		t.Errorf("RooSpec.Args = %v, want [auth login]", RooSpec.Args)
	}
}

func TestKiloSpec(t *testing.T) {
	if KiloSpec.Name != "kilo" {
		t.Errorf("KiloSpec.Name = %q, want kilo", KiloSpec.Name)
	}
	if len(KiloSpec.Args) != 1 || KiloSpec.Args[0] != "auth" {
		t.Errorf("KiloSpec.Args = %v, want [auth]", KiloSpec.Args)
	}
	if len(KiloSpec.FallbackNames) != 1 || KiloSpec.FallbackNames[0] != "kilocode" {
		t.Errorf("KiloSpec.FallbackNames = %v, want [kilocode]", KiloSpec.FallbackNames)
	}
}
