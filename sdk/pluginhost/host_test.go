package pluginhost

import (
	"context"
	"testing"
)

func TestHostStartLoginNilSafe(t *testing.T) {
	var nilHost *Host
	resp, handled, err := nilHost.StartLogin(context.Background(), "test", "http://example.com", map[string]any{"key": "val"})
	if handled || err != nil || resp.State != "" {
		t.Fatalf("StartLogin on nil host = (%#v, %v, %v), want zero values", resp, handled, err)
	}

	emptyHost := &Host{}
	resp, handled, err = emptyHost.StartLogin(context.Background(), "test", "http://example.com", map[string]any{"key": "val"})
	if handled || err != nil || resp.State != "" {
		t.Fatalf("StartLogin on empty host = (%#v, %v, %v), want zero values", resp, handled, err)
	}
}
