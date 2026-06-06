package pluginhost

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCandidateDirs(t *testing.T) {
	got := candidateDirs("plugins", "darwin", "arm64", "v3")
	want := []string{
		filepath.Join("plugins", "darwin", "arm64-v3"),
		filepath.Join("plugins", "darwin", "arm64"),
		"plugins",
	}
	if len(got) != len(want) {
		t.Fatalf("len(candidateDirs) = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("candidateDirs[%d] = %q, want %q", index, got[index], want[index])
		}
	}
}

func TestCandidateDirsOmitsEmptyVariant(t *testing.T) {
	got := candidateDirs("plugins", "linux", "arm64", "")
	want := []string{
		filepath.Join("plugins", "linux", "arm64"),
		"plugins",
	}
	if len(got) != len(want) {
		t.Fatalf("len(candidateDirs) = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("candidateDirs[%d] = %q, want %q", index, got[index], want[index])
		}
	}
}

func TestSelectPluginFilesFiltersInvalidIDAndDeduplicatesByID(t *testing.T) {
	root := t.TempDir()
	archDir := filepath.Join(root, runtime.GOOS, runtime.GOARCH)
	if errMkdirAll := os.MkdirAll(archDir, 0o755); errMkdirAll != nil {
		t.Fatalf("MkdirAll() error = %v", errMkdirAll)
	}

	paths := []string{
		filepath.Join(root, "sample.so"),
		filepath.Join(archDir, "sample.so"),
		filepath.Join(archDir, "bad name.so"),
		filepath.Join(archDir, "-bad.so"),
		filepath.Join(archDir, "another.SO"),
		filepath.Join(archDir, "ignored.txt"),
	}
	for _, path := range paths {
		if errWriteFile := os.WriteFile(path, []byte("x"), 0o644); errWriteFile != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, errWriteFile)
		}
	}
	if errMkdir := os.Mkdir(filepath.Join(archDir, "dir.so"), 0o755); errMkdir != nil {
		t.Fatalf("Mkdir() error = %v", errMkdir)
	}

	files, errSelect := selectPluginFiles(root)
	if errSelect != nil {
		t.Fatalf("selectPluginFiles() error = %v", errSelect)
	}

	want := []pluginFile{
		{ID: "another", Path: filepath.Join(archDir, "another.SO")},
		{ID: "sample", Path: filepath.Join(archDir, "sample.so")},
	}
	if len(files) != len(want) {
		t.Fatalf("selectPluginFiles() = %v, want %v", files, want)
	}
	for index := range want {
		if files[index] != want[index] {
			t.Fatalf("selectPluginFiles()[%d] = %v, want %v", index, files[index], want[index])
		}
	}
}

func TestSelectPluginFilesPrefersPlatformDirOverRootFallback(t *testing.T) {
	root := t.TempDir()
	archDir := filepath.Join(root, runtime.GOOS, runtime.GOARCH)
	if errMkdirAll := os.MkdirAll(archDir, 0o755); errMkdirAll != nil {
		t.Fatalf("MkdirAll() error = %v", errMkdirAll)
	}

	platformPath := filepath.Join(archDir, "alpha.so")
	rootPath := filepath.Join(root, "alpha.so")
	for _, path := range []string{rootPath, platformPath} {
		if errWriteFile := os.WriteFile(path, []byte("x"), 0o644); errWriteFile != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, errWriteFile)
		}
	}

	files, errSelect := selectPluginFiles(root)
	if errSelect != nil {
		t.Fatalf("selectPluginFiles() error = %v", errSelect)
	}
	if len(files) != 1 {
		t.Fatalf("selectPluginFiles() = %v, want exactly one alpha plugin", files)
	}
	if files[0] != (pluginFile{ID: "alpha", Path: platformPath}) {
		t.Fatalf("selectPluginFiles()[0] = %v, want platform plugin %s", files[0], platformPath)
	}
}

func TestDiscoverPluginFilesReturnsSelectedPluginFiles(t *testing.T) {
	root := makePluginDir(t, "alpha")

	files, errDiscover := DiscoverPluginFiles(root)
	if errDiscover != nil {
		t.Fatalf("DiscoverPluginFiles() error = %v", errDiscover)
	}

	if len(files) != 1 || files[0].ID != "alpha" || files[0].Path == "" {
		t.Fatalf("DiscoverPluginFiles() = %#v, want alpha file", files)
	}
}

func TestSelectPluginFilesPrefersCPUVariantOverGenericArchDir(t *testing.T) {
	variant := cpuVariant()
	if variant == "" {
		t.Skip("current GOARCH has no plugin CPU variant")
	}
	root := t.TempDir()
	archDir := filepath.Join(root, runtime.GOOS, runtime.GOARCH)
	variantDir := filepath.Join(root, runtime.GOOS, runtime.GOARCH+"-"+variant)
	for _, dir := range []string{archDir, variantDir} {
		if errMkdirAll := os.MkdirAll(dir, 0o755); errMkdirAll != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, errMkdirAll)
		}
	}

	genericPath := filepath.Join(archDir, "alpha.so")
	variantPath := filepath.Join(variantDir, "alpha.so")
	for _, path := range []string{genericPath, variantPath} {
		if errWriteFile := os.WriteFile(path, []byte("x"), 0o644); errWriteFile != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, errWriteFile)
		}
	}

	files, errSelect := selectPluginFiles(root)
	if errSelect != nil {
		t.Fatalf("selectPluginFiles() error = %v", errSelect)
	}
	if len(files) != 1 {
		t.Fatalf("selectPluginFiles() = %v, want exactly one alpha plugin", files)
	}
	if files[0] != (pluginFile{ID: "alpha", Path: variantPath}) {
		t.Fatalf("selectPluginFiles()[0] = %v, want CPU variant plugin %s", files[0], variantPath)
	}
}
