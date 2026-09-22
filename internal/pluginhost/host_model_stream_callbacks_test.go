package pluginhost

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestHostModelExecuteStreamDetachesFromCallbackParentCancel(t *testing.T) {
	host := New()
	ctxSeen := make(chan context.Context, 1)
	host.SetModelExecutor(&fakeHostModelExecutor{
		executeModelStream: func(ctx context.Context, req handlers.ModelExecutionRequest) (handlers.ModelExecutionStream, *interfaces.ErrorMessage) {
			ctxSeen <- ctx
			return handlers.ModelExecutionStream{
				StatusCode: http.StatusOK,
				Chunks:     make(chan handlers.ModelExecutionChunk),
			}, nil
		},
	})
	parentCtx, cancelParent := context.WithCancel(context.Background())
	callbackID, closeCallback := host.openCallbackContext(parentCtx)
	defer closeCallback()

	rawReq, errMarshal := json.Marshal(rpcHostModelExecutionRequest{
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol: "openai",
			ExitProtocol:  "openai",
			Model:         "model-1",
			Stream:        true,
			Body:          []byte(`{"stream":true}`),
		},
		HostCallbackID: callbackID,
	})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostModelExecuteStream, rawReq)
	if errCall != nil {
		t.Fatalf("callFromPlugin() error = %v", errCall)
	}
	resp, errDecode := decodeRPCEnvelope[pluginapi.HostModelStreamResponse](rawResp)
	if errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if resp.StreamID == "" {
		t.Fatalf("stream id is empty: %#v", resp)
	}

	var streamCtx context.Context
	select {
	case streamCtx = <-ctxSeen:
	case <-time.After(time.Second):
		t.Fatal("model executor was not called")
	}
	cancelParent()
	select {
	case <-streamCtx.Done():
		t.Fatal("stream context was canceled by callback parent context")
	default:
	}

	closeCallback()
	select {
	case <-streamCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("stream context was not canceled after callback scope closed")
	}
}
