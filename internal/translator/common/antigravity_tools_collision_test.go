package common

import "testing"

func TestIssue5190AntigravityToolNamesAreInjective(t *testing.T) {
	seen := map[string]string{}
	for _, name := range []string{"read_file", "external_read_file", "external_external_read_file", "write_file", "external_write_file", "execute_code", "external_execute_code", "external_lookup"} {
		upstream := AntigravityToolNameToUpstream(name)
		if previous, exists := seen[upstream]; exists {
			t.Errorf("%q and %q collide as %q", previous, name, upstream)
		}
		seen[upstream] = name
		if restored := AntigravityUpstreamToolNameToClient(upstream); restored != name {
			t.Errorf("round trip %q became %q", name, restored)
		}
	}
}
