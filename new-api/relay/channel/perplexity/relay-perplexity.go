package perplexity

import "github.com/QuantumNous/new-api/relaykit/dto"

func requestOpenAI2Perplexity(request dto.GeneralOpenAIRequest) *dto.GeneralOpenAIRequest {
	messages := make([]dto.Message, 0, len(request.Messages))
	for _, message := range request.Messages {
		messages = append(messages, dto.Message{
			Role:    message.Role,
			Content: message.Content,
		})
	}
	req := &dto.GeneralOpenAIRequest{
		Model:                  request.Model,
		Stream:                 request.Stream,
		Messages:               messages,
		Temperature:            request.Temperature,
		TopP:                   request.TopP,
		FrequencyPenalty:       request.FrequencyPenalty,
		PresencePenalty:        request.PresencePenalty,
		SearchDomainFilter:     request.SearchDomainFilter,
		SearchRecencyFilter:    request.SearchRecencyFilter,
		ReturnImages:           request.ReturnImages,
		ReturnRelatedQuestions: request.ReturnRelatedQuestions,
		SearchMode:             request.SearchMode,
	}
	if request.MaxTokens != nil || request.MaxCompletionTokens != nil {
		maxTokens := request.GetMaxTokens()
		req.MaxTokens = &maxTokens
	}
	return req
}
