package copilot

import (
    "encoding/json"
    "os"
    "path/filepath"
    "testing"
)

func TestTokenStorage_SaveTokenToFile_WritesJSON(t *testing.T) {
    t.Parallel()
    tmp := t.TempDir()
    dst := filepath.Join(tmp, "copilot-test.json")

    ts := &TokenStorage{
        IDToken:      "id",
        AccessToken:  "atk",
        RefreshToken: "rfk",
        LastRefresh:  "2025-01-01T00:00:00Z",
        Email:        "user@example.com",
        Expire:       "2025-01-02T00:00:00Z",
    }
    if err := ts.SaveTokenToFile(dst); err != nil {
        t.Fatalf("save error: %v", err)
    }
    raw, err := os.ReadFile(dst)
    if err != nil {
        t.Fatalf("read back error: %v", err)
    }
    var m map[string]any
    if err := json.Unmarshal(raw, &m); err != nil {
        t.Fatalf("json parse error: %v", err)
    }
    if m["type"] != "copilot" {
        t.Fatalf("expected type=copilot, got %v", m["type"])
    }
    if m["email"] != "user@example.com" {
        t.Fatalf("unexpected email: %v", m["email"])
    }
}

