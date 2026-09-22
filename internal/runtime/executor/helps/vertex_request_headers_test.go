package helps

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

func TestApplyVertexRequestHeaders(t *testing.T) {
	const requestType = "X-Vertex-AI-LLM-Request-Type"
	const sharedType = "X-Vertex-AI-LLM-Shared-Request-Type"
	incoming := make(http.Header)
	incoming.Set(requestType, "shared")
	incoming.Set(sharedType, "flex")
	incoming.Set("Authorization", "Bearer client-secret")
	incoming.Set("X-Unrelated", "must-not-forward")
	req, err := http.NewRequest(http.MethodPost, "https://example.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer upstream-token")
	ApplyVertexRequestHeaders(req, incoming)
	if req.Header.Get(requestType) != "shared" || req.Header.Get(sharedType) != "flex" {
		t.Fatal("explicit Flex request was not forwarded")
	}
	if req.Header.Get("Authorization") != "Bearer upstream-token" || req.Header.Get("X-Unrelated") != "" {
		t.Fatal("forwarded unrelated client headers")
	}
	util.ApplyCustomHeadersFromAttrs(req, map[string]string{"header:" + sharedType: "standard"}, incoming)
	if req.Header.Get(sharedType) != "standard" {
		t.Fatal("configured administrator override was not preserved")
	}
	// The next request with the toggle off must not inherit the prior tier.
	off := &http.Request{}
	ApplyVertexRequestHeaders(off, nil)
	if off.Header.Get(requestType) != "" || off.Header.Get(sharedType) != "" {
		t.Fatal("Flex was enabled without client headers")
	}
	ApplyVertexRequestHeaders(nil, incoming)
}
