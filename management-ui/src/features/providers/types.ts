/**
 * AI 提供商 Workbench 视图模型(归一化各 brand 的异构 config)
 */

import type { GeminiKeyConfig, OpenAIProviderConfig, ProviderKeyConfig } from '@/types';
import type { ThinkingLevel } from './thinkingLevels';
import type { RuntimePolicyDraft } from './runtimePolicy';
import type { ModelOptionsInput } from './modelOptions';
import type { ProviderBehaviorOptions } from '@/types/provider';

export type ProviderBrand =
  | 'gemini'
  | 'interactions'
  | 'codex'
  | 'meta'
  | 'xai'
  | 'claude'
  | 'vertex'
  | 'openaiCompatibility'
  | 'apikeyFun'
  | 'fennoAI'
  | 'qiniuCloud'
  | 'kimi';

export type SponsorProviderBrand = 'apikeyFun' | 'fennoAI' | 'qiniuCloud' | 'kimi';

export const PROVIDER_SORT_BY_VALUES = ['name', 'priority', 'recent-success'] as const;
export type ProviderSortBy = (typeof PROVIDER_SORT_BY_VALUES)[number];

export const SORT_DIR_VALUES = ['asc', 'desc'] as const;
export type SortDir = (typeof SORT_DIR_VALUES)[number];

export type ProviderResourceSelector =
  | { brand: 'gemini'; apiKey: string; baseUrl?: string; index: number }
  | { brand: 'interactions'; apiKey: string; baseUrl?: string; index: number }
  | { brand: 'codex'; apiKey: string; baseUrl?: string; index: number }
  | { brand: 'meta'; apiKey: string; baseUrl?: string; index: number }
  | { brand: 'xai'; apiKey: string; baseUrl?: string; index: number }
  | { brand: 'claude'; apiKey: string; baseUrl?: string; index: number }
  | { brand: 'vertex'; apiKey: string; baseUrl?: string; index: number }
  | { brand: 'openaiCompatibility'; name: string; index: number }
  | {
      brand: 'apikeyFun';
      openaiIndices: number[];
      claudeIndices: number[];
      codexIndices: number[];
      geminiIndices: number[];
    }
  | {
      brand: 'fennoAI';
      openaiIndices: number[];
      claudeIndices: number[];
      codexIndices: number[];
      geminiIndices: number[];
    }
  | {
      brand: 'qiniuCloud';
      openaiIndices: number[];
      claudeIndices: number[];
      codexIndices: number[];
      geminiIndices: number[];
    }
  | {
      brand: 'kimi';
      openaiIndices: number[];
      claudeIndices: number[];
      codexIndices: number[];
      geminiIndices: number[];
    };

export interface ProviderResourceFlags {
  cloakEnabled?: boolean;
  claudeCodeCliProfile?: boolean;
  websockets?: boolean;
  protocols?: string[];
}

export interface ProviderResource {
  /** 稳定 id,用作 React key 与选中态判断 */
  id: string;
  brand: ProviderBrand;
  /** 在原数组中的下标 */
  originalIndex: number;
  /** 表格 key 列显示名(OpenAI=name,其余=null) */
  name: string | null;
  /** 备用展示文字(API 密钥脱敏或 fallback) */
  identifier: string;
  /** apiKey 脱敏预览,展示用 */
  apiKeyPreview: string | null;
  /** 用于 selector 的真实 apiKey;OpenAI 因为多密钥这里返回 null */
  apiKey: string | null;
  authIndex: string | null;
  baseUrl: string | null;
  proxyUrl: string | null;
  prefix: string | null;
  modelCount: number;
  /** 去重后的模型名, 供筛选/搜索用 */
  models: string[];
  /** 排序用优先级,未配置时为 0 */
  priority: number;
  headerCount: number;
  excludedModelCount: number;
  /** 仅 OpenAI 有意义,其它 brand 该字段不展示但保留 */
  apiKeyEntryCount: number;
  /** 是否被禁用(各 brand 判定规则不同) */
  disabled: boolean;
  /** 额外能力旗标 */
  flags: ProviderResourceFlags;
  /** 删除/更新使用的 selector */
  selector: ProviderResourceSelector;
  /** 原始 raw config,Sheet 表单初始化用 */
  raw: unknown;
}

export interface ProviderGroup {
  id: ProviderBrand;
  resources: ProviderResource[];
}

export interface ProviderSnapshot {
  fetchedAt: string;
  groups: ProviderGroup[];
}

export interface SponsorProviderRaw {
  openai: Array<{ config: OpenAIProviderConfig; index: number }>;
  claude: Array<{ config: ProviderKeyConfig; index: number }>;
  codex: Array<{ config: ProviderKeyConfig; index: number }>;
  gemini: Array<{ config: GeminiKeyConfig; index: number }>;
}

/**
 * 通用 Sheet 表单值。
 * Gemini/Codex/Claude/Vertex/OpenAI 共用基础字段,各自启用 advanced 区。
 */
export interface ModelEntryInput extends ModelOptionsInput {
  /** Only used by the OAuth alias editor. */
  fork?: boolean;
  sourceIndex?: number | null;
  name: string;
  alias?: string;
  priority?: number;
  image?: boolean;
  /** Original backend value, preserved until the standard-level selector is changed. */
  thinkingJson?: string;
  thinkingLevels?: ThinkingLevel[];
  thinkingLevelsTouched?: boolean;
}

export type SponsorProtocol = 'openai' | 'codex' | 'claude' | 'gemini';

export interface SponsorKeyEntryInput extends ProviderBehaviorOptions {
  protocol: SponsorProtocol;
  apiKey: string;
  existingApiKey?: string;
  baseUrl: string;
  proxyUrl: string;
  prefix: string;
  disabled: boolean;
  disableCooling?: boolean;
  runtimePolicy?: RuntimePolicyDraft;
  priority?: number;
  weight?: number;
  models: ModelEntryInput[];
}

export interface ApiKeyEntryInput {
  sourceIndex?: number;
  apiKey: string;
  existingApiKey?: string;
  proxyUrl: string;
  weight?: number;
  authIndex?: string;
}

export interface CloakInput {
  mode: string;
  strictMode: boolean;
  sensitiveWordsText: string;
  cacheUserId: boolean;
}

export interface ProviderEntryFormInput extends ProviderBehaviorOptions {
  /** OpenAI 创建时只在 apiKeyEntries 中传 */
  apiKey: string;
  /** OpenAI 必填,其余 brand 不展示 */
  name: string;
  baseUrl: string;
  proxyUrl: string;
  prefix: string;
  disabled: boolean;
  disableCooling?: boolean;
  runtimePolicy?: RuntimePolicyDraft;
  priority?: number;
  weight?: number;

  /** 高级折叠区 */
  models: ModelEntryInput[];
  headers: Array<{ key: string; value: string }>;
  excludedModelsText: string;

  /** Codex 专属 */
  websockets?: boolean;
  /** Claude 专属 */
  cloak?: CloakInput;
  fingerprintProfile?: string;
  /** Temporary selection for connectivity tests only; never persisted in backend config. */
  testModel?: string;
  apiKeyEntries?: ApiKeyEntryInput[];
  /** APIKEY.FUN stores one grouped key per platform protocol. */
  sponsorKeyEntries?: SponsorKeyEntryInput[];
}
