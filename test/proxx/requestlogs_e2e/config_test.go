package requestlogse2e

import (
	"fmt"
	"strings"
)

// providers maps an openai-compatibility provider name to its "upstream: alias" models.
var providers = []struct {
	name   string
	models [][2]string
}{
	{"pok", [][2]string{{"up-ok", "model-ok"}}},
	{"p429", [][2]string{{"up-429", "m429"}}},
	{"p400", [][2]string{{"up-400", "m400"}}},
}

func renderConfig(port int, upstream, authDir, extraProxx string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `config-version: 8
server:
  host: 127.0.0.1
  port: %d
management:
  secret-key: %q
  disable-control-panel: true
access:
  api-keys: [%q]
oauth:
  auth-dir: %q
observability:
  logs:
    request-log: true
routing:
  retry:
    request-retry: 0
api-keys:
  openai-compatibility:
`, port, mgmtKey, clientKey, authDir)
	for _, p := range providers {
		fmt.Fprintf(&b, "    - name: %s\n      base-url: %s/%s/v1\n      request-retry: 0\n      keys:\n        - api-key: mock-key-%s\n      models:\n", p.name, upstream, p.name, p.name)
		for _, m := range p.models {
			fmt.Fprintf(&b, "        - name: %s\n          alias: %s\n", m[0], m[1])
		}
	}
	b.WriteString("proxx:\n  model-groups:\n    - name: auto\n      models: [m429, model-ok]\n")
	b.WriteString(extraProxx)
	return b.String()
}
