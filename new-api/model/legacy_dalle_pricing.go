package model

// Deprecated model compatibility: OpenAI removed DALL·E 2 and DALL·E 3 on
// 2026-05-12. Keep conversion of existing prices available for compatible
// upstreams, deriving every multiplier from the legacy DTO billing logic.
// https://developers.openai.com/api/docs/deprecations#2025-11-14-dalle-model-snapshots

import (
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

func legacyDallePricingRules(name string) []LegacyPricingRule {
	if !strings.HasPrefix(name, "dall-e") {
		return nil
	}
	var rules []LegacyPricingRule
	if name != "dall-e-3" {
		for _, size := range []string{"256x256", "512x512"} {
			request := dto.ImageRequest{Model: name, Size: size}
			rules = append(rules, LegacyPricingRule{`param("size") == "` + size + `"`, request.GetTokenCountMeta().ImagePriceRatio})
		}
	}
	rectangle := dto.ImageRequest{Model: name, Size: "1024x1792"}
	if name != "dall-e-2" && name != "dall-e" {
		rules = append(rules, LegacyPricingRule{`param("size") == "1024x1792" || param("size") == "1792x1024"`, rectangle.GetTokenCountMeta().ImagePriceRatio})
	}
	if name == "dall-e-3" {
		squareHD := dto.ImageRequest{Model: name, Size: "1024x1024", Quality: "hd"}
		rectangleHD := dto.ImageRequest{Model: name, Size: rectangle.Size, Quality: "hd"}
		rules = append(rules,
			LegacyPricingRule{`param("quality") == "hd" && param("size") != "1024x1792" && param("size") != "1792x1024"`, squareHD.GetTokenCountMeta().ImagePriceRatio},
			LegacyPricingRule{`param("quality") == "hd" && (param("size") == "1024x1792" || param("size") == "1792x1024")`, rectangleHD.GetTokenCountMeta().ImagePriceRatio / rectangle.GetTokenCountMeta().ImagePriceRatio})
	}
	return rules
}
