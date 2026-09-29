package common

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCountBillableToolCallWebSearchPrefersDeclaredWebSearch(t *testing.T) {
	info := &RelayInfo{
		OriginModelName: "gpt-5.1",
		ResponsesUsageInfo: &ResponsesUsageInfo{
			BuiltInTools: map[string]*BuildInToolInfo{
				dto.BuildInToolWebSearch: {ToolName: dto.BuildInToolWebSearch, CallCount: 0},
			},
		},
	}

	info.CountBillableToolCall(dto.BuildInCallWebSearchCall, "")
	require.Contains(t, info.ResponsesUsageInfo.BuiltInTools, dto.BuildInToolWebSearch)
	assert.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolWebSearch].CallCount)
	assert.NotContains(t, info.ResponsesUsageInfo.BuiltInTools, dto.BuildInToolWebSearchPreview)
}

func TestCountBillableToolCallWebSearchDefaultsToPreview(t *testing.T) {
	info := &RelayInfo{OriginModelName: "gpt-5.1"}

	info.CountBillableToolCall(dto.BuildInCallWebSearchCall, "")
	require.NotNil(t, info.ResponsesUsageInfo)
	require.Contains(t, info.ResponsesUsageInfo.BuiltInTools, dto.BuildInToolWebSearchPreview)
	assert.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolWebSearchPreview].CallCount)
}

func TestCountBillableToolCallFunctionCallRequiresPrice(t *testing.T) {
	operation_setting.SetToolPriceForTest("my_priced_fn", 5.0)
	t.Cleanup(func() {
		operation_setting.DeleteToolPriceForTest("my_priced_fn")
	})

	info := &RelayInfo{OriginModelName: "gpt-5.1"}
	info.CountBillableToolCall(dto.BuildInCallFunctionCall, "my_priced_fn")
	require.Contains(t, info.ResponsesUsageInfo.BuiltInTools, "my_priced_fn")
	assert.Equal(t, 1, info.ResponsesUsageInfo.BuiltInTools["my_priced_fn"].CallCount)

	info.CountBillableToolCall(dto.BuildInCallFunctionCall, "unpriced_fn")
	assert.NotContains(t, info.ResponsesUsageInfo.BuiltInTools, "unpriced_fn")
}

func TestCountBillableToolCallFunctionCallSkipsReservedNames(t *testing.T) {
	info := &RelayInfo{OriginModelName: "gpt-5.1"}

	info.CountBillableToolCall(dto.BuildInCallFunctionCall, dto.BuildInToolWebSearchPreview)
	info.CountBillableToolCall(dto.BuildInCallFunctionCall, dto.BuildInToolFileSearch)
	info.CountBillableToolCall(dto.BuildInCallFunctionCall, dto.BuildInToolGoogleSearch)
	info.CountBillableToolCall(dto.BuildInCallFunctionCall, dto.BuildInToolImageGeneration)

	if info.ResponsesUsageInfo != nil {
		assert.NotContains(t, info.ResponsesUsageInfo.BuiltInTools, dto.BuildInToolWebSearchPreview)
		assert.NotContains(t, info.ResponsesUsageInfo.BuiltInTools, dto.BuildInToolFileSearch)
		assert.NotContains(t, info.ResponsesUsageInfo.BuiltInTools, dto.BuildInToolGoogleSearch)
		assert.NotContains(t, info.ResponsesUsageInfo.BuiltInTools, dto.BuildInToolImageGeneration)
	}
}

func TestImageGenerationCallCounterCompletedOutputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		observe   func(c *ImageGenerationCallCounter)
		wantCount int
	}{
		{
			name: "one final result",
			observe: func(c *ImageGenerationCallCounter) {
				idx := 0
				c.Observe(&dto.ResponsesOutput{
					Type:   dto.ResponsesOutputTypeImageGenerationCall,
					ID:     "img_1",
					Status: "completed",
					Result: "base64-a",
				}, &idx)
			},
			wantCount: 1,
		},
		{
			name: "two distinct finals",
			observe: func(c *ImageGenerationCallCounter) {
				idx0, idx1 := 0, 1
				c.Observe(&dto.ResponsesOutput{
					Type:   dto.ResponsesOutputTypeImageGenerationCall,
					ID:     "img_1",
					Result: "base64-a",
				}, &idx0)
				c.Observe(&dto.ResponsesOutput{
					Type:   dto.ResponsesOutputTypeImageGenerationCall,
					ID:     "img_2",
					Result: "base64-b",
				}, &idx1)
			},
			wantCount: 2,
		},
		{
			name: "empty result",
			observe: func(c *ImageGenerationCallCounter) {
				idx := 0
				c.Observe(&dto.ResponsesOutput{
					Type:   dto.ResponsesOutputTypeImageGenerationCall,
					ID:     "img_1",
					Result: "   ",
				}, &idx)
			},
			wantCount: 0,
		},
		{
			name: "failed status",
			observe: func(c *ImageGenerationCallCounter) {
				idx := 0
				c.Observe(&dto.ResponsesOutput{
					Type:   dto.ResponsesOutputTypeImageGenerationCall,
					ID:     "img_1",
					Status: "failed",
					Result: "base64-a",
				}, &idx)
			},
			wantCount: 0,
		},
		{
			name: "incomplete status",
			observe: func(c *ImageGenerationCallCounter) {
				idx := 0
				c.Observe(&dto.ResponsesOutput{
					Type:   dto.ResponsesOutputTypeImageGenerationCall,
					ID:     "img_1",
					Status: "incomplete",
					Result: "base64-a",
				}, &idx)
			},
			wantCount: 0,
		},
		{
			name: "cancelled status",
			observe: func(c *ImageGenerationCallCounter) {
				idx := 0
				c.Observe(&dto.ResponsesOutput{
					Type:   dto.ResponsesOutputTypeImageGenerationCall,
					ID:     "img_1",
					Status: "cancelled",
					Result: "base64-a",
				}, &idx)
			},
			wantCount: 0,
		},
		{
			name: "canceled status",
			observe: func(c *ImageGenerationCallCounter) {
				idx := 0
				c.Observe(&dto.ResponsesOutput{
					Type:   dto.ResponsesOutputTypeImageGenerationCall,
					ID:     "img_1",
					Status: "canceled",
					Result: "base64-a",
				}, &idx)
			},
			wantCount: 0,
		},
		{
			name: "partial status",
			observe: func(c *ImageGenerationCallCounter) {
				idx := 0
				c.Observe(&dto.ResponsesOutput{
					Type:   dto.ResponsesOutputTypeImageGenerationCall,
					ID:     "img_1",
					Status: "partial",
					Result: "partial-bytes",
				}, &idx)
			},
			wantCount: 0,
		},
		{
			name: "id dedup",
			observe: func(c *ImageGenerationCallCounter) {
				idx0, idx1 := 0, 1
				c.Observe(&dto.ResponsesOutput{
					Type:   dto.ResponsesOutputTypeImageGenerationCall,
					ID:     "img_1",
					CallId: "call_a",
					Result: "base64-a",
				}, &idx0)
				c.Observe(&dto.ResponsesOutput{
					Type:   dto.ResponsesOutputTypeImageGenerationCall,
					ID:     "img_1",
					CallId: "call_b",
					Result: "base64-b",
				}, &idx1)
			},
			wantCount: 1,
		},
		{
			name: "index dedup",
			observe: func(c *ImageGenerationCallCounter) {
				idx := 0
				c.Observe(&dto.ResponsesOutput{
					Type:   dto.ResponsesOutputTypeImageGenerationCall,
					ID:     "img_1",
					Result: "base64-a",
				}, &idx)
				c.Observe(&dto.ResponsesOutput{
					Type:   dto.ResponsesOutputTypeImageGenerationCall,
					ID:     "img_2",
					Result: "base64-b",
				}, &idx)
			},
			wantCount: 1,
		},
		{
			name: "result hash dedup",
			observe: func(c *ImageGenerationCallCounter) {
				idx0, idx1 := 0, 1
				c.Observe(&dto.ResponsesOutput{
					Type:   dto.ResponsesOutputTypeImageGenerationCall,
					Result: "same-bytes",
				}, &idx0)
				c.Observe(&dto.ResponsesOutput{
					Type:   dto.ResponsesOutputTypeImageGenerationCall,
					Result: "same-bytes",
				}, &idx1)
			},
			wantCount: 1,
		},
		{
			name: "output_item.done plus completed dedup",
			observe: func(c *ImageGenerationCallCounter) {
				idx := 0
				item := &dto.ResponsesOutput{
					Type:   dto.ResponsesOutputTypeImageGenerationCall,
					ID:     "img_1",
					CallId: "call_1",
					Status: "completed",
					Result: "base64-a",
				}
				c.Observe(item, &idx)
				c.Observe(item, &idx)
			},
			wantCount: 1,
		},
		{
			name: "output_item.done plus incomplete equals zero",
			observe: func(c *ImageGenerationCallCounter) {
				idx := 0
				c.Observe(&dto.ResponsesOutput{
					Type:   dto.ResponsesOutputTypeImageGenerationCall,
					ID:     "img_1",
					Status: "completed",
					Result: "base64-a",
				}, &idx)
				c.Reset()
			},
			wantCount: 0,
		},
		{
			name: "partial event equals zero",
			observe: func(c *ImageGenerationCallCounter) {
				idx := 0
				c.Observe(&dto.ResponsesOutput{
					Type:   "image_generation_call.partial_image",
					ID:     "img_1",
					Result: "partial-bytes",
				}, &idx)
			},
			wantCount: 0,
		},
		{
			name: "in_progress with final result counts",
			observe: func(c *ImageGenerationCallCounter) {
				idx := 0
				c.Observe(&dto.ResponsesOutput{
					Type:   dto.ResponsesOutputTypeImageGenerationCall,
					ID:     "img_1",
					Status: "in_progress",
					Result: "base64-a",
				}, &idx)
			},
			wantCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			counter := &ImageGenerationCallCounter{}
			tt.observe(counter)
			assert.Equal(t, tt.wantCount, counter.Count())
		})
	}
}

func TestImageGenerationCallCounterCommitCapsAtMaxImageN(t *testing.T) {
	t.Parallel()

	counter := &ImageGenerationCallCounter{}
	for i := range dto.MaxImageN + 3 {
		idx := i
		counter.Observe(&dto.ResponsesOutput{
			Type:   dto.ResponsesOutputTypeImageGenerationCall,
			ID:     "img_" + strings.Repeat("a", i+1),
			Result: "result-" + strings.Repeat("b", i+1),
		}, &idx)
	}
	require.Equal(t, dto.MaxImageN+3, counter.Count())

	info := &RelayInfo{}
	counter.Commit(info)
	require.Contains(t, info.ResponsesUsageInfo.BuiltInTools, dto.BuildInToolImageGeneration)
	assert.Equal(t, dto.MaxImageN, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolImageGeneration].CallCount)
}

func TestImageGenerationCallCounterCommitDoesNotBillDeclarationsAlone(t *testing.T) {
	t.Parallel()

	info := &RelayInfo{
		ResponsesUsageInfo: &ResponsesUsageInfo{
			BuiltInTools: map[string]*BuildInToolInfo{
				dto.BuildInToolImageGeneration: {
					ToolName:  dto.BuildInToolImageGeneration,
					CallCount: 0,
				},
			},
		},
	}
	(&ImageGenerationCallCounter{}).Commit(info)
	assert.Equal(t, 0, info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolImageGeneration].CallCount)
}

func TestIsNonBillableResponsesStatus(t *testing.T) {
	t.Parallel()

	assert.True(t, IsNonBillableResponsesStatus([]byte(`"failed"`)))
	assert.True(t, IsNonBillableResponsesStatus([]byte(`"incomplete"`)))
	assert.True(t, IsNonBillableResponsesStatus([]byte(`"cancelled"`)))
	assert.True(t, IsNonBillableResponsesStatus([]byte(`"canceled"`)))
	assert.False(t, IsNonBillableResponsesStatus([]byte(`"completed"`)))
	assert.False(t, IsNonBillableResponsesStatus(nil))
}
