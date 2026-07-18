//go:build windows

package pluginhost

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShadowPluginDirIsProcessScoped(t *testing.T) {
	dir, errDir := shadowPluginDir()
	if errDir != nil {
		t.Fatalf("shadowPluginDir() error = %v", errDir)
	}
	want := filepath.Join(os.TempDir(), "cliproxy-pluginhost", fmt.Sprintf("pid-%d", os.Getpid()))
	if dir != want {
		t.Fatalf("shadowPluginDir() = %q, want %q", dir, want)
	}
}

func TestShadowCopyPluginReusesContentAddressedShadow(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(t.TempDir(), "alpha.dll")
	content := []byte("plugin-v1")
	if errWrite := os.WriteFile(source, content, 0o644); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	file := pluginFile{ID: "alpha", Path: source}

	first, errFirst := shadowCopyPluginToDir(file, dir)
	if errFirst != nil {
		t.Fatalf("shadowCopyPluginToDir() first error = %v", errFirst)
	}
	second, errSecond := shadowCopyPluginToDir(file, dir)
	if errSecond != nil {
		t.Fatalf("shadowCopyPluginToDir() second error = %v", errSecond)
	}

	if second != first {
		t.Fatalf("second shadow path = %q, want reused path %q", second, first)
	}
	gotContent, errRead := os.ReadFile(first)
	if errRead != nil {
		t.Fatalf("ReadFile(%s) error = %v", first, errRead)
	}
	if string(gotContent) != string(content) {
		t.Fatalf("shadow content = %q, want %q", gotContent, content)
	}
	digest := sha256.Sum256(content)
	wantDigest := hex.EncodeToString(digest[:])[:shadowPluginDigestLength]
	name := filepath.Base(first)
	if !strings.HasPrefix(name, shadowPluginPrefix+"alpha-") || !strings.Contains(name, wantDigest) {
		t.Fatalf("shadow file name = %q, want alpha content digest %s", name, wantDigest)
	}
	if count := countShadowPluginFiles(t, dir); count != 1 {
		t.Fatalf("shadow file count = %d, want 1", count)
	}
}

func TestShadowCopyPluginCreatesNewPathForChangedContent(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(t.TempDir(), "alpha.dll")
	file := pluginFile{ID: "alpha", Path: source}
	if errWrite := os.WriteFile(source, []byte("plugin-v1"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile() v1 error = %v", errWrite)
	}
	first, errFirst := shadowCopyPluginToDir(file, dir)
	if errFirst != nil {
		t.Fatalf("shadowCopyPluginToDir() v1 error = %v", errFirst)
	}

	if errWrite := os.WriteFile(source, []byte("plugin-v2"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile() v2 error = %v", errWrite)
	}
	second, errSecond := shadowCopyPluginToDir(file, dir)
	if errSecond != nil {
		t.Fatalf("shadowCopyPluginToDir() v2 error = %v", errSecond)
	}

	if second == first {
		t.Fatalf("second shadow path reused %q after content changed", second)
	}
	if count := countShadowPluginFiles(t, dir); count != 2 {
		t.Fatalf("shadow file count = %d, want 2 versions", count)
	}
}

func TestShadowCopyPluginReplacesCorruptSameSizeShadow(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(t.TempDir(), "alpha.dll")
	content := []byte("plugin-v1")
	if errWrite := os.WriteFile(source, content, 0o644); errWrite != nil {
		t.Fatalf("WriteFile() source error = %v", errWrite)
	}
	digest := sha256.Sum256(content)
	target := shadowPluginPath(dir, "alpha", hex.EncodeToString(digest[:]), ".dll")
	if errWrite := os.WriteFile(target, []byte("corrupt!!"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile() corrupt shadow error = %v", errWrite)
	}

	gotPath, errCopy := shadowCopyPluginToDir(pluginFile{ID: "alpha", Path: source}, dir)
	if errCopy != nil {
		t.Fatalf("shadowCopyPluginToDir() error = %v", errCopy)
	}

	if gotPath != target {
		t.Fatalf("shadow path = %q, want %q", gotPath, target)
	}
	gotContent, errRead := os.ReadFile(target)
	if errRead != nil {
		t.Fatalf("ReadFile(%s) error = %v", target, errRead)
	}
	if string(gotContent) != string(content) {
		t.Fatalf("shadow content = %q, want %q", gotContent, content)
	}
	if count := countShadowPluginFiles(t, dir); count != 1 {
		t.Fatalf("shadow file count = %d, want 1", count)
	}
}

func TestRemoveStaleShadowPluginsOnlyRemovesShadowFiles(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, shadowPluginPrefix+"alpha-deadbeef.dll")
	temp := filepath.Join(dir, shadowPluginTempPrefix+"alpha-temp.dll")
	keep := filepath.Join(dir, "keep.dll")
	for _, path := range []string{stale, temp, keep} {
		if errWrite := os.WriteFile(path, []byte("x"), 0o644); errWrite != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, errWrite)
		}
	}

	removeStaleShadowPlugins(dir)

	for _, path := range []string{stale, temp} {
		if _, errStat := os.Stat(path); !os.IsNotExist(errStat) {
			t.Fatalf("Stat(%s) error = %v, want not exist", path, errStat)
		}
	}
	if _, errStat := os.Stat(keep); errStat != nil {
		t.Fatalf("Stat(%s) error = %v, want kept", keep, errStat)
	}
}

func countShadowPluginFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, errRead := os.ReadDir(dir)
	if errRead != nil {
		t.Fatalf("ReadDir(%s) error = %v", dir, errRead)
	}
	count := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), shadowPluginPrefix) {
			count++
		}
		if strings.HasPrefix(entry.Name(), shadowPluginTempPrefix) {
			t.Fatalf("temporary shadow file was not cleaned up: %s", entry.Name())
		}
	}
	return count
}
