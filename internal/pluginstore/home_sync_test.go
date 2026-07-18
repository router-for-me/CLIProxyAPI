package pluginstore

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPluginSyncResponseValidatesAndClearsResolvedAuth(t *testing.T) {
	response := PluginSyncResponse{
		SchemaVersion: PluginSyncSchemaVersion,
		ExpiresAt:     time.Now().UTC().Add(time.Minute),
		Items: []PluginSyncItem{{
			Manifest: Manifest{
				SchemaVersion: SchemaVersionV2,
				ID:            "sample",
				Version:       "1.0.0",
				Install: InstallPlan{Type: InstallTypeDirect, Artifacts: []Artifact{{
					GOOS: "linux", GOARCH: "amd64", URL: "https://downloads.example/sample.zip",
					SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				}}},
			},
			Auth: []ResolvedAuthConfig{{
				Match: "https://downloads.example/", Type: AuthTypeBearer, Token: Secret("temporary-token"),
			}},
		}},
	}
	if errValidate := response.Validate(time.Now().UTC()); errValidate != nil {
		t.Fatalf("Validate() error = %v", errValidate)
	}
	backing := response.Items[0].Auth[0].Token
	response.Clear()
	for index, value := range backing {
		if value != 0 {
			t.Fatalf("token byte %d = %d, want zero", index, value)
		}
	}
	if response.Items != nil || !response.ExpiresAt.IsZero() || response.SchemaVersion != 0 {
		t.Fatalf("Clear() left response state: %#v", response)
	}
}

func TestPluginSyncResponseJSONKeepsSecretsOutOfPlainText(t *testing.T) {
	response := PluginSyncResponse{
		SchemaVersion: PluginSyncSchemaVersion,
		ExpiresAt:     time.Now().UTC().Add(time.Minute),
		Items:         []PluginSyncItem{{Auth: []ResolvedAuthConfig{{Token: Secret("temporary-token")}}}},
	}
	raw, errMarshal := json.Marshal(response)
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}
	if bytes.Contains(raw, []byte("temporary-token")) {
		t.Fatalf("Marshal() exposed token as plain text: %s", raw)
	}
	var decoded PluginSyncResponse
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		t.Fatalf("Unmarshal() error = %v", errUnmarshal)
	}
	if got := string(decoded.Items[0].Auth[0].Token); got != "temporary-token" {
		t.Fatalf("decoded token = %q, want temporary-token", got)
	}
	decoded.Clear()
}

func TestPluginSyncResponseRejectsExpiredPlan(t *testing.T) {
	response := PluginSyncResponse{SchemaVersion: PluginSyncSchemaVersion, ExpiresAt: time.Now().UTC().Add(-time.Second)}
	if errValidate := response.Validate(time.Now().UTC()); errValidate == nil {
		t.Fatal("Validate() error = nil, want expired response")
	}
}

func TestPluginSyncResponseRejectsInsecureResolvedAuthMatch(t *testing.T) {
	response := PluginSyncResponse{
		SchemaVersion: PluginSyncSchemaVersion,
		ExpiresAt:     time.Now().UTC().Add(time.Minute),
		Items: []PluginSyncItem{{
			Manifest: Manifest{
				SchemaVersion: SchemaVersionV2, ID: "sample", Version: "1.0.0",
				Install: InstallPlan{Type: InstallTypeDirect, Artifacts: []Artifact{{
					GOOS: "linux", GOARCH: "amd64", URL: "https://downloads.example/sample.zip",
					SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				}}},
			},
			Auth: []ResolvedAuthConfig{{Match: "http://downloads.example/", Type: AuthTypeBearer, Token: Secret("token")}},
		}},
	}
	defer response.Clear()
	if errValidate := response.Validate(time.Now().UTC()); errValidate == nil {
		t.Fatal("Validate() error = nil, want insecure auth match rejection")
	}
}

func TestPluginSyncResponseRejectsHTTPArtifact(t *testing.T) {
	response := PluginSyncResponse{
		SchemaVersion: PluginSyncSchemaVersion,
		ExpiresAt:     time.Now().UTC().Add(time.Minute),
		Items: []PluginSyncItem{{
			Manifest: Manifest{
				SchemaVersion: SchemaVersionV2, ID: "sample", Version: "1.0.0",
				Install: InstallPlan{Type: InstallTypeDirect, Artifacts: []Artifact{{
					GOOS: "linux", GOARCH: "amd64", URL: "http://downloads.example/sample.zip",
					SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				}}},
			},
		}},
	}
	defer response.Clear()
	if errValidate := response.Validate(time.Now().UTC()); errValidate == nil {
		t.Fatal("Validate() error = nil, want HTTP artifact rejection")
	}
}

func TestPluginSyncResponseRejectsHTTPArtifactWithResolvedAuth(t *testing.T) {
	response := PluginSyncResponse{
		SchemaVersion: PluginSyncSchemaVersion,
		ExpiresAt:     time.Now().UTC().Add(time.Minute),
		Items: []PluginSyncItem{{
			Manifest: Manifest{
				SchemaVersion: SchemaVersionV2, ID: "sample", Version: "1.0.0",
				Install: InstallPlan{Type: InstallTypeDirect, Artifacts: []Artifact{{
					GOOS: "linux", GOARCH: "amd64", URL: "http://downloads.example/sample.zip",
					SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				}}},
			},
			Auth: []ResolvedAuthConfig{{
				Match: "https://downloads.example/", ApplyTo: []string{RequestKindArtifact}, Type: AuthTypeBearer, Token: Secret("token"),
			}},
		}},
	}
	defer response.Clear()
	if errValidate := response.Validate(time.Now().UTC()); errValidate == nil {
		t.Fatal("Validate() error = nil, want HTTP artifact rejection")
	}
}

func TestPluginSyncResponseRejectsArtifactURLCredentials(t *testing.T) {
	response := PluginSyncResponse{
		SchemaVersion: PluginSyncSchemaVersion,
		ExpiresAt:     time.Now().UTC().Add(time.Minute),
		Items: []PluginSyncItem{{
			Manifest: Manifest{
				SchemaVersion: SchemaVersionV2, ID: "sample", Version: "1.0.0",
				Install: InstallPlan{Type: InstallTypeDirect, Artifacts: []Artifact{{
					GOOS: "linux", GOARCH: "amd64", URL: "https://user:password@downloads.example/sample.zip",
					SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				}}},
			},
		}},
	}
	defer response.Clear()
	errValidate := response.Validate(time.Now().UTC())
	if errValidate == nil {
		t.Fatal("Validate() error = nil, want artifact URL credentials rejection")
	}
	if strings.Contains(errValidate.Error(), "password") {
		t.Fatalf("Validate() error leaked URL credentials: %v", errValidate)
	}
}
