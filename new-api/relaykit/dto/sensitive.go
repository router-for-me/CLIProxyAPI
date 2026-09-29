package dto

type SensitiveResponse struct {
	SensitiveWords []string `json:"sensitive_words"`
	Content        string   `json:"content"`
}
