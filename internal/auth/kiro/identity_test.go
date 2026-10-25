package kiro

import (
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestEnsureMachineIDGeneratesAndPersists(t *testing.T) {
	metadata := map[string]any{"type": "kiro"}
	first := EnsureMachineID(metadata)
	if _, err := uuid.Parse(first); err != nil {
		t.Fatalf("machine id is not a UUID: %q", first)
	}
	if again := EnsureMachineID(metadata); again != first {
		t.Fatalf("machine id regenerated: %q != %q", again, first)
	}
	if kept := EnsureMachineID(map[string]any{"machine_id": "stable-id"}); kept != "stable-id" {
		t.Fatalf("existing machine id replaced: %q", kept)
	}
	firstAccount := map[string]any{"provider": "Github", "email": "user@example.com", "refresh_token": "fixture-refresh"}
	id := EnsureMachineID(firstAccount)
	firstAccount["refresh_token"] = "rotated-fixture"
	if EnsureMachineID(firstAccount) != id {
		t.Fatal("persisted identity changed during token rotation")
	}
	if got := EnsureMachineID(map[string]any{"provider": "Github", "email": "USER@example.com"}); got != id {
		t.Fatal("email normalization changed the derived identity")
	}
	if other := EnsureMachineID(map[string]any{"provider": "Github", "email": "another@example.com"}); other == id {
		t.Fatal("different accounts share a generated identity")
	}
}

func TestKiroImportPreservesMachineIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields map[string]any
		want   string
	}{
		{"native", map[string]any{"machineId": "native-device"}, "native-device"},
		{"canonical", map[string]any{"machine_id": "canonical-device"}, "canonical-device"},
		{"canonical wins", map[string]any{"machineId": "native-device", "machine_id": "canonical-device"}, "canonical-device"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metadata := map[string]any{"provider": "Github", "refreshToken": "fixture-refresh"}
			for key, value := range tc.fields {
				metadata[key] = value
			}
			NormalizeMetadata(metadata)
			if got := String(metadata, "machine_id"); got != tc.want {
				t.Fatalf("machine identity = %q, want %q", got, tc.want)
			}
			if _, exists := metadata["machineId"]; exists {
				t.Fatal("native machineId was not normalized")
			}
		})
	}
}

func TestKiroRequestIdentityIsStableWithoutPersistedMachineID(t *testing.T) {
	for _, metadata := range []map[string]any{
		{"email": "user@example.com", "provider": "Github", "refresh_token": "fixture-refresh"},
		{"refresh_token": "fixture-refresh"},
		{"access_token": "fixture-access"},
	} {
		ua := RuntimeUserAgent(metadata)
		if got := strings.TrimPrefix(RuntimeXAmzUserAgent(metadata), "aws-sdk-js/1.0.39 "); !strings.HasSuffix(ua, got) {
			t.Fatalf("runtime identities differ: %q / %q", ua, got)
		}
		if ua != RuntimeUserAgent(metadata) {
			t.Fatal("runtime identity changes between requests")
		}
		if got := strings.TrimPrefix(ManagementXAmzUserAgent(metadata), "aws-sdk-js/1.0.0 "); !strings.HasSuffix(ManagementUserAgent(metadata), got) {
			t.Fatal("management identities differ")
		}
		if _, exists := metadata["machine_id"]; exists {
			t.Fatal("read-only header construction mutated credential metadata")
		}
	}
}

func TestUserAgentsMimicKiroIDE(t *testing.T) {
	metadata := map[string]any{"type": "kiro", "machine_id": "11111111-2222-3333-4444-555555555555"}
	osSegment := regexp.MustCompile(`os/(win32|darwin|linux|windows|[a-z]+)#\S+`)
	cases := []struct {
		name     string
		value    string
		contains []string
	}{
		{"runtime", RuntimeUserAgent(metadata), []string{
			"aws-sdk-js/1.0.39 ua/2.1 ", " lang/js md/nodejs#22.22.0 ", " api/codewhispererstreaming#1.0.39 m/N ",
			"KiroIDE-1.4.26-11111111-2222-3333-4444-555555555555"}},
		{"runtime-x-amz", RuntimeXAmzUserAgent(metadata), []string{"aws-sdk-js/1.0.39 KiroIDE-1.4.26-11111111-2222-3333-4444-555555555555"}},
		{"management", ManagementUserAgent(metadata), []string{
			"aws-sdk-js/1.0.0 ua/2.1 ", " api/codewhispererruntime#1.0.0 m/N,E ",
			"KiroIDE-1.4.26-11111111-2222-3333-4444-555555555555"}},
		{"management-x-amz", ManagementXAmzUserAgent(metadata), []string{"aws-sdk-js/1.0.0 KiroIDE-1.4.26-11111111-2222-3333-4444-555555555555"}},
		{"control-plane", ControlPlaneUserAgent(metadata), []string{
			"aws-sdk-js/1.0.0 ua/2.1 ", " api/kirocontrolplanebearer#1.0.0 m/N,E"}},
		{"desktop-auth", DesktopAuthUserAgent(metadata), []string{"KiroIDE-0.6.18-11111111-2222-3333-4444-555555555555"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			needsOSSegment := !strings.HasSuffix(testCase.name, "-x-amz") && testCase.name != "desktop-auth"
			if needsOSSegment && !osSegment.MatchString(testCase.value) {
				t.Fatalf("user agent lacks os segment: %q", testCase.value)
			}
			for _, fragment := range testCase.contains {
				if !strings.Contains(testCase.value, fragment) {
					t.Fatalf("user agent %q missing %q", testCase.value, fragment)
				}
			}
		})
	}
	if control := ControlPlaneUserAgent(metadata); strings.Contains(control, "KiroIDE-") {
		t.Fatalf("control-plane user agent must not carry the IDE identity: %q", control)
	}
	if override := IDEVersion(map[string]any{"ide_version": "9.9.9"}); override != "9.9.9" {
		t.Fatalf("ide_version override ignored: %q", override)
	}
	if !strings.Contains(RuntimeUserAgent(map[string]any{}), "KiroIDE-1.4.26-") {
		t.Fatal("default IDE version missing from runtime user agent")
	}
}
