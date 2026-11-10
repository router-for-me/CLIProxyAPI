/** Shared quota adapter view; provider data modules retain their concrete state types. */

import type { ComponentType } from 'react';
import type { TFunction } from 'i18next';
import { useQuotaStore } from '@/stores';
import type { AuthFileItem } from '@/types';
import type { QuotaBodyProps } from '../types';
import type { QuotaProviderType, QuotaStore } from './types';
import { ANTIGRAVITY_CONFIG } from './antigravity/data';
import { AntigravityQuotaBody } from './antigravity/AntigravityQuotaBody';
import { CLAUDE_CONFIG } from './claude/data';
import { ClaudeQuotaBody } from './claude/ClaudeQuotaBody';
import { DEVIN_CONFIG } from './devin/data';
import { DevinQuotaBody } from './devin/DevinQuotaBody';
import { CODEX_CONFIG } from './codex/data';
import { CodexQuotaBody } from './codex/CodexQuotaBody';
import { META_CONFIG } from './meta/data';
import { MetaQuotaBody } from './meta/MetaQuotaBody';
import { KIRO_CONFIG } from './kiro/data';
import { KiroQuotaBody } from './kiro/KiroQuotaBody';
import { QODER_CONFIG } from './qoder/data';
import { QoderQuotaBody } from './qoder/QoderQuotaBody';
import { KIMI_CONFIG } from './kimi/data';
import { KimiQuotaBody } from './kimi/KimiQuotaBody';
import { XAI_CONFIG } from './xai/data';
import { XaiQuotaBody } from './xai/XaiQuotaBody';
import { ZCODE_CONFIG } from './zcode/data';
import { ZCodeQuotaBody } from './zcode/ZCodeQuotaBody';

/** Common state shape shared by every provider. */
export interface QuotaCardState {
  status: 'idle' | 'loading' | 'success' | 'error';
  error?: string;
  errorStatus?: number;
}

export interface QuotaAdapter {
  type: QuotaProviderType;
  i18nPrefix: string;
  filterFn: (file: AuthFileItem) => boolean;
  fetchQuota: (file: AuthFileItem, t: TFunction) => Promise<unknown>;
  enrichQuota?: (file: AuthFileItem, data: unknown, t: TFunction) => Promise<unknown>;
  resetQuota?: (file: AuthFileItem, t: TFunction, option?: string) => Promise<unknown>;
  getResetOptions?: (quota: QuotaCardState, t: TFunction) => { value: string; label: string }[];
  canResetQuota?: (quota: QuotaCardState) => boolean;
  storeSelector: (state: QuotaStore) => Record<string, QuotaCardState>;
  storeSetter: keyof QuotaStore;
  buildLoadingState: () => QuotaCardState;
  buildSuccessState: (data: unknown) => QuotaCardState;
  buildErrorState: (message: string, status?: number) => QuotaCardState;
  Body: ComponentType<QuotaBodyProps<QuotaCardState>>;
}

export const QUOTA_ADAPTERS: Record<QuotaProviderType, QuotaAdapter> = {
  antigravity: {
    ...ANTIGRAVITY_CONFIG,
    Body: AntigravityQuotaBody,
  } as unknown as QuotaAdapter,
  claude: { ...CLAUDE_CONFIG, Body: ClaudeQuotaBody } as unknown as QuotaAdapter,
  codex: { ...CODEX_CONFIG, Body: CodexQuotaBody } as unknown as QuotaAdapter,
  devin: { ...DEVIN_CONFIG, Body: DevinQuotaBody } as unknown as QuotaAdapter,
  kiro: { ...KIRO_CONFIG, Body: KiroQuotaBody } as unknown as QuotaAdapter,
  kimi: { ...KIMI_CONFIG, Body: KimiQuotaBody } as unknown as QuotaAdapter,
  qoder: { ...QODER_CONFIG, Body: QoderQuotaBody } as unknown as QuotaAdapter,
  meta: { ...META_CONFIG, Body: MetaQuotaBody } as unknown as QuotaAdapter,
  xai: { ...XAI_CONFIG, Body: XaiQuotaBody } as unknown as QuotaAdapter,
  zcode: { ...ZCODE_CONFIG, Body: ZCodeQuotaBody } as unknown as QuotaAdapter,
};

export type QuotaMapUpdater = (
  updater: (prev: Record<string, QuotaCardState>) => Record<string, QuotaCardState>
) => void;

/** Direct store access without a React subscription. */
export const getQuotaSetter = (adapter: QuotaAdapter): QuotaMapUpdater =>
  useQuotaStore.getState()[adapter.storeSetter] as unknown as QuotaMapUpdater;

/** Direct cache access without a React subscription. */
export const getQuotaMap = (adapter: QuotaAdapter): Record<string, QuotaCardState> =>
  adapter.storeSelector(useQuotaStore.getState() as unknown as QuotaStore);
