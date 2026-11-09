package config

import (
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestUnsupportedCompressionConfigKeysDetectsForeignKeys(t *testing.T) {
	doc := mustYAMLNode(t, `
compression:
  enabled: true
  min-bytes: 512
  defaultMode: auto
  engines:
    - rtk
`)
	got := unsupportedCompressionConfigKeys(&doc)
	want := []string{"defaultMode", "engines"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unsupported keys = %v, want %v", got, want)
	}
}

func TestUnsupportedCompressionConfigKeysAcceptsKnownKeys(t *testing.T) {
	doc := mustYAMLNode(t, `
compression:
  enabled: true
  shadow: false
  min-bytes: 10
  cache-prefix: true
`)
	if got := unsupportedCompressionConfigKeys(&doc); len(got) != 0 {
		t.Fatalf("unsupported keys = %v, want none", got)
	}
}

func TestUnsupportedCompressionConfigKeysDetectsLegacyLayout(t *testing.T) {
	doc := mustYAMLNode(t, `
requests:
  compression:
    enabled: true
    autoTriggerMode: auto
`)
	got := unsupportedCompressionConfigKeys(&doc)
	want := []string{"autoTriggerMode"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unsupported keys = %v, want %v", got, want)
	}
}

func TestCompressionConfigWithUnsupportedKeysLoadsWithoutError(t *testing.T) {
	var cfg Config
	data := []byte(`
compression:
  enabled: true
  defaultMode: auto
  engines: []
`)
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("config load returned error: %v", err)
	}
	if !cfg.Compression.Enabled {
		t.Fatal("supported key enabled was not applied")
	}
}

func mustYAMLNode(t *testing.T, data string) yaml.Node {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(data), &node); err != nil {
		t.Fatalf("unmarshal yaml: %v", err)
	}
	return node
}
