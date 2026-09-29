package ali

import (
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/samber/lo"
)

// https://help.aliyun.com/document_detail/613695.html?spm=a2c4g.2399480.0.0.1adb778fAdzP9w#341800c0f8w0r

const EnableSearchModelSuffix = "-internet"

func requestOpenAI2Ali(request dto.GeneralOpenAIRequest, upstreamModelName string) *dto.GeneralOpenAIRequest {
	modelName := upstreamModelName
	if modelName == "" {
		modelName = request.Model
	}
	if !dto.IsQwenThinkingBudgetModel(modelName) {
		request.ThinkingBudget = nil
	}

	// DashScope rejects top_p at the 0 and 1 boundaries, so an explicit value is
	// clamped into the open interval. The clamp stays at two decimals because
	// some models on the platform reject a third decimal with
	// "top_p参数非法：限制小数点[2]位".
	//
	// A request that omits top_p is left untouched: injecting a value would
	// silently replace the model's own default with near-greedy decoding.
	if request.TopP != nil {
		if *request.TopP >= 1 {
			request.TopP = lo.ToPtr(0.99)
		} else if *request.TopP <= 0 {
			request.TopP = lo.ToPtr(0.01)
		}
	}
	return &request
}
