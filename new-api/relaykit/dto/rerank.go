package dto

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/types"
)

type RerankRequest struct {
	Documents       []any  `json:"documents"`
	Query           string `json:"query"`
	Model           string `json:"model"`
	TopN            *int   `json:"top_n,omitempty"`
	ReturnDocuments *bool  `json:"return_documents,omitempty"`
	MaxChunkPerDoc  *int   `json:"max_chunk_per_doc,omitempty"`
	OverLapTokens   *int   `json:"overlap_tokens,omitempty"`
}

func (r *RerankRequest) IsStream(c *http.Request) bool {
	return false
}

func (r *RerankRequest) GetTokenCountMeta() *types.TokenCountMeta {
	var texts = make([]string, 0)

	for _, document := range r.Documents {
		texts = append(texts, fmt.Sprintf("%v", document))
	}

	if r.Query != "" {
		texts = append(texts, r.Query)
	}

	return &types.TokenCountMeta{
		CombineText: strings.Join(texts, "\n"),
	}
}

func (r *RerankRequest) SetModelName(modelName string) {
	if modelName != "" {
		r.Model = modelName
	}
}

func (r *RerankRequest) GetReturnDocuments() bool {
	if r.ReturnDocuments == nil {
		return false
	}
	return *r.ReturnDocuments
}

type RerankResponseResult struct {
	Document       any     `json:"document,omitempty"`
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

type RerankDocument struct {
	Text any `json:"text"`
}

type RerankResponse struct {
	Results []RerankResponseResult `json:"results"`
	Usage   Usage                  `json:"usage"`
}
