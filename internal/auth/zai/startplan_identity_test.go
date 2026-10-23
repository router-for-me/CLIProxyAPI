package zai

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestZCodeSourceHeadersIdentity(t *testing.T) {
	headers := ZCodeSourceHeaders("7f1431f0-53e8-41c1-9cf9-22d11aa51d6a")
	if got := headers["User-Agent"]; got != "ZCode/"+StartPlanAppVersion {
		t.Fatalf("User-Agent = %q, want ZCode/%s", got, StartPlanAppVersion)
	}
	if got := headers["X-ZCode-App-Version"]; got != StartPlanAppVersion {
		t.Fatalf("X-ZCode-App-Version = %q, want %s", got, StartPlanAppVersion)
	}
	if got := headers["X-Device-Mid"]; got != "7f1431f0-53e8-41c1-9cf9-22d11aa51d6a" {
		t.Fatalf("X-Device-Mid = %q", got)
	}
	if osVersion := zcodeOsVersion(); osVersion != "" {
		if got := headers["X-Os-Version"]; got != osVersion {
			t.Fatalf("X-Os-Version = %q, want %q", got, osVersion)
		}
	} else if _, ok := headers["X-Os-Version"]; ok {
		t.Fatal("X-Os-Version present while the platform provides none")
	}
	if _, ok := ZCodeSourceHeaders("  ")["X-Device-Mid"]; ok {
		t.Fatal("blank device mid must omit X-Device-Mid")
	}
}

func TestZCodeMetadataUserID(t *testing.T) {
	got := ZCodeMetadataUserID("7f1431f0-53e8-41c1-9cf9-22d11aa51d6a", "2d480878-38af-4bae-a54f-096e2fb0f4d7")
	var identity struct {
		DeviceID    string `json:"device_id"`
		AccountUUID string `json:"account_uuid"`
		SessionID   string `json:"session_id"`
	}
	if errParse := json.Unmarshal([]byte(got), &identity); errParse != nil {
		t.Fatalf("ZCodeMetadataUserID is not JSON: %v (%q)", errParse, got)
	}
	if identity.DeviceID != "7f1431f0-53e8-41c1-9cf9-22d11aa51d6a" {
		t.Fatalf("device_id = %q", identity.DeviceID)
	}
	if identity.AccountUUID != "" {
		t.Fatalf("account_uuid = %q, want empty (the official client sends an empty account uuid)", identity.AccountUUID)
	}
	if identity.SessionID != "2d480878-38af-4bae-a54f-096e2fb0f4d7" {
		t.Fatalf("session_id = %q", identity.SessionID)
	}
	if !strings.Contains(got, `"account_uuid":""`) {
		t.Fatalf("missing account_uuid key: %q", got)
	}
}

func TestStartPlanDeviceMid(t *testing.T) {
	metadata := map[string]any{"device_mid": " 7f1431f0-53e8-41c1-9cf9-22d11aa51d6a "}
	if got := StartPlanDeviceMid(metadata, "seed"); got != "7f1431f0-53e8-41c1-9cf9-22d11aa51d6a" {
		t.Fatalf("stored device_mid wins: %q", got)
	}
	first := StartPlanDeviceMid(map[string]any{}, "zai-account@example.com")
	second := StartPlanDeviceMid(nil, "zai-account@example.com")
	if first == "" || first != second {
		t.Fatalf("derived device mid must be stable: %q vs %q", first, second)
	}
	if StartPlanDeviceMid(map[string]any{}, "") != "" {
		t.Fatal("no stored id and no seed must yield empty")
	}
}
