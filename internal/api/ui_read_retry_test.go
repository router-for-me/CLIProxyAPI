package api

import (
	"strings"
	"testing"
)

func TestManagementReadRetryTransform(t *testing.T) {
	input := "<script>class API{" + upstreamManagementGET + "async post(){}}</script>"
	output := injectManagementReadRetry(input)
	if !strings.Contains(output, resilientManagementGET) || !strings.Contains(output, "async post(){}") {
		t.Fatal("GET transform failed or affected write method")
	}
	if injectManagementReadRetry(output) != output {
		t.Fatal("transform is not idempotent")
	}
	for _, unknown := range []string{"unrecognized upstream", input + input} {
		if injectManagementReadRetry(unknown) != unknown {
			t.Fatal("ambiguous upstream must not be rewritten")
		}
	}
}
