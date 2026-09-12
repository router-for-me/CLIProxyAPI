package executor

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatNativeResponsesDoesNotLoseInteractionsIncompleteOutcome(t *testing.T) {
	for _, format := range []sdktranslator.Format{sdktranslator.FormatOpenAIResponse, sdktranslator.FormatInteractions} {
		for _, status := range []string{"completed", "incomplete"} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t", format, status, stream), func(t *testing.T) {
					body := fmt.Sprintf(`{"id":"resp_1","status":%q,"output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`, status)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.%s\",\"response\":%s}\n\n", status, body)
						} else {
							w.Header().Set("Content-Type", "application/json")
							_, _ = fmt.Fprint(w, body)
						}
					}))
					defer server.Close()
					executor, auth := nativeResponsesExecutor(server.URL)
					req, opts := nativeResponsesRequest(stream)
					opts.ResponseFormat = format
					var output strings.Builder
					var err error
					if stream {
						result, executeErr := executor.ExecuteStream(t.Context(), auth, req, opts)
						err = executeErr
						if result != nil {
							for chunk := range result.Chunks {
								if chunk.Err != nil {
									err = chunk.Err
								}
								output.Write(chunk.Payload)
							}
						}
					} else {
						result, executeErr := executor.Execute(t.Context(), auth, req, opts)
						err = executeErr
						output.Write(result.Payload)
					}
					if err != nil {
						// A converter without this outcome must fail explicitly until it supports it.
						code, ok := err.(interface{ StatusCode() int })
						scope, scoped := err.(interface{ IsRequestScoped() bool })
						if format != sdktranslator.FormatInteractions || status != "incomplete" || !ok || code.StatusCode() != http.StatusBadGateway || !scoped || !scope.IsRequestScoped() || !strings.Contains(err.Error(), "incomplete") || output.Len() != 0 {
							t.Fatalf("unexpected error = %v, output = %s", err, output.String())
						}
						return
					}
					if !stream {
						if got := gjson.Get(output.String(), "status").String(); got != status {
							t.Fatalf("status = %q, want %q; output: %s", got, status, output.String())
						}
						return
					}
					path := "response.status"
					if format == sdktranslator.FormatInteractions {
						path = "interaction.status"
					}
					for _, line := range strings.Split(output.String(), "\n") {
						if strings.HasPrefix(line, "data:") && gjson.Get(strings.TrimSpace(strings.TrimPrefix(line, "data:")), path).String() == status {
							return
						}
					}
					t.Fatalf("missing terminal status %q; output: %s", status, output.String())
				})
			}
		}
	}
}
