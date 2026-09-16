package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

func TestHostModelErrorEnvelope(t *testing.T) {
	for _, method := range []string{pluginabi.MethodHostModelExecute, pluginabi.MethodHostModelExecuteStream} {
		for _, status := range []int{503, 429, 400, 401, 422, 0} {
			t.Run(fmt.Sprintf("%s/%d", method, status), func(t *testing.T) {
				cause := errors.New("  opaque failure\n")
				msg := &interfaces.ErrorMessage{StatusCode: status, Error: cause}
				host := New()
				host.SetModelExecutor(&fakeHostModelExecutor{
					executeModel: func(context.Context, handlers.ModelExecutionRequest) (handlers.ModelExecutionResponse, *interfaces.ErrorMessage) {
						return handlers.ModelExecutionResponse{}, msg
					},
					executeModelStream: func(context.Context, handlers.ModelExecutionRequest) (handlers.ModelExecutionStream, *interfaces.ErrorMessage) {
						return handlers.ModelExecutionStream{}, msg
					},
				})
				req := []byte(fmt.Sprintf(`{"model":"synthetic","stream":%t}`, method == pluginabi.MethodHostModelExecuteStream))
				_, err := host.callFromPlugin(context.Background(), method, req)
				if err == nil {
					t.Fatal("expected callback error")
				}
				if !errors.Is(err, cause) {
					t.Fatal("callback lost original error")
				}
				var envelope pluginabi.Envelope
				if err := json.Unmarshal(marshalHostCallError(err), &envelope); err != nil {
					t.Fatal(err)
				}
				want := status
				if want == 0 {
					want = 500
				}
				if envelope.OK || envelope.Error == nil || envelope.Error.Code != "host_call_failed" || envelope.Error.HTTPStatus != want || envelope.Error.Message != cause.Error() {
					t.Fatalf("unexpected envelope: %+v", envelope.Error)
				}
			})
		}
	}
}

func TestHostCallErrorStatusValidation(t *testing.T) {
	for _, status := range []int{-1, 0, 200, 399, 400, 429, 503, 599, 600, int(^uint(0) >> 1)} {
		err := fmt.Errorf("outer: %w", rpcError{message: " opaque ", statusCode: status})
		var envelope pluginabi.Envelope
		if err := json.Unmarshal(marshalHostCallError(err), &envelope); err != nil {
			t.Fatal(err)
		}
		want := status
		if want < 400 || want > 599 {
			want = 0
		}
		if envelope.OK || envelope.Error == nil || envelope.Error.Code != "host_call_failed" || envelope.Error.HTTPStatus != want || envelope.Error.Message != err.Error() {
			t.Fatalf("status %d: %+v", status, envelope.Error)
		}
	}
	for _, cause := range []error{context.Canceled, fmt.Errorf("outer: %w", context.Canceled), errors.New("upstream returned 503")} {
		var envelope pluginabi.Envelope
		if err := json.Unmarshal(marshalHostCallError(cause), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.OK || envelope.Error == nil || envelope.Error.Code != "host_call_failed" || envelope.Error.HTTPStatus != 0 || envelope.Error.Message != cause.Error() {
			t.Fatalf("unexpected untyped error: %+v", envelope.Error)
		}
	}
}
