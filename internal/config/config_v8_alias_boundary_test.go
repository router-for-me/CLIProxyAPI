package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestNormalizeV8AliasesRejectsInvalidContainersBeforeRewrite(t *testing.T) {
	for _, raw := range []string{
		"client: false\noauth: {providers: {codex: {optimize-multi-agent-v2: true}}}\n",
		"upstream: {codex: false}\noauth: {providers: {codex: {response-steering: true}}}\n",
		"oauth: {providers: false}\nclient: {codex: {optimize-multi-agent-v2: true}}\n",
	} {
		var doc yaml.Node
		if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
			t.Fatal(err)
		}
		if err := NormalizeV8ConfigAliases(doc.Content[0]); err == nil {
			t.Fatalf("alias normalization silently repaired an invalid container: %s", raw)
		}
	}
}
