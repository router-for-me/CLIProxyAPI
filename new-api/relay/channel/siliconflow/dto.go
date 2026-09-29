package siliconflow

import "github.com/QuantumNous/new-api/relaykit/dto"

type SFTokens struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type SFMeta struct {
	Tokens SFTokens `json:"tokens"`
}

type SFRerankResponse struct {
	Results []dto.RerankResponseResult `json:"results"`
	Meta    SFMeta                     `json:"meta"`
}

type SFImageRequest struct {
	Model             string  `json:"model"`
	Prompt            string  `json:"prompt"`
	NegativePrompt    string  `json:"negative_prompt,omitempty"`
	ImageSize         string  `json:"image_size,omitempty"`
	BatchSize         uint    `json:"batch_size,omitempty"`
	Seed              uint64  `json:"seed,omitempty"`
	NumInferenceSteps uint    `json:"num_inference_steps,omitempty"`
	GuidanceScale     float64 `json:"guidance_scale,omitempty"`
	Cfg               float64 `json:"cfg,omitempty"`
	Image             string  `json:"image,omitempty"`
	Image2            string  `json:"image2,omitempty"`
	Image3            string  `json:"image3,omitempty"`
}
