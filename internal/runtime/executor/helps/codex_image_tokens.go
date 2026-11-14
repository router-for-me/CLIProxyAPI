package helps

import (
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"strings"
	"time"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
)

// Codex itself uses this coarse estimate for resized images when dimensions or
// model-specific rules are unavailable. Never tokenize base64 or fetch URLs.
// https://github.com/openai/codex/blob/main/codex-rs/core/src/context_manager/history.rs
const codexFallbackImageTokens int64 = 1844

type codexImagePolicy struct {
	dimension  float64
	patches    float64
	multiplier float64
	base, tile int64
}

func codexImageModelFamily(model, family string) bool {
	if model == family {
		return true
	}
	snapshot, ok := strings.CutPrefix(model, family+"-")
	if !ok {
		return false
	}
	_, err := time.Parse("2006-01-02", snapshot)
	return err == nil
}

func codexImagePolicyFor(model, detail string) codexImagePolicy {
	model = strings.ToLower(strings.TrimSpace(model))
	// Keep policies limited to documented model families. A shared family prefix
	// does not establish identical image preprocessing for undocumented variants.
	// https://developers.openai.com/api/docs/guides/images-vision#model-sizing-behavior
	switch {
	case codexImageModelFamily(model, "gpt-6-astra"),
		codexImageModelFamily(model, "gpt-5.6-sol"), codexImageModelFamily(model, "gpt-5.6-terra"), codexImageModelFamily(model, "gpt-5.6-luna"):
		if detail == "low" {
			return codexImagePolicy{dimension: 512, multiplier: 1.2}
		}
		if detail == "high" {
			dimension := 2048.0
			if codexImageModelFamily(model, "gpt-6-astra") {
				dimension = 65535
			}
			return codexImagePolicy{dimension: dimension, patches: 2500, multiplier: 1.2}
		}
		return codexImagePolicy{dimension: 65535, multiplier: 1.2}
	case codexImageModelFamily(model, "gpt-5.5"):
		if detail == "low" {
			return codexImagePolicy{dimension: 512, multiplier: 1.2}
		}
		if detail == "high" {
			return codexImagePolicy{dimension: 2048, patches: 2500, multiplier: 1.2}
		}
		return codexImagePolicy{dimension: 6000, patches: 10000, multiplier: 1.2}
	case codexImageModelFamily(model, "gpt-5.4"), codexImageModelFamily(model, "gpt-5.4-mini"), codexImageModelFamily(model, "gpt-5.4-nano"):
		if detail == "original" {
			return codexImagePolicy{dimension: 6000, patches: 10000, multiplier: 1.2}
		}
		if detail == "low" {
			// OpenAI documents a larger patch budget for low than high/auto
			// in the GPT-5.4 family; low can therefore use more tokens.
			return codexImagePolicy{dimension: 2048, patches: 6144, multiplier: 1.2}
		}
		return codexImagePolicy{dimension: 2048, patches: 2500, multiplier: 1.2}
	case codexImageModelFamily(model, "gpt-5.2"):
		return codexImagePolicy{dimension: 2048, patches: 6144, multiplier: 1.2}
	case codexImageModelFamily(model, "gpt-4.1-mini"):
		return codexImagePolicy{dimension: 2048, patches: 6144, multiplier: 1.62}
	case codexImageModelFamily(model, "gpt-4o-mini"):
		return codexImagePolicy{base: 2833, tile: 5667}
	case codexImageModelFamily(model, "gpt-4o"), codexImageModelFamily(model, "gpt-4.1"):
		return codexImagePolicy{base: 85, tile: 170}
	case codexImageModelFamily(model, "gpt-5.1"), codexImageModelFamily(model, "gpt-5"):
		return codexImagePolicy{base: 70, tile: 140}
	}
	return codexImagePolicy{}
}

func codexInlineImageDimensions(url string) (float64, float64, bool) {
	header, data, found := strings.Cut(url, ",")
	if !found || !strings.HasPrefix(strings.ToLower(header), "data:image/") {
		return 0, 0, false
	}
	encoded := false
	for _, parameter := range strings.Split(header, ";")[1:] {
		if strings.EqualFold(parameter, "base64") {
			encoded = true
		}
	}
	if !encoded {
		return 0, 0, false
	}
	// Decode headers only, with bounded work even for a malformed JPEG or huge data
	// URL. No decoded pixel buffer is allocated and URL/file references stay local.
	reader := io.LimitReader(base64.NewDecoder(base64.StdEncoding, strings.NewReader(data)), 1<<20)
	config, _, err := image.DecodeConfig(reader)
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return 0, 0, false
	}
	return float64(config.Width), float64(config.Height), true
}

func estimateCodexImageTokens(model string, part ojson.Result) int64 {
	detail := strings.ToLower(part.Get("detail").String())
	policy := codexImagePolicyFor(model, detail)
	if policy.base > 0 && detail == "low" {
		return policy.base
	}
	width, height, known := codexInlineImageDimensions(part.Get("image_url").String())
	if !known {
		if detail == "original" && part.Get("file_id").String() != "" {
			return 10000
		}
		return codexFallbackImageTokens
	}
	if policy.multiplier > 0 {
		scale := math.Min(1, policy.dimension/math.Max(width, height))
		width = math.Max(1, math.Floor(width*scale))
		height = math.Max(1, math.Floor(height*scale))
		if policy.patches > 0 && math.Ceil(width/32)*math.Ceil(height/32) > policy.patches {
			shrink := math.Sqrt(1024 * policy.patches / (width * height))
			shrink = math.Min(1, shrink)
			w, h := width*shrink/32, height*shrink/32
			shrink *= math.Min(math.Floor(w)/w, math.Floor(h)/h)
			width = math.Max(1, math.Floor(width*shrink))
			height = math.Max(1, math.Floor(height*shrink))
		}
		return int64(math.Ceil(math.Ceil(width/32) * math.Ceil(height/32) * policy.multiplier))
	}
	if policy.base > 0 {
		scale := math.Min(1, 2048/math.Max(width, height))
		width *= scale
		height *= scale
		scale = math.Min(1, 768/math.Min(width, height))
		width = math.Max(1, math.Floor(width*scale))
		height = math.Max(1, math.Floor(height*scale))
		return policy.base + int64(math.Ceil(width/512)*math.Ceil(height/512))*policy.tile
	}
	if detail == "original" {
		// Match the native Codex fallback without asserting undocumented family rules.
		return int64(math.Min(10000, math.Ceil(width/32)*math.Ceil(height/32)))
	}
	return codexFallbackImageTokens
}
