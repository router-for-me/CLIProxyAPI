package config

import "testing"

func TestInferCompatKindFromBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    string
	}{
		{
			name:    "minimax cn anthropic",
			baseURL: "https://api.minimaxi.com/anthropic",
			want:    "minimax",
		},
		{
			name:    "minimax global anthropic",
			baseURL: "https://api.minimaxi.io/anthropic",
			want:    "minimax",
		},
		{
			name:    "minimax global alternate anthropic",
			baseURL: "https://api.minimax.io/anthropic/v1/messages",
			want:    "minimax",
		},
		{
			name:    "minimax trailing slash",
			baseURL: "https://api.minimaxi.com/anthropic/",
			want:    "minimax",
		},
		{
			name:    "other provider",
			baseURL: "https://api.anthropic.com",
			want:    "",
		},
		{
			name:    "deepseek anthropic",
			baseURL: "https://api.deepseek.com/anthropic",
			want:    "deepseek",
		},
		{
			name:    "deepseek anthropic messages",
			baseURL: "https://api.deepseek.com/anthropic/v1/messages?beta=true",
			want:    "deepseek",
		},
		{
			name:    "kimi coding",
			baseURL: "https://api.kimi.com/coding",
			want:    "kimi",
		},
		{
			name:    "zhipu anthropic",
			baseURL: "https://open.bigmodel.cn/api/anthropic",
			want:    "zhipu",
		},
		{
			name:    "zhipu coding plan openai",
			baseURL: "https://open.bigmodel.cn/api/coding/paas/v4",
			want:    "zhipu",
		},
		{
			name:    "zai coding openai",
			baseURL: "https://api.z.ai/api/paas/v4",
			want:    "zhipu",
		},
		{
			name:    "lanyun anthropic",
			baseURL: "https://maas-api.lanyun.net/anthropic",
			want:    "zhipu",
		},
		{
			name:    "xfyun anthropic",
			baseURL: "https://maas-coding-api.cn-huabei-1.xf-yun.com/anthropic",
			want:    "xfyun",
		},
		{
			name:    "xiaomi anthropic",
			baseURL: "https://token-plan-cn.xiaomimimo.com/anthropic",
			want:    "xiaomi",
		},
		{
			name:    "xiaomi token plan openai",
			baseURL: "https://token-plan-cn.xiaomimimo.com/v1",
			want:    "xiaomi",
		},
		{
			name:    "xiaomi token plan singapore openai",
			baseURL: "https://token-plan-sgp.xiaomimimo.com/v1",
			want:    "xiaomi",
		},
		{
			name:    "xiaomi token plan europe anthropic",
			baseURL: "https://token-plan-ams.xiaomimimo.com/anthropic",
			want:    "xiaomi",
		},
		{
			name:    "xiaomi api openai",
			baseURL: "https://api.xiaomimimo.com/v1",
			want:    "xiaomi",
		},
		{
			name:    "xiaomi api anthropic",
			baseURL: "https://api.xiaomimimo.com/anthropic",
			want:    "xiaomi",
		},
		{
			name:    "qwen anthropic app",
			baseURL: "https://coding.dashscope.aliyuncs.com/apps/anthropic",
			want:    "qwen",
		},
		{
			name:    "doubao coding",
			baseURL: "https://ark.cn-beijing.volces.com/api/coding",
			want:    "doubao",
		},
		{
			name:    "qianfan anthropic coding",
			baseURL: "https://qianfan.baidubce.com/anthropic/coding",
			want:    "qianfan",
		},
		{
			name:    "qianfan openai coding",
			baseURL: "https://qianfan.baidubce.com/v2/coding",
			want:    "qianfan",
		},
		{
			name:    "step plan",
			baseURL: "https://api.stepfun.com/step_plan",
			want:    "step",
		},
		{
			name:    "minimax non anthropic path",
			baseURL: "https://api.minimaxi.com/v1",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := InferCompatKindFromBaseURL(tt.baseURL); got != tt.want {
				t.Fatalf("InferCompatKindFromBaseURL(%q) = %q, want %q", tt.baseURL, got, tt.want)
			}
		})
	}
}
