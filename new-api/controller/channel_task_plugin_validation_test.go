package controller

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateTaskPluginChannel(t *testing.T) {
	source := `
export const meta = {apiVersion: 1, key: "channel-validation", name: "Validation", version: "1.0.0", author: {name: "Test"}, models: ["doc"], fetchMode: "per_task"};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`
	_, err := jsplugin.DefaultRegistry.Register(source, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister("channel-validation") })
	baseURL := "https://example.com"

	channel := &model.Channel{Type: constant.ChannelTypeTaskPlugin, BaseURL: &baseURL}
	require.ErrorContains(t, validateChannel(channel, false), "task plugin key is required")

	missing := `{"task_plugin_key":"missing"}`
	channel.Setting = &missing
	require.ErrorContains(t, validateChannel(channel, false), "is not registered")

	longKey := `{"task_plugin_key":"` + strings.Repeat("x", 31) + `"}`
	channel.Setting = &longKey
	require.ErrorContains(t, validateChannel(channel, false), "must not exceed 30")

	valid := `{"task_plugin_key":"channel-validation"}`
	channel.Setting = &valid
	channel.BaseURL = nil
	require.ErrorContains(t, validateChannel(channel, false), "base URL is required")
}

func TestValidateTaskPluginChannelFillsPluginDefaultBaseURL(t *testing.T) {
	source := `
export const meta = {apiVersion: 1, key: "channel-default-url", name: "Default URL", version: "1.0.0", author: {name: "Test"}, baseUrl: "http://127.0.0.1:8000/", models: ["doc"], fetchMode: "per_task"};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`
	_, err := jsplugin.DefaultRegistry.Register(source, jsplugin.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister("channel-default-url") })
	bound := `{"task_plugin_key":"channel-default-url"}`

	empty := "  "
	for _, baseURL := range []*string{nil, &empty} {
		channel := &model.Channel{Type: constant.ChannelTypeTaskPlugin, Key: "sk", Setting: &bound, BaseURL: baseURL}
		require.NoError(t, validateChannel(channel, true))
		require.NotNil(t, channel.BaseURL)
		assert.Equal(t, "http://127.0.0.1:8000", *channel.BaseURL, "normalized plugin default is persisted onto the channel")
	}

	explicit := "https://override.example.com"
	channel := &model.Channel{Type: constant.ChannelTypeTaskPlugin, Setting: &bound, BaseURL: &explicit}
	require.NoError(t, validateChannel(channel, false))
	assert.Equal(t, explicit, *channel.BaseURL, "an administrator value is never replaced by the plugin default")
}

func TestValidateNewAPIChannelTaskPluginExtensions(t *testing.T) {
	// gateway-ext-vendor never declares new_api, so it can only run against
	// its vendor and must be refused on a New API channel.
	for key, upstreams := range map[string]string{"gateway-ext-a": `upstreams: ["vendor", "new_api"],`, "gateway-ext-b": `upstreams: ["new_api"],`, "gateway-ext-vendor": ""} {
		source := `
export const meta = {apiVersion: 1, key: "` + key + `", name: "Gateway", version: "1.0.0", author: {name: "Test"}, models: ["doc-` + key + `"], fetchMode: "per_task", ` + upstreams + `};
export function buildSubmitRequest() { return {}; }
export function parseSubmitResponse() { return {}; }
export function buildQueryRequest() { return {}; }
export function parseTaskResult() { return {}; }
`
		_, err := jsplugin.DefaultRegistry.Register(source, jsplugin.Options{})
		require.NoError(t, err)
		t.Cleanup(func() { jsplugin.DefaultRegistry.Unregister(key) })
	}
	baseURL := "https://gateway.example"
	cases := []struct {
		name, setting, wantErr string
	}{
		{"single and extension keys", `{"task_plugin_key":"gateway-ext-a","task_extend_plugin_keys":["gateway-ext-b"]}`, ""},
		{"extension keys only", `{"task_extend_plugin_keys":["gateway-ext-a","gateway-ext-b"]}`, ""},
		{"unregistered", `{"task_extend_plugin_keys":["gateway-ext-a","missing"]}`, `task plugin "missing" is not registered`},
		{"vendor-only plugin as extension", `{"task_extend_plugin_keys":["gateway-ext-a","gateway-ext-vendor"]}`, `task plugin "gateway-ext-vendor" does not support a New API upstream`},
		{"vendor-only plugin as single key", `{"task_plugin_key":"gateway-ext-vendor"}`, `task plugin "gateway-ext-vendor" does not support a New API upstream`},
		{"duplicate", `{"task_extend_plugin_keys":["gateway-ext-a","gateway-ext-a"]}`, "bound more than once"},
		{"duplicate of single key", `{"task_plugin_key":"gateway-ext-a","task_extend_plugin_keys":["gateway-ext-a"]}`, "bound more than once"},
		{"padded", `{"task_extend_plugin_keys":[" gateway-ext-a"]}`, "is invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setting := tc.setting
			channel := &model.Channel{Type: constant.ChannelTypeNewAPI, BaseURL: &baseURL, Setting: &setting}
			err := validateChannel(channel, false)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
		})
	}

	setting := `{"task_extend_plugin_keys":["gateway-ext-a"]}`
	channel := &model.Channel{Type: constant.ChannelTypeOpenAI, Setting: &setting}
	require.ErrorContains(t, validateChannel(channel, false), "only supported on New API channels")
}
