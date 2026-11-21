package pluginhost

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/bundledplugins"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// contentRecordingLoader records the library content each plugin had when the
// host opened it.
type contentRecordingLoader struct {
	opened map[string]string
}

func (l *contentRecordingLoader) Open(file pluginFile, _ *Host) (pluginClient, error) {
	raw, err := os.ReadFile(file.Path)
	if err != nil {
		return nil, err
	}
	l.opened[file.ID] = string(raw)
	return nil, fmt.Errorf("test loader does not load %s", file.ID)
}

// After an upgrade the plugins directory still holds the previous bundled
// copy; the first ApplyConfig of the process must already open the new one.
func TestApplyConfigInstallsBundledPluginsBeforeOpening(t *testing.T) {
	source := t.TempDir()
	pluginsDir := t.TempDir()
	library := filepath.Join(source, runtime.GOOS, runtime.GOARCH, "guard"+pluginExtension(runtime.GOOS))
	if err := os.MkdirAll(filepath.Dir(library), 0o755); err != nil {
		t.Fatal(err)
	}
	writeLibrary := func(content string) {
		t.Helper()
		if err := os.WriteFile(library, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeLibrary("old")
	if _, err := bundledplugins.Install(source, pluginsDir); err != nil {
		t.Fatalf("install previous version: %v", err)
	}
	writeLibrary("new")
	t.Setenv(bundledplugins.SourceDirEnv, source)

	loader := &contentRecordingLoader{opened: map[string]string{}}
	enabled := true
	NewForTest(loader).ApplyConfig(context.Background(), &config.Config{Plugins: config.PluginsConfig{
		Enabled: true,
		Dir:     pluginsDir,
		Configs: map[string]config.PluginInstanceConfig{"guard": {Enabled: &enabled}},
	}})

	if got := loader.opened["guard"]; got != "new" {
		t.Fatalf("opened library content = %q, want the bundled update", got)
	}
}
