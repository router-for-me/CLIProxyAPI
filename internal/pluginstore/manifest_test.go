package pluginstore

import "testing"

func TestManifestPluginNormalizesPluginLocales(t *testing.T) {
	t.Parallel()

	manifest := Manifest{
		ID:          "sample-provider",
		Name:        "Sample Provider",
		Description: "Adds sample provider support.",
		Author:      "author-name",
		Version:     "0.1.0",
		Repository:  "https://github.com/author-name/cliproxy-sample-provider-plugin",
		Locales: map[string]PluginLocale{
			" zh_CN ": {
				Name:        " 示例插件 ",
				Description: " 增加示例提供商支持。 ",
				Author:      " 作者 ",
				Homepage:    " https://example.com/zh ",
				License:     " MIT ",
				Tags:        []string{" 工具 "},
			},
		},
	}

	plugin := manifest.Plugin()
	localized, ok := plugin.Locales["zh-cn"]
	if !ok {
		t.Fatalf("plugin locales = %#v, want zh-cn", plugin.Locales)
	}
	if localized.Name != "示例插件" || localized.Description != "增加示例提供商支持。" ||
		localized.Author != "作者" || localized.Homepage != "https://example.com/zh" ||
		localized.License != "MIT" || len(localized.Tags) != 1 || localized.Tags[0] != "工具" {
		t.Fatalf("localized plugin = %#v, want trimmed locale fields", localized)
	}

	manifest.Locales[" zh_CN "].Tags[0] = "mutated"
	if plugin.Locales["zh-cn"].Tags[0] != "工具" {
		t.Fatalf("plugin locale tags shared backing array: %#v", plugin.Locales["zh-cn"].Tags)
	}
}
