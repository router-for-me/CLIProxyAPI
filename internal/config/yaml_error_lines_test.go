package config

import (
	"errors"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestValidateV8ConfigNamesUnknownFields(t *testing.T) {
	cases := []struct{ name, input, want string }{
		{
			"typo next to a known sibling",
			"config-version: 8\n# comment\n\nserver:\n  port: 8317\n  hots: 0.0.0.0\n",
			"line 6: field hots not found",
		},
		{
			"typo in a nested section",
			"config-version: 8\nserver:\n  port: 8317\nobservability:\n  logs:\n    request-log: true\n    reqest-log: true\n",
			"line 7: field reqest-log not found",
		},
		{
			"typo under a section that is a struct in both layouts",
			"config-version: 8\nrouting:\n  retry:\n    request-retry: 3\n    reqest-retry: 1\n",
			"line 5: field reqest-retry not found",
		},
		{
			"typo in an expanded API-key entry",
			"config-version: 8\napi-keys:\n  codex:\n    - name: x\n      keys:\n        - api-key: hi\n          oops: 1\n",
			"line 7: field oops not found",
		},
		{
			"typo in a section decoded as one struct",
			"config-version: 8\nrouting:\n  strategy: round-robin\n\n  stratgy: fill-first\n",
			"line 5: field stratgy not found",
		},
	}
	for _, tc := range cases {
		err := ValidateV8Config([]byte(tc.input))
		if err == nil {
			t.Fatalf("%s: expected an error", tc.name)
		}
		var typeErr *yaml.TypeError
		if !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "legacyConfig") || !errors.As(err, &typeErr) {
			t.Errorf("%s: got %q, want it to contain %q", tc.name, err.Error(), tc.want)
		}
	}
}

func TestValidateV8ConfigReportsEveryUnknownFieldInASection(t *testing.T) {
	err := ValidateV8Config([]byte("config-version: 8\nserver:\n  hots: a\n  prot: 1\n"))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"line 3: field hots not found", "line 4: field prot not found"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("got %q, want it to contain %q", err.Error(), want)
		}
	}
}
