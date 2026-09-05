package helps

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestObfuscateSensitiveWords_ObfuscatesToolDescriptionsButNotNames(t *testing.T) {
	matcher := BuildSensitiveWordMatcher([]string{"Hermes"})
	if matcher == nil {
		t.Fatal("expected matcher")
	}

	payload := []byte(`{
		"system":[{"type":"text","text":"You are Hermes Agent"}],
		"messages":[{"role":"user","content":"ask Hermes"}],
		"tools":[{
			"name":"terminal",
			"description":"Run a shell. Hermes tracks background jobs.",
			"input_schema":{
				"type":"object",
				"properties":{
					"command":{"type":"string","description":"Command for Hermes to run"}
				}
			}
		}]
	}`)

	out := ObfuscateSensitiveWords(payload, matcher)

	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "terminal" {
		t.Fatalf("tool name changed: %q", got)
	}

	desc := gjson.GetBytes(out, "tools.0.description").String()
	if !strings.Contains(desc, "H\u200Bermes") {
		t.Fatalf("tool description not obfuscated: %q", desc)
	}
	if strings.Contains(desc, "Hermes") && !strings.Contains(desc, "H\u200Bermes") {
		t.Fatalf("plain Hermes remains in description: %q", desc)
	}

	propDesc := gjson.GetBytes(out, "tools.0.input_schema.properties.command.description").String()
	if !strings.Contains(propDesc, "H\u200Bermes") {
		t.Fatalf("nested schema description not obfuscated: %q", propDesc)
	}

	sys := gjson.GetBytes(out, "system.0.text").String()
	if !strings.Contains(sys, "H\u200Bermes") {
		t.Fatalf("system text not obfuscated: %q", sys)
	}
}
