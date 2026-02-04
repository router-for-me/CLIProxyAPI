import type { ApiKeyEntry, GeminiKeyConfig, ProviderKeyConfig } from '@/types';
import type { HeaderEntry } from '@/utils/headers';
import type { KeyStats, UsageDetail } from '@/utils/usage';

export type ProviderModal =
  | { type: 'gemini'; index: number | null }
  | { type: 'codex'; index: number | null }
  | { type: 'claude'; index: number | null }
  | { type: 'vertex'; index: number | null }
  | { type: 'ampcode'; index: null }
  | { type: 'openai'; index: number | null };

export interface ModelEntry {
  name: string;
  alias: string;
}

export interface OpenAIFormState {
  name: string;
  prefix: string;
  baseUrl: string;
  headers: HeaderEntry[];
  testModel?: string;
  modelEntries: ModelEntry[];
  apiKeyEntries: ApiKeyEntry[];
}

export interface AmpcodeFormState {
  upstreamUrl: string;
  upstreamApiKey: string;
  forceModelMappings: boolean;
  mappingEntries: ModelEntry[];
}

export type GeminiFormState = Omit<GeminiKeyConfig, 'headers'> & {
  headers: HeaderEntry[];
  excludedText: string;
};

export type ProviderFormState = Omit<ProviderKeyConfig, 'headers'> & {
  headers: HeaderEntry[];
  modelEntries: ModelEntry[];
  excludedText: string;
};

export type VertexFormState = Omit<ProviderKeyConfig, 'headers' | 'excludedModels'> & {
  headers: HeaderEntry[];
  modelEntries: ModelEntry[];
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

export interface ProviderModalProps<TConfig, TPayload = TConfig> {
  isOpen: boolean;
  editIndex: number | null;
  initialData?: TConfig;
  onClose: () => void;
  onSave: (data: TPayload, index: number | null) => Promise<void>;
  disabled?: boolean;
}
