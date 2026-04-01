import type { ApiKeyEntry, GeminiKeyConfig, ProviderKeyConfig } from '@/types';
import type { HeaderEntry } from '@/utils/headers';
import type { KeyStats, UsageDetail } from '@/utils/usage';

export interface ModelEntry {
  name: string;
  alias: string;
}

export interface OpenAIFormState {
  name: string;
  priority?: number;
  prefix: string;
  baseUrl: string;
  headers: HeaderEntry[];
  testModel?: string;
  modelEntries: ModelEntry[];
  apiKeyEntries: ApiKeyEntry[];
}

export interface AmpcodeUpstreamApiKeyEntry {
  upstreamApiKey: string;
  clientApiKeysText: string;
}

export interface AmpcodeFormState {
  upstreamUrl: string;
  upstreamApiKey: string;
  forceModelMappings: boolean;
  mappingEntries: ModelEntry[];
  upstreamApiKeyEntries: AmpcodeUpstreamApiKeyEntry[];
}

export type GeminiFormState = Omit<GeminiKeyConfig, 'headers' | 'models'> & {
  headers: HeaderEntry[];
  modelEntries: ModelEntry[];
  excludedText: string;
};

export type ProviderFormState = Omit<ProviderKeyConfig, 'headers'> & {
  headers: HeaderEntry[];
  modelEntries: ModelEntry[];
  excludedText: string;
};

export type VertexFormState = Omit<ProviderKeyConfig, 'headers'> & {
  headers: HeaderEntry[];
  modelEntries: ModelEntry[];
  excludedText: string;
};

export interface ProviderSectionProps<TConfig> {
  configs: TConfig[];
  keyStats: KeyStats;
  usageDetails: UsageDetail[];
  disabled: boolean;
  onEdit: (index: number) => void;
  onAdd: () => void;
  onDelete: (index: number) => void;
  onToggle?: (index: number, enabled: boolean) => void;
}
