package main

import (
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func buildPlugin(configYAML []byte, pluginDir string) (pluginapi.Plugin, error) {
	cfg, errParse := parseJSHandlerConfig(configYAML)
	if errParse != nil {
		return pluginapi.Plugin{}, errParse
	}
	if pluginDir == "" {
		pluginDir = inferPluginDir()
	}
	p := &jsHandlerPlugin{
		cfg:        cfg,
		configYAML: append([]byte(nil), configYAML...),
		pluginDir:  pluginDir,
	}
	return pluginapi.Plugin{
		Metadata: pluginapi.Metadata{
			Name:             pluginName,
			Version:          "0.1.0",
			Author:           "router-for-me",
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			ConfigFields: []pluginapi.ConfigField{
				{
					Name:        "enabled",
					Type:        pluginapi.ConfigFieldTypeBoolean,
					Description: "Enable or disable the JS handler plugin.",
				},
				{
					Name:        "script_paths",
					Type:        pluginapi.ConfigFieldTypeArray,
					Description: "List of JS script file paths to load (absolute or relative to plugin directory).",
				},
				{
					Name:        "timeout",
					Type:        pluginapi.ConfigFieldTypeString,
					Description: "Execution timeout per JS hook call as a Go duration, such as 1s.",
				},
			},
		},
		Capabilities: pluginapi.Capabilities{
			RequestInterceptor:     p,
			ResponseInterceptor:    p,
			StreamChunkInterceptor: p,
		},
	}, nil
}
