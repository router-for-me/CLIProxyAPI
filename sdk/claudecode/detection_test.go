package claudecode

import (
	"net/http"
	"testing"

	"github.com/MoeForge/moego/json"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

const testMetadataUserID = `{"device_id":"0000000000000000000000000000000000000000000000000000000000000000","account_uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","session_id":"11111111-2222-4333-8444-555555555555"}`

func testPayload(userID string) []byte {
	if userID == "" {
		return []byte(`{"model":"claude-sonnet-4-5","messages":[]}`)
	}
	encoded, _ := json.Marshal(userID)
	return []byte(`{"model":"claude-sonnet-4-5","messages":[],"metadata":{"user_id":` + string(encoded) + `}}`)
}

func nativeHeaders() http.Header {
	return http.Header{
		"User-Agent":     {"claude-cli/2.1.321 (external, cli)"},
		"X-App":          {"cli"},
		"Anthropic-Beta": {"claude-code-20250219"},
	}
}

func TestDetectRequestConfirmsNativeClaudeCode(t *testing.T) {
	for _, cfg := range []*sdkconfig.Config{nil, {}} {
		detection := DetectRequest(nativeHeaders(), testPayload(testMetadataUserID), false, cfg)
		if !detection.Confirmed || !detection.NativeClient {
			t.Fatalf("cfg=%v: detection = %#v, want confirmed native Claude Code", cfg, detection)
		}
	}
}

func TestDetectRequestRejectsNonClaudeCodeClients(t *testing.T) {
	copiedUA := http.Header{"User-Agent": {"claude-cli/2.1.321 (external, cli)"}}
	missingMetadata := nativeHeaders()
	sdkEntrypoint := nativeHeaders()
	sdkEntrypoint.Set("User-Agent", "claude-cli/2.1.321 (external, sdk-py)")

	cases := map[string]struct {
		headers http.Header
		payload []byte
	}{
		"generic client":            {http.Header{"User-Agent": {"curl/8.0"}}, testPayload(testMetadataUserID)},
		"user agent only":           {copiedUA, testPayload(testMetadataUserID)},
		"missing metadata user id":  {missingMetadata, testPayload("")},
		"unverified sdk entrypoint": {sdkEntrypoint, testPayload(testMetadataUserID)},
	}
	for name, tc := range cases {
		if detection := DetectRequest(tc.headers, tc.payload, false, nil); detection.Confirmed {
			t.Fatalf("%s: detection = %#v, want unconfirmed", name, detection)
		}
	}
}

func TestDetectRequestCountTokensDoesNotRequireMetadata(t *testing.T) {
	if detection := DetectRequest(nativeHeaders(), testPayload(""), true, nil); !detection.Confirmed {
		t.Fatalf("detection = %#v, want confirmed count_tokens request", detection)
	}
}
