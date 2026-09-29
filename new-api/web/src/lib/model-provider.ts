/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

type ModelProvider = {
  readonly name: string
  readonly icon: string
  readonly label?: string
}

type ModelProviderRule = ModelProvider & {
  keywords?: readonly string[]
  pattern?: RegExp
  fallbackKeywords?: readonly string[]
  fallbackPattern?: RegExp
}

// Match specific providers before overlapping model families: 360gpt before
// GPT, and Perplexity's Sonar / NVIDIA's Nemotron before Llama or Mixtral.
const MODEL_PROVIDER_RULES: readonly ModelProviderRule[] = [
  { name: '360 AI', icon: 'Ai360.Color', keywords: ['360gpt', '360zhinao'] },
  {
    name: 'Perplexity',
    icon: 'Perplexity.Color',
    keywords: ['perplexity', 'sonar-'],
  },
  {
    name: 'NVIDIA',
    icon: 'Nvidia.Color',
    keywords: ['nvidia/', 'nvidia.', 'nemotron'],
  },
  {
    name: 'OpenAI',
    icon: 'OpenAI.Color',
    fallbackKeywords: [
      'text-embedding-',
      'omni-moderation',
      'dall-e',
      'whisper',
      'tts-',
    ],
    fallbackPattern: /\bo[134](?:-|$)/,
    keywords: [
      'openai/',
      'openai.',
      'gpt-',
      'chatgpt-',
      'codex-',
      'dall-e-',
      'whisper-',
      'omni-moderation-',
      'text-moderation-',
      'text-embedding-ada-',
      'text-embedding-3-',
      'text-ada-',
      'text-babbage-',
      'text-curie-',
      'davinci-',
      'babbage-',
      'computer-use-preview',
      'sora',
    ],
    pattern: /(?:^|[/.:])(?:o(?:1|3|4)(?=$|[-.:])|tts-)/,
  },
  {
    name: 'Anthropic',
    icon: 'Claude.Color',
    label: 'Claude',
    keywords: ['anthropic', 'claude'],
  },
  {
    name: 'Gemini',
    icon: 'Gemini.Color',
    keywords: [
      'gemini',
      'gemma',
      'learnlm',
      'imagen',
      'veo',
      'nano-banana',
      'palm-',
    ],
    pattern: /(?:^|[/.:])aqa$/,
  },
  {
    name: 'xAI',
    icon: 'Grok.Color',
    label: 'Grok',
    keywords: ['x-ai/', 'xai/', 'xai-', 'grok'],
  },
  { name: 'DeepSeek', icon: 'DeepSeek.Color', keywords: ['deepseek'] },
  {
    name: 'Qwen',
    icon: 'Qwen.Color',
    keywords: ['qwen', 'qwq-', 'qvq-', 'tongyi', 'gte-'],
    pattern: /(?:^|[/.:])(?:text-embedding-v\d+|gui-plus|z-image)(?:$|[-_.:])/,
  },
  { name: 'Wan', icon: 'Wan', pattern: /(?:^|[/.:])wan(?:x?\d|[-_])/ },
  { name: 'Moonshot', icon: 'Moonshot.Color', keywords: ['moonshot', 'kimi-'] },
  {
    name: 'MiniMax',
    icon: 'Minimax.Color',
    keywords: ['minimax', 'abab', 'hailuo'],
    pattern: /^(?:t2v|i2v|s2v)-01(?:-|$)/,
  },
  {
    name: 'Doubao',
    icon: 'Doubao.Color',
    keywords: ['doubao', 'volcengine', 'seedance', 'seedream', 'seed-1-'],
  },
  {
    name: 'Zhipu',
    icon: 'Zhipu.Color',
    fallbackKeywords: ['glm-'],
    keywords: ['zhipu', 'zai-org', 'thudm', 'chatglm', 'cogview', 'cogvideo'],
    pattern: /(?:^|[/._-])glm(?=$|[-._])/,
  },
  {
    name: 'Baidu',
    icon: 'Wenxin.Color',
    keywords: ['baidu', 'wenxin', 'ernie'],
  },
  {
    name: 'Yi',
    icon: 'Yi.Color',
    fallbackKeywords: ['yi-'],
    keywords: ['01-ai/'],
    pattern: /(?:^|[/.:])yi(?=$|[-_])/,
  },
  {
    name: 'iFlytek',
    icon: 'Spark.Color',
    label: 'iFlyTek',
    fallbackKeywords: ['spark'],
    keywords: ['iflytek', 'sparkdesk'],
  },
  {
    name: 'Tencent',
    icon: 'Hunyuan.Color',
    keywords: ['tencent', 'hunyuan'],
    pattern: /(?:^|[/.:])hy\d*(?=$|[-_.:])/,
  },
  { name: 'Baichuan', icon: 'Baichuan.Color', keywords: ['baichuan'] },
  { name: 'InternLM', icon: 'InternLM.Color', keywords: ['internlm'] },
  { name: 'StepFun', icon: 'Stepfun.Color', keywords: ['stepfun', 'step-'] },
  { name: 'MiMo', icon: 'XiaomiMiMo', keywords: ['xiaomi', 'mimo-'] },
  {
    name: 'Mistral',
    icon: 'Mistral.Color',
    keywords: [
      'mistral',
      'mixtral',
      'codestral',
      'ministral',
      'pixtral',
      'magistral',
    ],
  },
  {
    name: 'Meta',
    icon: 'Meta.Color',
    fallbackKeywords: ['meta-'],
    keywords: ['meta-llama', 'llama-', 'llama2', 'llama3'],
  },
  {
    name: 'Cohere',
    icon: 'Cohere.Color',
    keywords: ['cohere', 'command-', 'c4ai-aya', 'aya-'],
    pattern: /(?:^|[/.:])command$/,
  },
  { name: 'Jina', icon: 'Jina', keywords: ['jinaai', 'jina-'] },
  { name: 'BAAI', icon: 'BAAI', keywords: ['baai/', 'bge-'] },
  {
    name: 'Black Forest Labs',
    icon: 'Bfl',
    keywords: ['black-forest-labs', 'flux.'],
  },
  {
    name: 'Microsoft',
    icon: 'Microsoft.Color',
    keywords: ['microsoft/'],
    pattern: /(?:^|[/.:])phi(?=$|[-._])/,
  },
  {
    name: 'Amazon',
    icon: 'Aws.Color',
    keywords: ['amazon/', 'amazon.', 'nova-', 'titan-'],
  },
  { name: 'AI21 Labs', icon: 'Ai21', keywords: ['ai21', 'jamba'] },
  {
    name: 'Stability AI',
    icon: 'Stability.Color',
    keywords: ['stabilityai', 'stable-diffusion', 'stable-image', 'sdxl-'],
  },
  {
    name: 'Nous Research',
    icon: 'NousResearch',
    keywords: ['nousresearch', 'hermes-'],
  },
  {
    name: 'Midjourney',
    icon: 'Midjourney',
    keywords: ['midjourney', 'mj_', 'mj-', 'swap_face'],
  },
  { name: 'Kling', icon: 'Kling.Color', keywords: ['kling'] },
  { name: 'Vidu', icon: 'Vidu.Color', keywords: ['vidu'] },
  { name: 'Suno', icon: 'Suno', keywords: ['suno'] },
  { name: 'Jimeng', icon: 'Jimeng.Color', keywords: ['jimeng'] },
]

export function resolveModelProvider(modelName: string): ModelProvider | null {
  const model = modelName.trim().toLowerCase()

  // The broader log aliases are fallbacks so text-embedding-v3 stays Qwen
  // and step-tts-mini stays StepFun instead of matching OpenAI first.
  return (
    MODEL_PROVIDER_RULES.find(
      (rule) =>
        rule.keywords?.some((keyword) => model.includes(keyword)) ||
        rule.pattern?.test(model)
    ) ??
    MODEL_PROVIDER_RULES.find(
      (rule) =>
        rule.fallbackKeywords?.some((keyword) => model.includes(keyword)) ||
        rule.fallbackPattern?.test(model)
    ) ??
    null
  )
}
