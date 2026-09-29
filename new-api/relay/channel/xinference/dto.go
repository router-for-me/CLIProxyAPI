package xinference

type XinRerankResponseDocument struct {
	Document       any     `json:"document,omitempty"`
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

type XinRerankResponse struct {
	Results []XinRerankResponseDocument `json:"results"`
}
