package service

import (
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

func ConvertResponse(c *gin.Context, info *relaycommon.RelayInfo, target types.RelayFormat, response any) (*relayconvert.ResponseResult, error) {
	result, err := relayconvert.ConvertResponse(c, info, target, response)
	if result != nil {
		info.RecordConversionDiagnostics(c, result.Diagnostics)
	}
	return result, err
}

func ConvertStreamResponse(c *gin.Context, info *relaycommon.RelayInfo, target types.RelayFormat, response any) (*relayconvert.ResponseResult, error) {
	result, err := relayconvert.ConvertStreamResponse(c, info, target, response)
	if result != nil {
		info.RecordConversionDiagnostics(c, result.Diagnostics)
	}
	return result, err
}

func ConvertStreamResponseChunk(c *gin.Context, info *relaycommon.RelayInfo, state *relayconvert.ResponseStreamState, response any) ([]relayconvert.ResponseResult, error) {
	results, err := relayconvert.ConvertStreamResponseChunk(c, info, state, response)
	if state != nil {
		info.RecordConversionDiagnostics(c, state.Diagnostics())
	}
	return results, err
}

func FinalizeStreamResponse(c *gin.Context, info *relaycommon.RelayInfo, state *relayconvert.ResponseStreamState) ([]relayconvert.ResponseResult, error) {
	results, err := relayconvert.FinalizeStreamResponse(c, info, state)
	if state != nil {
		info.RecordConversionDiagnostics(c, state.Diagnostics())
	}
	return results, err
}
