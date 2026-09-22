package pluginhost

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCandidateDirs(t *testing.T) {
	got := candidateDirs("plugins", "darwin", "arm64")
	want := []string{
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

func TestPluginExtensionForPlatform(t *testing.T) {
	cases := []struct {
		goos string
		want string
	}{
		{goos: "linux", want: ".so"},
		{goos: "freebsd", want: ".so"},
		{goos: "darwin", want: ".dylib"},
		{goos: "windows", want: ".dll"},
	}

	for _, tc := range cases {
		if got := pluginExtension(tc.goos); got != tc.want {
			t.Fatalf("pluginExtension(%q) = %q, want %q", tc.goos, got, tc.want)
		}
	}
}

func TestPluginIDFromDynamicLibraryPath(t *testing.T) {
	cases := map[string]string{
		"plugins/example.so":     "example",
		"plugins/example.dylib":  "example",
		"plugins/example.dll":    "example",
		"plugins/example.custom": "example.custom",
	}

	for path, want := range cases {
		if got := pluginIDFromPath(path); got != want {
			t.Fatalf("pluginIDFromPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestSelectPluginFilesFiltersInvalidIDAndDeduplicatesByID(t *testing.T) {
	root := t.TempDir()
	archDir := filepath.Join(root, runtime.GOOS, runtime.GOARCH)
	if errMkdirAll := os.MkdirAll(archDir, 0o755); errMkdirAll != nil {
		t.Fatalf("MkdirAll() error = %v", errMkdirAll)
	}

	extension := pluginExtension(runtime.GOOS)
	paths := []string{
		filepath.Join(root, "sample"+extension),
		filepath.Join(archDir, "sample"+extension),
		filepath.Join(archDir, "bad name"+extension),
		filepath.Join(archDir, "-bad"+extension),
		filepath.Join(archDir, "another"+strings.ToUpper(extension)),
		filepath.Join(archDir, "ignored.txt"),
	}
	for _, path := range paths {
		if errWriteFile := os.WriteFile(path, []byte("x"), 0o644); errWriteFile != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, errWriteFile)
		}
	}
	if errMkdir := os.Mkdir(filepath.Join(archDir, "dir"+extension), 0o755); errMkdir != nil {
		t.Fatalf("Mkdir() error = %v", errMkdir)
	}

	files, errSelect := selectPluginFiles(root)
	if errSelect != nil {
		t.Fatalf("selectPluginFiles() error = %v", errSelect)
	}

	want := []pluginFile{
		{ID: "another", Path: filepath.Join(archDir, "another"+strings.ToUpper(extension))},
		{ID: "sample", Path: filepath.Join(archDir, "sample"+extension)},
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

	extension := pluginExtension(runtime.GOOS)
	platformPath := filepath.Join(archDir, "alpha"+extension)
	rootPath := filepath.Join(root, "alpha"+extension)
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

func TestSelectPluginFilesPrefersConfiguredVersionOverHigherVersion(t *testing.T) {
	root := t.TempDir()
	archDir := filepath.Join(root, runtime.GOOS, runtime.GOARCH)
	if errMkdirAll := os.MkdirAll(archDir, 0o755); errMkdirAll != nil {
		t.Fatalf("MkdirAll() error = %v", errMkdirAll)
	}

	extension := pluginExtension(runtime.GOOS)
	olderPath := filepath.Join(archDir, "alpha-v1.0.3"+extension)
	newerPath := filepath.Join(archDir, "alpha-v1.0.4"+extension)
	for _, path := range []string{olderPath, newerPath} {
		if errWriteFile := os.WriteFile(path, []byte("x"), 0o644); errWriteFile != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, errWriteFile)
		}
	}

	files, errSelect := selectPluginFiles(root, map[string]string{"alpha": "1.0.3"})
	if errSelect != nil {
		t.Fatalf("selectPluginFiles() error = %v", errSelect)
	}
	if len(files) != 1 {
		t.Fatalf("selectPluginFiles() = %v, want exactly one alpha plugin", files)
	}
	if files[0] != (pluginFile{ID: "alpha", Path: olderPath, Version: "1.0.3"}) {
		t.Fatalf("selectPluginFiles()[0] = %v, want configured plugin %s", files[0], olderPath)
	}
}

func TestSelectPluginFilesFallsBackToHighestVersionWithoutConfiguredVersion(t *testing.T) {
	root := t.TempDir()
	archDir := filepath.Join(root, runtime.GOOS, runtime.GOARCH)
	if errMkdirAll := os.MkdirAll(archDir, 0o755); errMkdirAll != nil {
		t.Fatalf("MkdirAll() error = %v", errMkdirAll)
	}

	extension := pluginExtension(runtime.GOOS)
	olderPath := filepath.Join(archDir, "alpha-v1.0.3"+extension)
	newerPath := filepath.Join(archDir, "alpha-v1.0.4"+extension)
	for _, path := range []string{olderPath, newerPath} {
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
	if files[0] != (pluginFile{ID: "alpha", Path: newerPath, Version: "1.0.4"}) {
		t.Fatalf("selectPluginFiles()[0] = %v, want highest plugin %s", files[0], newerPath)
	}
}

func TestSelectPluginFilesSkipsPluginWhenConfiguredVersionIsMissing(t *testing.T) {
	root := t.TempDir()
	archDir := filepath.Join(root, runtime.GOOS, runtime.GOARCH)
	if errMkdirAll := os.MkdirAll(archDir, 0o755); errMkdirAll != nil {
		t.Fatalf("MkdirAll() error = %v", errMkdirAll)
	}

	extension := pluginExtension(runtime.GOOS)
	path := filepath.Join(archDir, "alpha-v1.0.4"+extension)
	if errWriteFile := os.WriteFile(path, []byte("x"), 0o644); errWriteFile != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, errWriteFile)
	}

	files, errSelect := selectPluginFiles(root, map[string]string{"alpha": "1.0.3"})
	if errSelect != nil {
		t.Fatalf("selectPluginFiles() error = %v", errSelect)
	}
	if len(files) != 0 {
		t.Fatalf("selectPluginFiles() = %v, want no selected alpha plugin", files)
	}
}
