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
import type { PricingModel } from '../types'
import {
  hashStringToSeed,
  randomInRange,
  randomIntInRange,
  seededRandom,
} from './seed'

// ----------------------------------------------------------------------------
// Mock model statistics
// ----------------------------------------------------------------------------
//
// The backend has not yet implemented latency / uptime / app-ranking data.
// These helpers generate plausible, deterministic mock values seeded from
// the model name (and optionally the group name) so that:
//   - Every render of the same model shows the same numbers
//   - Different models / different groups render visibly distinct values
//
// When the backend ships real metrics, callers should switch to the
// real API and these helpers can be deleted. The shape of the returned
// data is designed to mirror what we expect the real endpoints to return.

export type GroupPerformance = {
  group: string
  ttft_p50_ms: number
  ttft_p95_ms: number
  ttft_p99_ms: number
  throughput_tps: number
  uptime_30d_pct: number
  /** Number of monitored requests in the last 24h (display only). */
  request_volume_24h: number
}

export type LatencyTimePoint = {
  timestamp: string
  group: string
  ttft_ms: number
}

export type UptimeDayPoint = {
  date: string
  uptime_pct: number
  incidents: number
  outage_minutes: number
}

export type AppRanking = {
  rank: number
  name: string
  description: string
  category: string
  growth_pct: number
  monthly_tokens: number
  url?: string
  initial: string
}

const APP_TEMPLATES: Array<
  Omit<AppRanking, 'rank' | 'monthly_tokens' | 'growth_pct' | 'initial'>
> = [
  {
    name: 'Cline',
    description: 'Autonomous coding agent inside the IDE',
    category: 'Coding',
    url: 'https://cline.bot',
  },
  {
    name: 'Roo Code',
    description: 'AI agent for VS Code with multi-step planning',
    category: 'Coding',
    url: 'https://roocode.com',
  },
  {
    name: 'Open WebUI',
    description: 'Self-hosted ChatGPT-like web interface',
    category: 'Chat',
    url: 'https://openwebui.com',
  },
  {
    name: 'LibreChat',
    description: 'Open-source chat platform with multi-model support',
    category: 'Chat',
    url: 'https://librechat.ai',
  },
  {
    name: 'Lobe Chat',
    description: 'Modern open-source chat UI with plugins',
    category: 'Chat',
    url: 'https://lobehub.com',
  },
  {
    name: 'NextChat',
    description: 'Cross-platform private ChatGPT client',
    category: 'Chat',
    url: 'https://nextchat.dev',
  },
  {
    name: 'Continue',
    description: 'Open-source AI code assistant for editors',
    category: 'Coding',
    url: 'https://continue.dev',
  },
  {
    name: 'Aider',
    description: 'Pair-programming agent in your terminal',
    category: 'Coding',
    url: 'https://aider.chat',
  },
  {
    name: 'Dify',
    description: 'LLM application development platform',
    category: 'Platform',
    url: 'https://dify.ai',
  },
  {
    name: 'FastGPT',
    description: 'Knowledge base orchestration and chat platform',
    category: 'Platform',
    url: 'https://fastgpt.in',
  },
  {
    name: 'Flowise',
    description: 'Low-code LLM workflow builder',
    category: 'Platform',
    url: 'https://flowiseai.com',
  },
  {
    name: 'OpenInterpreter',
    description: 'Natural-language code execution agent',
    category: 'Coding',
    url: 'https://openinterpreter.com',
  },
  {
    name: 'Devika',
    description: 'Open-source AI software engineer',
    category: 'Coding',
    url: 'https://github.com/stitionai/devika',
  },
  {
    name: 'Cherry Studio',
    description: 'Multi-model desktop chat client',
    category: 'Chat',
    url: 'https://cherry-ai.com',
  },
  {
    name: 'AnythingLLM',
    description: 'Workspaces around your private documents',
    category: 'Platform',
    url: 'https://anythingllm.com',
  },
  {
    name: 'OpenHands',
    description: 'Coding agent with browser-and-code tools',
    category: 'Coding',
    url: 'https://docs.all-hands.dev',
  },
  {
    name: 'Cursor',
    description: 'AI-native code editor',
    category: 'Coding',
    url: 'https://cursor.com',
  },
  {
    name: 'Zed',
    description: 'Multiplayer code editor with AI',
    category: 'Coding',
    url: 'https://zed.dev',
  },
  {
    name: 'Notion AI',
    description: 'Documents and writing assistant',
    category: 'Productivity',
    url: 'https://notion.so',
  },
  {
    name: 'Raycast AI',
    description: 'AI on your macOS launcher',
    category: 'Productivity',
    url: 'https://raycast.com',
  },
  {
    name: 'Obsidian Smart Connections',
    description: 'Connect notes with semantic search',
    category: 'Productivity',
  },
  {
    name: 'Bolt.new',
    description: 'Prompt-to-app full-stack builder',
    category: 'Coding',
    url: 'https://bolt.new',
  },
  {
    name: 'Pieces',
    description: 'AI workflow companion for developers',
    category: 'Productivity',
    url: 'https://pieces.app',
  },
  {
    name: 'AmazingAI',
    description: 'Personal AI knowledge assistant',
    category: 'Productivity',
  },
  {
    name: 'TypingMind',
    description: 'Better UI for ChatGPT and Claude',
    category: 'Chat',
    url: 'https://typingmind.com',
  },
]

const PROFILE_BY_NAME = (name: string) => {
  const n = name.toLowerCase()
  if (/embed|rerank/.test(n)) return 'embedding'
  if (/image|sora|veo|kling|pika|jimeng|dalle|imagen/.test(n)) return 'image'
  if (/whisper|tts|voice|audio/.test(n)) return 'audio'
  if (/o1|o3|o4|reasoning|thinking|deepseek-r/.test(n)) return 'reasoning'
  if (/flash|haiku|mini|small|nano|fast/.test(n)) return 'fast'
  if (/gpt-5|opus|ultra|405|70b/.test(n)) return 'large'
  return 'standard'
}

type ProfileSpec = {
  ttftRange: [number, number]
  throughputRange: [number, number]
  uptimeRange: [number, number]
}

const PROFILE_SPECS: Record<string, ProfileSpec> = {
  embedding: {
    ttftRange: [40, 120],
    throughputRange: [0, 0],
    uptimeRange: [99.9, 99.99],
  },
  image: {
    ttftRange: [2_500, 12_000],
    throughputRange: [0, 0],
    uptimeRange: [98.5, 99.8],
  },
  audio: {
    ttftRange: [180, 600],
    throughputRange: [0, 0],
    uptimeRange: [99.5, 99.95],
  },
  reasoning: {
    ttftRange: [1_800, 5_500],
    throughputRange: [25, 70],
    uptimeRange: [99.4, 99.95],
  },
  fast: {
    ttftRange: [180, 480],
    throughputRange: [110, 240],
    uptimeRange: [99.7, 99.99],
  },
  large: {
    ttftRange: [600, 1_400],
    throughputRange: [55, 95],
    uptimeRange: [99.5, 99.95],
  },
  standard: {
    ttftRange: [400, 900],
    throughputRange: [70, 140],
    uptimeRange: [99.6, 99.97],
  },
}

function rangeFromSeed(
  rand: () => number,
  [min, max]: [number, number]
): number {
  return randomInRange(rand, min, max)
}

function applyGroupFactor(value: number, factor: number): number {
  return value * factor
}

function groupFactor(
  group: string,
  baseSeed: number
): { ttft: number; throughput: number; uptime: number } {
  const rand = seededRandom(baseSeed ^ hashStringToSeed(group || 'default'))
  return {
    ttft: 0.85 + rand() * 0.55,
    throughput: 0.85 + rand() * 0.4,
    uptime: 0.997 + rand() * 0.003,
  }
}

/**
 * Build per-group performance stats for a model. Always returns at least one
 * row for each enabled group, sorted alphabetically.
 */
export function buildGroupPerformance(model: PricingModel): GroupPerformance[] {
  const groups = (model.enable_groups ?? []).filter((g) => g && g !== 'auto')
  const targets = groups.length > 0 ? groups : ['default']
  const profile = PROFILE_BY_NAME(model.model_name)
  const spec = PROFILE_SPECS[profile]
  const baseSeed = hashStringToSeed(model.model_name)

  return targets
    .slice()
    .sort((a, b) => a.localeCompare(b))
    .map<GroupPerformance>((group) => {
      const rand = seededRandom(baseSeed ^ hashStringToSeed(group))
      const factor = groupFactor(group, baseSeed)
      const ttftP50 = applyGroupFactor(
        rangeFromSeed(rand, spec.ttftRange),
        factor.ttft
      )
      const throughput = applyGroupFactor(
        rangeFromSeed(rand, spec.throughputRange),
        factor.throughput
      )
      const uptimePct = Math.min(
        99.99,
        rangeFromSeed(rand, spec.uptimeRange) * factor.uptime
      )
      const requestVolume = randomIntInRange(rand, 18_000, 480_000)
      return {
        group,
        ttft_p50_ms: Math.round(ttftP50),
        ttft_p95_ms: Math.round(ttftP50 * (1.6 + rand() * 0.4)),
        ttft_p99_ms: Math.round(ttftP50 * (2.4 + rand() * 0.6)),
        throughput_tps: throughput === 0 ? 0 : Math.round(throughput * 10) / 10,
        uptime_30d_pct: Math.round(uptimePct * 100) / 100,
        request_volume_24h: requestVolume,
      }
    })
}

/**
 * Build a 24-hour latency series for each group. Returns one point per hour
 * (24 buckets), oldest first.
 */
export function buildLatencyTimeSeries(
  model: PricingModel
): LatencyTimePoint[] {
  const performances = buildGroupPerformance(model)
  if (performances.length === 0) return []

  const now = new Date()
  now.setMinutes(0, 0, 0)
  const baseSeed = hashStringToSeed(`${model.model_name}:lat`)
  const points: LatencyTimePoint[] = []

  for (const perf of performances) {
    const rand = seededRandom(baseSeed ^ hashStringToSeed(perf.group))
    for (let i = 23; i >= 0; i--) {
      const ts = new Date(now.getTime() - i * 3_600_000)
      const noise = 0.7 + rand() * 0.7
      const trend = 0.85 + Math.sin(i / 3) * 0.1
      const value = Math.max(50, Math.round(perf.ttft_p50_ms * noise * trend))
      points.push({
        timestamp: ts.toISOString(),
        group: perf.group,
        ttft_ms: value,
      })
    }
  }
  return points
}

/**
 * Build a 30-day uptime series. Returns one point per day, oldest first.
 *
 * If `group` is provided the series is anchored on that group's mean uptime,
 * otherwise it uses the per-model average. Either way the seed is derived
 * deterministically so re-renders are stable.
 */
export function buildUptimeSeries(
  model: PricingModel,
  group?: string
): UptimeDayPoint[] {
  const performances = buildGroupPerformance(model)
  if (performances.length === 0) return []

  const target = group ? performances.find((p) => p.group === group) : null
  const baseUptime = target
    ? target.uptime_30d_pct
    : performances.reduce((s, p) => s + p.uptime_30d_pct, 0) /
      performances.length
  const baseSeed = hashStringToSeed(`${model.model_name}:up:${group ?? '_all'}`)
  const rand = seededRandom(baseSeed)

  const today = new Date()
  today.setHours(0, 0, 0, 0)
  const points: UptimeDayPoint[] = []

  for (let i = 29; i >= 0; i--) {
    const date = new Date(today.getTime() - i * 86_400_000)
    const isoDate = date.toISOString().slice(0, 10)
    const incidentChance = rand()
    const incidents = incidentChance > 0.92 ? 1 : 0
    const outageMinutes = incidents > 0 ? Math.round(rand() * 30 + 5) : 0
    const downtimePct = (outageMinutes / 1_440) * 100
    const dayUptime = Math.max(85, Math.min(100, baseUptime - downtimePct))
    points.push({
      date: isoDate,
      uptime_pct: Math.round(dayUptime * 100) / 100,
      incidents,
      outage_minutes: outageMinutes,
    })
  }

  return points
}

/**
 * Build a deterministic top-apps ranking for the model. The first three apps
 * always come from the same template list; the rest is shuffled by the seed
 * so different models surface different long tails.
 */
export function buildAppRankings(
  model: PricingModel,
  count = 12
): AppRanking[] {
  const baseSeed = hashStringToSeed(`${model.model_name}:apps`)
  const rand = seededRandom(baseSeed)
  const candidates = [...APP_TEMPLATES]
  // Fisher–Yates shuffle.
  for (let i = candidates.length - 1; i > 0; i--) {
    const j = Math.floor(rand() * (i + 1))
    ;[candidates[i], candidates[j]] = [candidates[j], candidates[i]]
  }

  const top = candidates.slice(0, count)
  const baseTokens = randomInRange(rand, 90_000_000, 320_000_000)

  return top.map((app, idx) => {
    const decay = Math.pow(0.78, idx)
    const monthlyTokens = Math.round(baseTokens * decay * (0.85 + rand() * 0.3))
    const growthPctRaw = randomInRange(rand, -28, 84)
    const growthPct = Math.round(growthPctRaw * 10) / 10
    return {
      rank: idx + 1,
      name: app.name,
      description: app.description,
      category: app.category,
      url: app.url,
      growth_pct: growthPct,
      monthly_tokens: monthlyTokens,
      initial: app.name.charAt(0).toUpperCase(),
    }
  })
}

/** Aggregate uptime over the most recent 30 days. */
export function aggregateUptime(points: UptimeDayPoint[]): {
  uptime_pct: number
  incidents: number
  outage_minutes: number
} {
  if (points.length === 0) {
    return { uptime_pct: 0, incidents: 0, outage_minutes: 0 }
  }
  const incidents = points.reduce((s, p) => s + p.incidents, 0)
  const outageMinutes = points.reduce((s, p) => s + p.outage_minutes, 0)
  const totalMinutes = points.length * 1_440
  const uptimePct = ((totalMinutes - outageMinutes) / totalMinutes) * 100
  return {
    incidents,
    outage_minutes: outageMinutes,
    uptime_pct: Math.round(uptimePct * 1000) / 1000,
  }
}

/** Compact integer formatter for token counts in apps tab. */
export function formatTokenVolume(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0'
  if (n >= 1_000_000_000) return `${(n / 1_000_000_000).toFixed(1)}B`
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`
  return n.toString()
}

// ---------------------------------------------------------------------------
// Mock supported-parameters & rate-limits & misc API metadata
// ---------------------------------------------------------------------------

export type SupportedParameter = {
  name: string
  type:
    | 'number'
    | 'integer'
    | 'boolean'
    | 'string'
    | 'object'
    | 'array'
    | 'enum'
  defaultValue?: string | number | boolean
  range?: string
  enumValues?: string[]
  descriptionKey: string
  required?: boolean
}

const COMMON_CHAT_PARAMS: SupportedParameter[] = [
  {
    name: 'temperature',
    type: 'number',
    defaultValue: 1,
    range: '0 ~ 2',
    descriptionKey: 'Sampling temperature; lower is more deterministic',
  },
  {
    name: 'top_p',
    type: 'number',
    defaultValue: 1,
    range: '0 ~ 1',
    descriptionKey: 'Nucleus sampling probability mass',
  },
  {
    name: 'max_tokens',
    type: 'integer',
    range: '>= 1',
    descriptionKey: 'Maximum number of tokens in the response',
  },
  {
    name: 'frequency_penalty',
    type: 'number',
    defaultValue: 0,
    range: '-2 ~ 2',
    descriptionKey: 'Penalises repetition of frequent tokens',
  },
  {
    name: 'presence_penalty',
    type: 'number',
    defaultValue: 0,
    range: '-2 ~ 2',
    descriptionKey: 'Encourages introducing new topics',
  },
  {
    name: 'stop',
    type: 'array',
    descriptionKey: 'Up to 4 strings that stop generation',
  },
  {
    name: 'seed',
    type: 'integer',
    descriptionKey: 'Deterministic sampling seed (best-effort)',
  },
  {
    name: 'n',
    type: 'integer',
    defaultValue: 1,
    range: '>= 1',
    descriptionKey: 'Number of completions to generate',
  },
  {
    name: 'stream',
    type: 'boolean',
    defaultValue: false,
    descriptionKey: 'Stream tokens via Server-Sent Events',
  },
  {
    name: 'response_format',
    type: 'object',
    descriptionKey: 'Force JSON object or schema-conforming output',
  },
  {
    name: 'tools',
    type: 'array',
    descriptionKey: 'Tool / function declarations the model may call',
  },
  {
    name: 'tool_choice',
    type: 'string',
    enumValues: ['auto', 'none', 'required'],
    descriptionKey: 'Tool-choice policy or specific tool name',
  },
  {
    name: 'logprobs',
    type: 'boolean',
    defaultValue: false,
    descriptionKey: 'Return per-token log probabilities',
  },
  {
    name: 'top_logprobs',
    type: 'integer',
    range: '0 ~ 20',
    descriptionKey: 'Number of top log probabilities returned per token',
  },
  {
    name: 'logit_bias',
    type: 'object',
    descriptionKey: 'Per-token logit bias map',
  },
  {
    name: 'user',
    type: 'string',
    descriptionKey: 'End-user identifier for abuse monitoring',
  },
]

const REASONING_PARAMS: SupportedParameter[] = [
  {
    name: 'reasoning_effort',
    type: 'enum',
    enumValues: ['low', 'medium', 'high'],
    defaultValue: 'medium',
    descriptionKey: 'Controls how much the model thinks before answering',
  },
  {
    name: 'max_completion_tokens',
    type: 'integer',
    range: '>= 1',
    descriptionKey: 'Maximum tokens including hidden reasoning tokens',
  },
  {
    name: 'stop',
    type: 'array',
    descriptionKey: 'Up to 4 strings that stop generation',
  },
  {
    name: 'seed',
    type: 'integer',
    descriptionKey: 'Deterministic sampling seed (best-effort)',
  },
  {
    name: 'stream',
    type: 'boolean',
    defaultValue: false,
    descriptionKey: 'Stream tokens via Server-Sent Events',
  },
  {
    name: 'response_format',
    type: 'object',
    descriptionKey: 'Force JSON object or schema-conforming output',
  },
  {
    name: 'tools',
    type: 'array',
    descriptionKey: 'Tool / function declarations the model may call',
  },
  {
    name: 'tool_choice',
    type: 'string',
    enumValues: ['auto', 'none', 'required'],
    descriptionKey: 'Tool-choice policy or specific tool name',
  },
  {
    name: 'user',
    type: 'string',
    descriptionKey: 'End-user identifier for abuse monitoring',
  },
]

const EMBEDDING_PARAMS: SupportedParameter[] = [
  {
    name: 'input',
    type: 'string',
    required: true,
    descriptionKey: 'Text or array of texts to embed',
  },
  {
    name: 'dimensions',
    type: 'integer',
    range: '>= 1',
    descriptionKey: 'Truncate embeddings to this many dimensions',
  },
  {
    name: 'encoding_format',
    type: 'enum',
    enumValues: ['float', 'base64'],
    defaultValue: 'float',
    descriptionKey: 'Wire encoding for the embedding vectors',
  },
  {
    name: 'user',
    type: 'string',
    descriptionKey: 'End-user identifier for abuse monitoring',
  },
]

const IMAGE_PARAMS: SupportedParameter[] = [
  {
    name: 'prompt',
    type: 'string',
    required: true,
    descriptionKey: 'Text description of the desired image',
  },
  {
    name: 'size',
    type: 'enum',
    enumValues: ['256x256', '512x512', '1024x1024', '1024x1792', '1792x1024'],
    defaultValue: '1024x1024',
    descriptionKey: 'Output image size',
  },
  {
    name: 'quality',
    type: 'enum',
    enumValues: ['standard', 'hd'],
    defaultValue: 'standard',
    descriptionKey: 'Generation quality preset',
  },
  {
    name: 'style',
    type: 'enum',
    enumValues: ['vivid', 'natural'],
    defaultValue: 'vivid',
    descriptionKey: 'Aesthetic style',
  },
  {
    name: 'n',
    type: 'integer',
    defaultValue: 1,
    range: '1 ~ 10',
    descriptionKey: 'Number of images to generate',
  },
  {
    name: 'response_format',
    type: 'enum',
    enumValues: ['url', 'b64_json'],
    defaultValue: 'url',
    descriptionKey: 'How to deliver the resulting image',
  },
]

const VIDEO_PARAMS: SupportedParameter[] = [
  {
    name: 'prompt',
    type: 'string',
    required: true,
    descriptionKey: 'Text description of the desired video',
  },
  {
    name: 'duration',
    type: 'integer',
    range: '1 ~ 60',
    descriptionKey: 'Video length in seconds',
  },
  {
    name: 'aspect_ratio',
    type: 'enum',
    enumValues: ['16:9', '9:16', '1:1'],
    defaultValue: '16:9',
    descriptionKey: 'Output aspect ratio',
  },
  {
    name: 'fps',
    type: 'integer',
    range: '8 ~ 60',
    defaultValue: 24,
    descriptionKey: 'Frames per second',
  },
]

type ApiCategory = 'reasoning' | 'embedding' | 'image' | 'video' | 'chat'

/**
 * Refine the broad PROFILE_BY_NAME bucket into an API-shape category. The
 * `image` bucket from `PROFILE_BY_NAME` lumps still-image and video models
 * together (because their performance profiles overlap); for the API tab we
 * need to distinguish them so the request-parameter table is accurate.
 */
function apiCategoryOf(model: PricingModel): ApiCategory {
  const profile = PROFILE_BY_NAME(model.model_name)
  if (profile === 'embedding' || profile === 'reasoning') return profile
  if (profile === 'image') {
    return /sora|veo|kling|pika|video|wan-|hunyuanvideo/i.test(model.model_name)
      ? 'video'
      : 'image'
  }
  return 'chat'
}

/**
 * Build the list of request parameters that the model accepts. The list is
 * shaped per-modality so reasoning, embedding, image, video and chat models
 * each show their relevant parameter set.
 */
export function buildSupportedParameters(
  model: PricingModel
): SupportedParameter[] {
  const cat = apiCategoryOf(model)
  if (cat === 'reasoning') return REASONING_PARAMS
  if (cat === 'embedding') return EMBEDDING_PARAMS
  if (cat === 'image') return IMAGE_PARAMS
  if (cat === 'video') return VIDEO_PARAMS
  return COMMON_CHAT_PARAMS
}

export type RateLimit = {
  group: string
  rpm: number
  tpm: number
  rpd: number
}

/** Build per-group RPM / TPM / RPD limits for the model. */
export function buildRateLimits(model: PricingModel): RateLimit[] {
  const groups = (model.enable_groups ?? []).filter((g) => g && g !== 'auto')
  const targets = groups.length > 0 ? groups : ['default']
  const cat = apiCategoryOf(model)
  const baseSeed = hashStringToSeed(`${model.model_name}:rl`)
  const isHeavy = cat === 'image' || cat === 'video'
  const isLight = cat === 'embedding'
  const baseRpm = isHeavy ? 60 : isLight ? 5_000 : 500
  const baseTpm = isHeavy ? 0 : isLight ? 1_000_000 : 200_000
  const baseRpd = isHeavy ? 1_000 : isLight ? 100_000 : 10_000

  return targets
    .slice()
    .sort((a, b) => a.localeCompare(b))
    .map((group) => {
      const rand = seededRandom(baseSeed ^ hashStringToSeed(group))
      const tier = 0.6 + rand() * 1.4
      return {
        group,
        rpm: Math.round((baseRpm * tier) / 10) * 10,
        tpm: baseTpm === 0 ? 0 : Math.round((baseTpm * tier) / 1_000) * 1_000,
        rpd: Math.round((baseRpd * tier) / 100) * 100,
      }
    })
}

/** Format an integer rate-limit value compactly. */
export function formatRateLimit(value: number): string {
  if (value <= 0) return '—'
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
  if (value >= 1_000)
    return `${(value / 1_000).toFixed(value >= 10_000 ? 0 : 1)}K`
  return value.toLocaleString()
}
