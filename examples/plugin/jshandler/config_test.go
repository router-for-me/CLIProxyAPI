package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvedScriptPathsRejectsRelativeSymlinkEscapingPluginDir(t *testing.T) {
	pluginDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideScript := filepath.Join(outsideDir, "handler.js")
	if errWrite := os.WriteFile(outsideScript, []byte("function on_before_request(ctx) { return ctx; }\n"), 0600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	linkPath := filepath.Join(pluginDir, "handler.js")
	if errSymlink := os.Symlink(outsideScript, linkPath); errSymlink != nil {
		t.Skipf("os.Symlink() is not available: %v", errSymlink)
	}

	cfg := jsHandlerConfig{ScriptPaths: []string{"handler.js"}}
	_, errResolve := cfg.resolvedScriptPaths(pluginDir)
	if errResolve == nil {
		t.Fatal("resolvedScriptPaths() expected error for escaping symlink")
	}
	if !strings.Contains(errResolve.Error(), "escapes plugin_dir") {
		t.Fatalf("resolvedScriptPaths() error = %v, want escapes plugin_dir", errResolve)
	}
}

func TestResolvedScriptPathsAllowsRelativeSymlinkInsidePluginDir(t *testing.T) {
	pluginDir := t.TempDir()
	scriptsDir := filepath.Join(pluginDir, "scripts")
	if errMkdir := os.Mkdir(scriptsDir, 0700); errMkdir != nil {
		t.Fatalf("os.Mkdir() error = %v", errMkdir)
	}
	realScript := filepath.Join(scriptsDir, "handler.js")
	if errWrite := os.WriteFile(realScript, []byte("function on_before_request(ctx) { return ctx; }\n"), 0600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	linkPath := filepath.Join(pluginDir, "handler.js")
	if errSymlink := os.Symlink(realScript, linkPath); errSymlink != nil {
		t.Skipf("os.Symlink() is not available: %v", errSymlink)
	}

	cfg := jsHandlerConfig{ScriptPaths: []string{"handler.js"}}
	paths, errResolve := cfg.resolvedScriptPaths(pluginDir)
	if errResolve != nil {
		t.Fatalf("resolvedScriptPaths() error = %v", errResolve)
	}
	if len(paths) != 1 {
		t.Fatalf("resolvedScriptPaths() returned %d paths, want 1", len(paths))
	}
	resolvedRealScript, errEval := filepath.EvalSymlinks(realScript)
	if errEval != nil {
		t.Fatalf("filepath.EvalSymlinks() error = %v", errEval)
	}
	if paths[0] != resolvedRealScript {
		t.Fatalf("resolvedScriptPaths()[0] = %q, want %q", paths[0], resolvedRealScript)
	}
}
