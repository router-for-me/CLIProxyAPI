package cmd

import (
	"strings"
	"testing"
)

func TestStartServiceReturnsBuildError(t *testing.T) {
	if err := StartService(nil, "", ""); err == nil || !strings.Contains(err.Error(), "configuration is required") {
		t.Fatalf("expected the missing configuration error, got %v", err)
	}
}

func TestStartServiceWithPluginHostReturnsBuildError(t *testing.T) {
	if err := StartServiceWithPluginHost(nil, "", "", nil); err == nil || !strings.Contains(err.Error(), "configuration is required") {
		t.Fatalf("expected the missing configuration error, got %v", err)
	}
}
