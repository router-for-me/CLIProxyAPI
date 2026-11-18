package synthesizer

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestSynthesizeAuthFilePluginOAuthProviderHint(t *testing.T) {
	for _, tc := range []struct {
		name       string
		raw        string
		attributes map[string]string
		metadata   map[string]any
		want       string
		inherit    bool
	}{
		{name: "primary", raw: `{"type":"vendor","oauth_provider":" Vendor "}`, want: "vendor", inherit: true},
		{name: "oauth-before-portal", raw: `{"type":"vendor","oauth_provider":" Vendor ","portal":"vendor-cn"}`, want: "vendor", inherit: true},
		{name: "blank-oauth-fallback", raw: `{"type":"vendor","oauth_provider":"   ","portal":" Vendor-CN "}`, want: "vendor-cn", inherit: true},
		{name: "non-string-oauth-fallback", raw: `{"type":"vendor","oauth_provider":42,"portal":" Vendor "}`, want: "vendor", inherit: true},
		{name: "invalid-parsed-hints", raw: `{"type":"vendor","oauth_provider":"vendor"}`, attributes: map[string]string{"oauth_provider": "   ", "portal": "   "}, metadata: map[string]any{"oauth_provider": true, "portal": 42}, want: "vendor", inherit: true},
		{name: "attributes-oauth-preserved", raw: `{"type":"vendor","oauth_provider":"vendor"}`, attributes: map[string]string{"oauth_provider": "vendor-cn"}, want: "vendor-cn"},
		{name: "metadata-oauth-preserved", raw: `{"type":"vendor","oauth_provider":"vendor"}`, metadata: map[string]any{"oauth_provider": "vendor-cn"}, want: "vendor-cn"},
		{name: "attributes-portal-preserved", raw: `{"type":"vendor","oauth_provider":"vendor"}`, attributes: map[string]string{"portal": "vendor-cn"}, want: "vendor-cn"},
		{name: "metadata-portal-preserved", raw: `{"type":"vendor","oauth_provider":"vendor"}`, metadata: map[string]any{"portal": "vendor-cn"}, want: "vendor-cn"},
		{name: "no-hint", raw: `{"type":"vendor"}`},
		{name: "invalid-file-hints", raw: `{"type":"vendor","oauth_provider":true,"portal":42}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := &oauthProviderMetadataStorage{}
			parser := multiAuthParserFunc(func(context.Context, pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
				return []*coreauth.Auth{
					{Provider: "vendor", Metadata: tc.metadata, Attributes: tc.attributes, Storage: storage},
					{Provider: "vendor"},
				}, true, nil
			})
			authDir := t.TempDir()
			auths, errSynthesize := SynthesizeAuthFile(&SynthesisContext{
				AuthDir: authDir, PluginAuthParser: parser, Now: time.Now(), StrictFileParsing: true,
			}, filepath.Join(authDir, "vendor.json"), []byte(tc.raw))
			if errSynthesize != nil || len(auths) != 2 {
				t.Fatalf("SynthesizeAuthFile() count=%d error=%v", len(auths), errSynthesize)
			}
			auth := auths[0]
			hint := ""
			for _, key := range []string{"oauth_provider", "portal"} {
				if hint = auth.Attributes[key]; hint != "" {
					break
				}
				if hint, _ = auth.Metadata[key].(string); hint != "" {
					break
				}
			}
			if hint != tc.want {
				t.Fatalf("synthesized hint = %q, want %q", hint, tc.want)
			}
			if tc.inherit {
				for index, parsed := range auths {
					if parsed.Metadata["oauth_provider"] != tc.want || parsed.Attributes["oauth_provider"] != tc.want {
						t.Errorf("auth %d did not inherit hint: metadata=%#v attributes=%#v", index, parsed.Metadata, parsed.Attributes)
					}
				}
				if storage.metadata["oauth_provider"] != tc.want {
					t.Fatalf("storage metadata=%#v, want oauth_provider=%q", storage.metadata, tc.want)
				}
			} else if storage.metadata != nil {
				t.Fatalf("existing hint unexpectedly changed storage metadata: %#v", storage.metadata)
			}
		})
	}
}

type oauthProviderMetadataStorage struct {
	metadata map[string]any
}

func (s *oauthProviderMetadataStorage) SetMetadata(metadata map[string]any) {
	s.metadata = metadata
}

func (s *oauthProviderMetadataStorage) SaveTokenToFile(string) error {
	return nil
}
