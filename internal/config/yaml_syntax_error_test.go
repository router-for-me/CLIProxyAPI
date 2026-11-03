package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// yaml.v3 itself reports the wrong line for most of these inputs.
func TestDescribeYAMLSyntaxErrorReportsTheProblemLine(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"dedent after a header comment", "# header\nconfig-version: 8\nserver:\n  host: a\n  port: 1\nrouting:\n  strategy: round-robin\n bad: 1\n",
			"yaml: line 8: did not find expected key"},
		{"sequence entry inside a mapping", "config-version: 8\nserver:\n  host: a\n  port: 1\n  - x\n",
			"yaml: line 5: did not find expected key"},
		{"unclosed flow sequence", "config-version: 8\n# comment\n\nserver:\n  port: [8317\nother: 2\n",
			"yaml: line 5: did not find expected ',' or ']'"},
		{"unclosed flow on a one-line body", "a: [1, 2",
			"yaml: line 1: did not find expected ',' or ']'"},
		{"parser problem on the first line", "[}\n",
			"yaml: line 1: did not find expected node content"},
		{"tab indentation", "config-version: 8\nserver:\n\tport: 1\n",
			"yaml: line 3: found character that cannot start any token"},
		{"unclosed quote", "config-version: 8\nserver: \"oops\nother: 2\n",
			"yaml: line 2: found unexpected end of stream"},
		{"valid multi-line flow before the problem", "a: [1,\n  2]\nb:\n  c: 1\n d: 2\n",
			"yaml: line 5: did not find expected key"},
		{"valid multi-line quote before an unclosed quote", "config-version: 8\nserver:\n  host: \"hello\n    world\n    and\n    more\n    lines\n    ok\"\n  port: \"oops\n",
			"yaml: line 9: found unexpected end of stream"},
		{"valid multi-line flow before an unclosed flow", "config-version: 8\nrequests:\n  passthrough-headers: [\n    a,\n    b,\n    c\n  ]\n  proxy-url: x\nrouting:\n  strategy: [round-robin\n",
			"yaml: line 10: did not find expected ',' or ']'"},
		{"valid multi-line quote at the start", "a: \"x\n  y\n  z\n  w\"\nb: \"open\n",
			"yaml: line 5: found unexpected end of stream"},
		{"CR line endings", "a: 1\rb:\r  c: 1\r d: 2\r",
			"yaml: line 4: did not find expected key"},
		{"CRLF line endings", "a: 1\r\nb:\r\n  c: 1\r\n d: 2\r\n",
			"yaml: line 4: did not find expected key"},
	}
	for _, tc := range cases {
		var node yaml.Node
		err := yaml.Unmarshal([]byte(tc.body), &node)
		if err == nil {
			t.Fatalf("%s: body parsed", tc.name)
		}
		if got := DescribeYAMLSyntaxError([]byte(tc.body), err); got != tc.want {
			t.Errorf("%s: got %q, want %q (yaml.v3 said %q)", tc.name, got, tc.want, err.Error())
		}
	}
}

func TestDescribeYAMLSyntaxErrorLeavesLargeBodiesToYAMLv3(t *testing.T) {
	body := []byte("root:\n" + strings.Repeat("  key: value\n", maxYAMLSyntaxSearchBytes/13) + " bad: 1\n")
	var node yaml.Node
	err := yaml.Unmarshal(body, &node)
	if err == nil {
		t.Fatal("body parsed")
	}
	if got := DescribeYAMLSyntaxError(body, err); got != err.Error() {
		t.Errorf("got %q, want yaml.v3's %q", got, err.Error())
	}
}
