package advancedcustom

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func (a *Adaptor) doSGLangRerankResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (any, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)
	// SGLang v0.5.19 returns a sorted array with score, not a Jina results envelope.
	// https://github.com/sgl-project/sglang/blob/v0.5.19/python/sglang/srt/entrypoints/openai/serving_rerank.py
	var results []struct {
		Index *int     `json:"index"`
		Score *float64 `json:"score"`
	}
	if err := common.DecodeJson(resp.Body, &results); err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	if results == nil {
		return nil, types.NewOpenAIError(errors.New("invalid SGLang rerank response"), types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	response := dto.RerankResponse{Results: make([]dto.RerankResponseResult, 0, len(results))}
	for _, result := range results {
		if result.Index == nil || result.Score == nil || *result.Index < 0 || *result.Index >= len(info.Documents) {
			return nil, types.NewOpenAIError(errors.New("invalid SGLang rerank result"), types.ErrorCodeBadResponseBody, http.StatusBadGateway)
		}
		item := dto.RerankResponseResult{Index: *result.Index, RelevanceScore: *result.Score}
		if info.ReturnDocuments {
			item.Document = dto.RerankDocument{Text: info.Documents[*result.Index]}
		}
		response.Results = append(response.Results, item)
	}
	// top_n can omit scored documents, and decoder rerankers can omit meta_info.
	// Per-result counts therefore cannot account for the full request; use the
	// existing prompt estimate, as with other rerank providers without usage.
	response.Usage = dto.Usage{PromptTokens: info.GetEstimatePromptTokens(), TotalTokens: info.GetEstimatePromptTokens()}
	c.JSON(http.StatusOK, response)
	return &response.Usage, nil
}
