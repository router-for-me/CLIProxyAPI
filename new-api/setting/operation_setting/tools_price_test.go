package operation_setting

import (
	"maps"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func preserveToolPrices(t *testing.T) {
	t.Helper()
	original := make(map[string]float64, len(toolPriceSetting.Prices))
	maps.Copy(original, toolPriceSetting.Prices)
	t.Cleanup(func() {
		toolPriceSetting.Prices = original
		RebuildToolPriceIndex()
	})
}

func TestToolPriceHardcodedFallbacksSurviveMissingOperatorConfig(t *testing.T) {
	preserveToolPrices(t)
	toolPriceSetting.Prices = map[string]float64{}
	RebuildToolPriceIndex()

	expectedDefaults := map[string]float64{
		"web_search":         10,
		"web_search_preview": 10,
		"file_search":        2.5,
		"google_search":      14,
		"image_generation":   150,
	}
	for name, expected := range expectedDefaults {
		assert.Equal(t, expected, GetToolPrice(name), name)
	}
	assert.Equal(t, 25.0, GetToolPriceForModel("web_search_preview", "gpt-4o-2024-11-20"))
	assert.Equal(t, 25.0, GetToolPriceForModel("web_search_preview", "gpt-4.1-mini"))
}

func TestToolPriceOperatorOverridePrecedenceAndExplicitZero(t *testing.T) {
	preserveToolPrices(t)
	toolPriceSetting.Prices = map[string]float64{
		"image_generation":                 0,
		"web_search":                       12,
		"web_search_preview":               0,
		"web_search_preview:gpt-4o*":       30,
		"web_search_preview:gpt-4o-mini*":  0,
		"web_search_preview:custom-model*": 7,
	}
	RebuildToolPriceIndex()

	assert.Equal(t, 0.0, GetToolPrice("image_generation"))
	assert.Equal(t, 12.0, GetToolPrice("web_search"))
	assert.Equal(t, 0.0, GetToolPriceForModel("web_search_preview", "o1"))
	assert.Equal(t, 30.0, GetToolPriceForModel("web_search_preview", "gpt-4o"))
	assert.Equal(t, 0.0, GetToolPriceForModel("web_search_preview", "gpt-4o-mini"))
	assert.Equal(t, 25.0, GetToolPriceForModel("web_search_preview", "gpt-4.1"))
	assert.Equal(t, 7.0, GetToolPriceForModel("web_search_preview", "custom-model-v2"))

	delete(toolPriceSetting.Prices, "web_search_preview:gpt-4o*")
	RebuildToolPriceIndex()
	assert.Equal(t, 25.0, GetToolPriceForModel("web_search_preview", "gpt-4o"))

	delete(toolPriceSetting.Prices, "web_search")
	RebuildToolPriceIndex()
	assert.Equal(t, 10.0, GetToolPrice("web_search"))
}

func TestToolPriceCustomFunctionHasNoHardcodedFallback(t *testing.T) {
	preserveToolPrices(t)
	toolPriceSetting.Prices = map[string]float64{}
	RebuildToolPriceIndex()

	assert.Equal(t, 0.0, GetToolPrice("lookup_customer"))

	toolPriceSetting.Prices["lookup_customer"] = 5
	RebuildToolPriceIndex()
	assert.Equal(t, 5.0, GetToolPrice("lookup_customer"))

	toolPriceSetting.Prices["lookup_customer"] = 0
	RebuildToolPriceIndex()
	assert.Equal(t, 0.0, GetToolPrice("lookup_customer"))
}

func TestValidateToolPricesJSON(t *testing.T) {
	valid := []string{
		`{}`,
		`{"web_search":0}`,
		`{"web_search":10,"custom_fn":2.5}`,
	}
	for _, value := range valid {
		assert.NoError(t, ValidateToolPricesJSON(value), value)
	}

	invalid := []string{
		`null`,
		`[]`,
		`{"web_search":null}`,
		`{"web_search":true}`,
		`{"web_search":"0"}`,
		`{"web_search":-1}`,
		`{"web_search":1e999}`,
		`{"web_search":`,
	}
	for _, value := range invalid {
		assert.Error(t, ValidateToolPricesJSON(value), value)
	}
}

func TestLoadToolPricesFromJSONStringReplacesMapAndKeepsValidSiblings(t *testing.T) {
	preserveToolPrices(t)

	LoadToolPricesFromJSONString(`{
		"web_search": 0,
		"custom_fn": 3,
		"file_search": null,
		"google_search": -1,
		"image_generation": "0"
	}`)

	require.Len(t, toolPriceSetting.Prices, 2)
	assert.Equal(t, 0.0, toolPriceSetting.Prices["web_search"])
	assert.Equal(t, 3.0, toolPriceSetting.Prices["custom_fn"])
	assert.Equal(t, 0.0, GetToolPrice("web_search"))
	assert.Equal(t, 3.0, GetToolPrice("custom_fn"))
	assert.Equal(t, 2.5, GetToolPrice("file_search"))
	assert.Equal(t, 14.0, GetToolPrice("google_search"))
	assert.Equal(t, 150.0, GetToolPrice("image_generation"))

	LoadToolPricesFromJSONString(`{"image_generation":0}`)
	require.Len(t, toolPriceSetting.Prices, 1)
	assert.NotContains(t, toolPriceSetting.Prices, "web_search")
	assert.NotContains(t, toolPriceSetting.Prices, "custom_fn")
	assert.Equal(t, 10.0, GetToolPrice("web_search"))
	assert.Equal(t, 0.0, GetToolPrice("custom_fn"))
	assert.Equal(t, 0.0, GetToolPrice("image_generation"))
}

func TestRebuildToolPriceIndexIgnoresInvalidDirectValues(t *testing.T) {
	preserveToolPrices(t)
	toolPriceSetting.Prices = map[string]float64{
		"web_search":       -1,
		"file_search":      math.Inf(1),
		"image_generation": math.NaN(),
		"custom_fn":        math.NaN(),
	}
	RebuildToolPriceIndex()

	assert.Equal(t, 10.0, GetToolPrice("web_search"))
	assert.Equal(t, 2.5, GetToolPrice("file_search"))
	assert.Equal(t, 150.0, GetToolPrice("image_generation"))
	assert.Equal(t, 0.0, GetToolPrice("custom_fn"))
}
