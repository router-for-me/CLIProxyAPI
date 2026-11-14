package management

import (
	"os"
	"path/filepath"
	"testing"

	qoderauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestBuildAuthFileEntryIncludesQoderUsageSnapshot(t *testing.T) {
	authDir := t.TempDir()
	filePath := filepath.Join(authDir, "qoder-user@example.com.json")
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"qoder","email":"user@example.com"}`), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	storage := &qoderauth.QoderTokenStorage{}
	storage.SetUsageInfo(&qoderauth.QoderUsageInfo{
		UserQuota: qoderauth.QoderQuota{
			Total:      3000,
			Used:       250,
			Remaining:  2750,
			Percentage: 0.0833,
			Unit:       "credits",
		},
		OrgResourcePackage:   qoderauth.QoderQuota{Remaining: 1200},
		TotalUsagePercentage: 0.0833,
		IsQuotaExceeded:      false,
		ExpiresAt:            1790755390618,
	})

	auth := &coreauth.Auth{
		ID:       "qoder-user@example.com.json",
		FileName: "qoder-user@example.com.json",
		Provider: "qoder",
		Status:   coreauth.StatusActive,
		Storage:  storage,
		Attributes: map[string]string{
			"path": filePath,
		},
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)

	entry := h.buildAuthFileEntry(auth, authFileQuotaContext{})
	usage, ok := entry["usage"].(map[string]any)
	if !ok {
		t.Fatalf("expected Qoder usage snapshot, got %#v", entry["usage"])
	}
	if got := usage["remaining"]; got != float64(2750) {
		t.Fatalf("remaining = %#v, want 2750", got)
	}
	if got := usage["org_resource_remaining"]; got != float64(1200) {
		t.Fatalf("org_resource_remaining = %#v, want 1200", got)
	}
	if got := usage["expires_at"]; got != int64(1790755390618) {
		t.Fatalf("expires_at = %#v, want 1790755390618", got)
	}
}

func TestBuildAuthFileEntryOmitsQoderUsageBeforeSync(t *testing.T) {
	authDir := t.TempDir()
	filePath := filepath.Join(authDir, "qoder-user@example.com.json")
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"qoder"}`), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	auth := &coreauth.Auth{
		ID:       "qoder-user@example.com.json",
		FileName: "qoder-user@example.com.json",
		Provider: "qoder",
		Status:   coreauth.StatusActive,
		Storage:  &qoderauth.QoderTokenStorage{},
		Attributes: map[string]string{
			"path": filePath,
		},
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)

	entry := h.buildAuthFileEntry(auth, authFileQuotaContext{})
	if _, ok := entry["usage"]; ok {
		t.Fatalf("usage should be absent before Qoder sync")
	}
}
