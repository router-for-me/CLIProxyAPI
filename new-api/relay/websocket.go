package relay

import (
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/pkg/wsmanager"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func WssHelper(c *gin.Context, info *relaycommon.RelayInfo) (newAPIError *types.NewAPIError) {
	info.InitChannelMeta(c)

	adaptor := GetAdaptor(info.ApiType)
	if adaptor == nil {
		return types.NewError(fmt.Errorf("invalid api type: %d", info.ApiType), types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}
	adaptor.Init(info)
	//var requestBody io.Reader
	//firstWssRequest, _ := c.Get("first_wss_request")
	//requestBody = bytes.NewBuffer(firstWssRequest.([]byte))

	statusCodeMappingStr := c.GetString("status_code_mapping")
	resp, err := adaptor.DoRequest(c, info, nil)
	if err != nil {
		return types.NewError(err, types.ErrorCodeDoRequestFailed)
	}

	if resp != nil {
		info.TargetWs = resp.(*websocket.Conn)
		defer info.TargetWs.Close()
		var closeOnce sync.Once
		unregister := wsmanager.Register(info.ChannelId, wsmanager.KindRealtime, func(reason string) {
			closeOnce.Do(func() {
				deadline := time.Now().Add(time.Second)
				closeMessage := websocket.FormatCloseMessage(websocket.ClosePolicyViolation, reason)
				_ = info.ClientWs.WriteControl(websocket.CloseMessage, closeMessage, deadline)
				_ = info.TargetWs.WriteControl(websocket.CloseMessage, closeMessage, deadline)
				_ = info.ClientWs.Close()
				_ = info.TargetWs.Close()
			})
		})
		defer unregister()
	}

	usage, newAPIError := adaptor.DoResponse(c, nil, info)
	if newAPIError != nil {
		// reset status code 重置状态码
		service.ResetStatusCode(newAPIError, statusCodeMappingStr)
		return newAPIError
	}
	service.PostWssConsumeQuota(c, info, info.UpstreamModelName, usage.(*dto.RealtimeUsage), "")
	return nil
}
