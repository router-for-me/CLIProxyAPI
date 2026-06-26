package pluginhost

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

func pluginLogFields(id, name, version, path string) log.Fields {
	fields := log.Fields{
		"plugin_id": strings.TrimSpace(id),
	}
	if name = strings.TrimSpace(name); name != "" {
		fields["plugin_name"] = name
	}
	if version = strings.TrimSpace(version); version != "" {
		fields["version"] = version
	}
	if path = strings.TrimSpace(path); path != "" {
		fields["path"] = path
	}
	return fields
}

func pluginLogFieldsFromMetadata(id string, meta pluginapi.Metadata, path string) log.Fields {
	return pluginLogFields(id, meta.Name, meta.Version, path)
}
