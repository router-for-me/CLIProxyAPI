package helps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	homekv "github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

type fakeClaudeCredentialDevicePoolKV struct {
	values  map[string][]byte
	setOpts []homekv.KVSetOptions
}

func (fake *fakeClaudeCredentialDevicePoolKV) KVGet(_ context.Context, key string) ([]byte, bool, error) {
	value, found := fake.values[key]
	return bytes.Clone(value), found, nil
}

func (fake *fakeClaudeCredentialDevicePoolKV) KVSet(_ context.Context, key string, value []byte, opts homekv.KVSetOptions) (bool, error) {
	_, found := fake.values[key]
	if (opts.NX && found) || (opts.XX && !found) {
		return false, nil
	}
	fake.values[key] = bytes.Clone(value)
	fake.setOpts = append(fake.setOpts, opts)
	return true, nil
}

func TestClaudeAgentSessionUUIDPreservesNativeSession(t *testing.T) {
	const sessionID = "11111111-2222-4333-8444-555555555555"
	got := ClaudeAgentSessionUUIDForRequest(http.Header{"X-Claude-Code-Session-Id": {sessionID}}, nil, nil, true)
	if got != sessionID {
		t.Fatalf("ClaudeAgentSessionUUIDForRequest() = %q, want native session %q", got, sessionID)
	}
}

func TestClaudeAgentSessionUUIDIgnoresUnconfirmedClaudeSignals(t *testing.T) {
	const nativeSessionID = "11111111-2222-4333-8444-555555555555"
	metadata := map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "non-native-conversation"}
	got := ClaudeAgentSessionUUIDForRequest(
		http.Header{"X-Claude-Code-Session-Id": {nativeSessionID}},
		[]byte(`{"metadata":{"user_id":"{\"device_id\":\"0000000000000000000000000000000000000000000000000000000000000000\",\"session_id\":\"11111111-2222-4333-8444-555555555555\"}"}}`),
		nil,
		false,
		metadata,
	)
	if got == nativeSessionID {
		t.Fatalf("ClaudeAgentSessionUUIDForRequest() = native session %q for unconfirmed caller", got)
	}
	if repeated := ClaudeAgentSessionUUIDForRequest(nil, nil, nil, false, metadata); repeated != got {
		t.Fatalf("derived session changed: first=%q repeated=%q", got, repeated)
	}
}

func TestClaudeAgentSessionUUIDUsesExecutionAndDerivedIdentity(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]any
	}{
		{
			name:     "execution session",
			metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "agent-run-1"},
		},
		{
			name:     "derived session",
			metadata: map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: "ctx:v1:conversation-root"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first := ClaudeAgentSessionUUID(nil, nil, nil, test.metadata)
			second := ClaudeAgentSessionUUID(nil, nil, nil, test.metadata)
			if first == "" || first != second {
				t.Fatalf("session UUIDs = %q and %q, want equal non-empty values", first, second)
			}
		})
	}
}

func TestEnsureClaudeCredentialDevicePoolRequiredMigratesHomeKVToOne(t *testing.T) {
	auth := &cliproxyauth.Auth{ID: "legacy-five-device-credential", Metadata: map[string]any{}}
	key := "cpa:claude:credential-device-pool:" + homekv.HashKeyPart(auth.EnsureIndex())
	legacy := []string{
		"0000000000000000000000000000000000000000000000000000000000000000",
		"1111111111111111111111111111111111111111111111111111111111111111",
		"2222222222222222222222222222222222222222222222222222222222222222",
		"3333333333333333333333333333333333333333333333333333333333333333",
		"4444444444444444444444444444444444444444444444444444444444444444",
	}
	rawLegacy, errMarshal := json.Marshal(legacy)
	if errMarshal != nil {
		t.Fatalf("marshal legacy device pool: %v", errMarshal)
	}
	fake := &fakeClaudeCredentialDevicePoolKV{values: map[string][]byte{key: rawLegacy}}
	previousClient := currentClaudeCredentialDevicePoolKVClient
	currentClaudeCredentialDevicePoolKVClient = func() (claudeCredentialDevicePoolKVClient, bool, error) {
		return fake, true, nil
	}
	t.Cleanup(func() { currentClaudeCredentialDevicePoolKVClient = previousClient })

	deviceIDs, errEnsure := EnsureClaudeCredentialDevicePoolRequired(context.Background(), auth)
	if errEnsure != nil {
		t.Fatalf("EnsureClaudeCredentialDevicePoolRequired() error = %v", errEnsure)
	}
	want := []string{legacy[0]}
	if len(deviceIDs) != 1 || deviceIDs[0] != want[0] {
		t.Fatalf("device IDs = %#v, want %#v", deviceIDs, want)
	}
	if len(fake.setOpts) != 1 || !fake.setOpts[0].XX || fake.setOpts[0].NX || fake.setOpts[0].EX != 0 || fake.setOpts[0].PX != 0 {
		t.Fatalf("Home KV set options = %#v, want one persistent XX rewrite", fake.setOpts)
	}
	var stored []string
	if errUnmarshal := json.Unmarshal(fake.values[key], &stored); errUnmarshal != nil {
		t.Fatalf("decode canonical Home KV pool: %v", errUnmarshal)
	}
	if len(stored) != 1 || stored[0] != want[0] {
		t.Fatalf("Home KV device IDs = %#v, want %#v", stored, want)
	}
	if !claudeauth.HasCanonicalDeviceIDPool(auth.Metadata[claudeauth.ClaudeDeviceIDsMetadataKey]) {
		t.Fatalf("auth metadata device pool = %#v, want canonical single device", auth.Metadata[claudeauth.ClaudeDeviceIDsMetadataKey])
	}
}

func TestApplyClaudeCredentialMetadataUsesCredentialDeviceAndPreservesExtras(t *testing.T) {
	deviceIDs := []string{
		"0000000000000000000000000000000000000000000000000000000000000000",
	}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{
		"account_uuid":                        "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		claudeauth.ClaudeDeviceIDsMetadataKey: deviceIDs,
	}}
	const sessionID = "11111111-2222-4333-8444-555555555555"
	body := []byte(`{"messages":[{"role":"user","content":"x"}],"metadata":{"user_id":"{\"device_id\":\"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff\",\"account_uuid\":\"downstream-account\",\"session_id\":\"downstream-session\",\"parent_session_id\":\"parent-1\",\"extra\":true}"}}`)

	updated, selectedDevice, errApply := ApplyClaudeCredentialMetadata(body, auth, sessionID)
	if errApply != nil {
		t.Fatalf("ApplyClaudeCredentialMetadata() error = %v", errApply)
	}
	userID := gjson.GetBytes(updated, "metadata.user_id").String()
	if got := gjson.Get(userID, "device_id").String(); got != selectedDevice {
		t.Fatalf("device_id = %q, want selected %q", got, selectedDevice)
	}
	if got := gjson.Get(userID, "account_uuid").String(); got != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("account_uuid = %q, want credential account", got)
	}
	if got := gjson.Get(userID, "session_id").String(); got != sessionID {
		t.Fatalf("session_id = %q, want %q", got, sessionID)
	}
	if got := gjson.Get(userID, "parent_session_id").String(); got != "parent-1" {
		t.Fatalf("parent_session_id = %q, want preserved", got)
	}
	if !gjson.Get(userID, "extra").Bool() {
		t.Fatal("extra metadata was not preserved")
	}
	wantPrefix := `{"device_id":"` + selectedDevice + `","account_uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","session_id":"` + sessionID + `"`
	if !strings.HasPrefix(userID, wantPrefix) {
		t.Fatalf("metadata.user_id = %q, want credential identity fields first", userID)
	}
}

func TestApplyClaudeCredentialMetadataRejectsDuplicateIdentityContainers(t *testing.T) {
	auth := &cliproxyauth.Auth{Metadata: map[string]any{
		"account_uuid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		claudeauth.ClaudeDeviceIDsMetadataKey: []string{
			"0000000000000000000000000000000000000000000000000000000000000000",
		},
	}}
	const sessionID = "11111111-2222-4333-8444-555555555555"
	tests := []struct {
		name string
		body string
	}{
		{
			name: "invalid request JSON",
			body: `{"messages":[],"metadata":`,
		},
		{
			name: "duplicate top-level metadata",
			body: `{"messages":[],"metadata":{"user_id":"{}"},"metadata":{"user_id":"{}"}}`,
		},
		{
			name: "duplicate metadata user ID",
			body: `{"messages":[],"metadata":{"user_id":"{}","user_id":"{}"}}`,
		},
		{
			name: "duplicate encoded account UUID",
			body: `{"messages":[],"metadata":{"user_id":"{\"account_uuid\":\"first\",\"account_uuid\":\"last\"}"}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, errApply := ApplyClaudeCredentialMetadata([]byte(test.body), auth, sessionID)
			if errApply == nil {
				t.Fatal("ApplyClaudeCredentialMetadata() error = nil, want duplicate-key rejection")
			}
			var requestErr cliproxyexecutor.RequestScopedError
			if !errors.As(errApply, &requestErr) || requestErr == nil || !requestErr.IsRequestScoped() {
				t.Fatalf("ApplyClaudeCredentialMetadata() error = %T %v, want request-scoped", errApply, errApply)
			}
			var statusErr interface{ StatusCode() int }
			if !errors.As(errApply, &statusErr) || statusErr.StatusCode() != http.StatusBadRequest {
				t.Fatalf("ApplyClaudeCredentialMetadata() error = %T %v, want HTTP 400", errApply, errApply)
			}
		})
	}
}

func TestApplyClaudeCredentialMetadataRequiresAccountUUID(t *testing.T) {
	auth := &cliproxyauth.Auth{Metadata: map[string]any{
		claudeauth.ClaudeDeviceIDsMetadataKey: []string{
			"0000000000000000000000000000000000000000000000000000000000000000",
		},
	}}
	_, _, errApply := ApplyClaudeCredentialMetadata(
		[]byte(`{"messages":[]}`),
		auth,
		"11111111-2222-4333-8444-555555555555",
	)
	if errApply == nil {
		t.Fatal("ApplyClaudeCredentialMetadata() error = nil, want missing account UUID rejection")
	}
	var requestErr cliproxyexecutor.RequestScopedError
	if errors.As(errApply, &requestErr) && requestErr != nil && requestErr.IsRequestScoped() {
		t.Fatalf("missing credential identity error = %T %v, want credential-scoped", errApply, errApply)
	}
}
