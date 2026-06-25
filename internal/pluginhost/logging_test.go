package pluginhost

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestPluginLogFieldsIncludesNameVersionAndPath(t *testing.T) {
	fields := pluginLogFieldsFromMetadata("sample", pluginapi.Metadata{
		Name:    "Sample Provider",
		Version: "0.2.0",
	}, "/tmp/plugins/sample-v0.2.0.dll")

	if fields["plugin_id"] != "sample" {
		t.Fatalf("plugin_id = %v, want sample", fields["plugin_id"])
	}
	if fields["plugin_name"] != "Sample Provider" {
		t.Fatalf("plugin_name = %v, want Sample Provider", fields["plugin_name"])
	}
	if fields["version"] != "0.2.0" {
		t.Fatalf("version = %v, want 0.2.0", fields["version"])
	}
	if fields["path"] != "/tmp/plugins/sample-v0.2.0.dll" {
		t.Fatalf("path = %v, want /tmp/plugins/sample-v0.2.0.dll", fields["path"])
	}
}

func TestPluginLogFieldsOmitsEmptyName(t *testing.T) {
	fields := pluginLogFields("sample", "", "0.2.0", "")
	if _, ok := fields["plugin_name"]; ok {
		t.Fatalf("plugin_name = %v, want omitted", fields["plugin_name"])
	}
}
