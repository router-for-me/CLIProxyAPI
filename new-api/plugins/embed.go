package plugins

import (
	"embed"
	"encoding/base64"
	"fmt"
	"io/fs"

	"github.com/QuantumNous/new-api/pkg/jsplugin"
)

//go:embed tasks
var taskPlugins embed.FS

func init() {
	entries, err := fs.ReadDir(taskPlugins, "tasks")
	if err != nil {
		panic(fmt.Sprintf("read embedded task plugins: %v", err))
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		key := entry.Name()
		source, sourceErr := Source(key)
		if sourceErr != nil {
			panic(fmt.Sprintf("read embedded task plugin %s: %v", key, sourceErr))
		}
		if _, registerErr := jsplugin.DefaultRegistry.RegisterFactory(source, jsplugin.Options{Key: key}); registerErr != nil {
			panic(fmt.Sprintf("register embedded task plugin %s: %v", key, registerErr))
		}
		if mediaType, data, ok := Icon(key); ok {
			if iconErr := jsplugin.ValidateIconImage(mediaType, data); iconErr != nil {
				panic(fmt.Sprintf("embedded task plugin %s icon: %v", key, iconErr))
			}
		}
	}
}

// Source returns the embedded factory source for a task plugin key.
func Source(key string) (string, error) {
	source, err := taskPlugins.ReadFile("tasks/" + key + "/plugin.js")
	if err != nil {
		return "", err
	}
	return string(source), nil
}

// Icon returns the embedded sidecar logo for a factory plugin, if the plugin
// directory ships an icon.svg or icon.png next to plugin.js.
func Icon(key string) (mediaType string, data []byte, ok bool) {
	if data, err := taskPlugins.ReadFile("tasks/" + key + "/icon.svg"); err == nil {
		return "image/svg+xml", data, true
	}
	if data, err := taskPlugins.ReadFile("tasks/" + key + "/icon.png"); err == nil {
		return "image/png", data, true
	}
	return "", nil, false
}

// IconDataURI returns the embedded factory logo in the same data URI form the
// task_plugins.icon column stores, so factory and override logos share one
// read path.
func IconDataURI(key string) string {
	mediaType, data, ok := Icon(key)
	if !ok {
		return ""
	}
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data)
}
