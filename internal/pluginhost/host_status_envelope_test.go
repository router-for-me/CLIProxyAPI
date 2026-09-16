package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

func TestHostErrorTreeStatus(t *testing.T) {
	upstream := &rpcError{message: " opaque upstream failure ", statusCode: 503}
	rateLimit := &rpcError{message: "rate limited", statusCode: 429}
	base := &auth.Error{Code: "auth_unavailable", Message: "no auth available"}
	// Match the scheduler's actual zero-status wrapper around its last upstream error.
	wrapped := auth.WithCause(base, upstream)
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"auth unavailable", wrapped, 503},
		{"fmt wrapper", fmt.Errorf("outer: %w", wrapped), 503},
		{"nested invalid", auth.WithCause(&auth.Error{HTTPStatus: 200}, fmt.Errorf("middle: %w", wrapped)), 503},
		{"valid outer", auth.WithCause(&auth.Error{HTTPStatus: 422}, wrapped), 422},
		{"lower boundary", auth.WithCause(&auth.Error{HTTPStatus: 400}, wrapped), 400},
		{"upper boundary", auth.WithCause(&auth.Error{HTTPStatus: 599}, wrapped), 599},
		{"join depth first", errors.Join(wrapped, rateLimit), 503},
		{"join reversed", errors.Join(rateLimit, wrapped), 429},
		{"join skip invalid branch", errors.Join(&auth.Error{HTTPStatus: 200}, wrapped), 503},
		{"join nested", errors.Join(errors.Join(errors.New("plain"), wrapped), rateLimit), 503},
		{"invalid outer join", auth.WithCause(&auth.Error{}, errors.Join(rateLimit, upstream)), 429},
		{"valid outer join", auth.WithCause(&auth.Error{HTTPStatus: 401}, errors.Join(rateLimit, upstream)), 401},
		{"no valid", errors.Join(auth.WithCause(&auth.Error{HTTPStatus: 600}, &auth.Error{HTTPStatus: 399}), errors.New("upstream returned 503")), 0},
	}
	for _, status := range []int{0, -1, 200, 399, 600, int(^uint(0) >> 1)} {
		for _, cause := range []*rpcError{rateLimit, upstream} {
			cases = append(cases, struct {
				name string
				err  error
				want int
			}{fmt.Sprintf("outer %d/inner %d", status, cause.statusCode), auth.WithCause(&auth.Error{HTTPStatus: status}, cause), cause.statusCode})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var envelope pluginabi.Envelope
			if err := json.Unmarshal(marshalHostCallError(tc.err), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.OK || envelope.Error == nil || envelope.Error.Code != "host_call_failed" || envelope.Error.HTTPStatus != tc.want || envelope.Error.Message != tc.err.Error() {
				t.Errorf("raw envelope = %#v, want status %d and unchanged message", envelope.Error, tc.want)
			}
			for _, fallback := range []int{401, 0} {
				for _, method := range []string{pluginabi.MethodHostModelExecute, pluginabi.MethodHostModelExecuteStream} {
					t.Run(fmt.Sprintf("%s/fallback %d", method, fallback), func(t *testing.T) {
						msg := &interfaces.ErrorMessage{StatusCode: fallback, Error: tc.err}
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
						if err == nil || err.Error() != tc.err.Error() || !errors.Is(err, tc.err) {
							t.Fatal("callback lost message or error identity")
						}
						var original, recovered *auth.Error
						if errors.As(tc.err, &original) && (!errors.As(err, &recovered) || original != recovered || !errors.Is(err, original)) {
							t.Fatal("auth error identity lost")
						}
						var originalUpstream, recoveredUpstream *rpcError
						if errors.As(tc.err, &originalUpstream) && (!errors.As(err, &recoveredUpstream) || originalUpstream != recoveredUpstream || !errors.Is(err, originalUpstream)) {
							t.Fatal("upstream error identity lost")
						}
						want := tc.want
						if want == 0 {
							want = fallback
							if want == 0 {
								want = 500
							}
						}
						var statusErr interface{ StatusCode() int }
						if !errors.As(err, &statusErr) {
							t.Fatal("missing returned status accessor")
						}
						if got := statusErr.StatusCode(); got != want {
							t.Errorf("returned status = %d, want %d", got, want)
						}
						var envelope pluginabi.Envelope
						if err := json.Unmarshal(marshalHostCallError(err), &envelope); err != nil {
							t.Fatal(err)
						}
						if envelope.OK || envelope.Error == nil || envelope.Error.Code != "host_call_failed" || envelope.Error.HTTPStatus != want || envelope.Error.Message != tc.err.Error() {
							t.Errorf("callback envelope = %#v, want status %d and unchanged message", envelope.Error, want)
						}
					})
				}
			}
		})
	}
}

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
