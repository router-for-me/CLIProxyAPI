package helps

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

type groundingURLRoundTripper func(*http.Request) (*http.Response, error)

func (f groundingURLRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestResolveAntigravityGroundingURLsResolvesVertexRedirects(t *testing.T) {
	t.Parallel()

	const redirectURL = "https://vertexaisearch.cloud.google.com/grounding-api-redirect/example-token"
	const resolvedURL = "https://example.com/weather"

	var sawRedirectRequest bool
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", groundingURLRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodHead {
			t.Fatalf("method = %s, want HEAD", req.Method)
		}
		if req.URL.String() != redirectURL {
			t.Fatalf("url = %s, want %s", req.URL.String(), redirectURL)
		}
		sawRedirectRequest = true
		return &http.Response{
			StatusCode: http.StatusFound,
			Header: http.Header{
				"Location": []string{resolvedURL},
			},
			Body: io.NopCloser(strings.NewReader("")),
		}, nil
	}))

	input := []byte(`{
		"response": {
			"candidates": [{
				"groundingMetadata": {
					"groundingChunks": [
						{"web": {"uri": "` + redirectURL + `", "title": "Weather"}},
						{"web": {"uri": "https://already.example/source", "title": "Existing"}}
					]
				}
			}]
		}
	}`)

	output := ResolveAntigravityGroundingURLs(ctx, nil, nil, input)
	if !sawRedirectRequest {
		t.Fatal("expected resolver to request the vertex redirect")
	}
	if got := gjson.GetBytes(output, "response.candidates.0.groundingMetadata.groundingChunks.0.web.uri").String(); got != resolvedURL {
		t.Fatalf("resolved uri = %q, want %q; output=%s", got, resolvedURL, output)
	}
	if got := gjson.GetBytes(output, "response.candidates.0.groundingMetadata.groundingChunks.1.web.uri").String(); got != "https://already.example/source" {
		t.Fatalf("non-vertex uri = %q", got)
	}
}
