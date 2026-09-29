package task_pricing_setting

import (
	"strings"

	"github.com/QuantumNous/new-api/setting/config"
	"github.com/samber/lo"
)

type TaskPricingSetting struct {
	SoraSizeRatio      map[string]float64 `json:"sora_size_ratio"`
	VertexResolution4K map[string]float64 `json:"vertex_resolution_4k_ratio"`
}

var taskPricingSetting = TaskPricingSetting{
	SoraSizeRatio: map[string]float64{
		"1792x1024": 1.666667,
		"1024x1792": 1.666667,
	},
	VertexResolution4K: map[string]float64{
		"veo-3.1-fast-generate": 2.333333,
		"veo-3.1-generate":      1.5,
		"veo-3.1":               1.5,
	},
}

func init() {
	config.GlobalConfig.Register("task_pricing_setting", &taskPricingSetting)
}

func SoraSizeRatio(size string) float64 {
	if ratio, ok := taskPricingSetting.SoraSizeRatio[size]; ok && ratio > 0 {
		return ratio
	}
	return 1
}

func VertexResolutionRatio(model, resolution string) float64 {
	if !strings.EqualFold(resolution, "4k") {
		return 1
	}
	matchedPattern := ""
	matchedRatio := 1.0
	for pattern, ratio := range taskPricingSetting.VertexResolution4K {
		if !strings.Contains(model, pattern) || ratio <= 0 {
			continue
		}
		if len(pattern) > len(matchedPattern) || (len(pattern) == len(matchedPattern) && pattern < matchedPattern) {
			matchedPattern = pattern
			matchedRatio = ratio
		}
	}
	return matchedRatio
}

func GetCopy() TaskPricingSetting {
	return TaskPricingSetting{
		SoraSizeRatio:      lo.Assign(taskPricingSetting.SoraSizeRatio),
		VertexResolution4K: lo.Assign(taskPricingSetting.VertexResolution4K),
	}
}
