/**
 * OpenAI provider editor draft state.
 *
 * Why this exists:
 * - The app uses `PageTransition` with iOS-style stacked routes for `/ai-providers/*`.
 * - Entering `/ai-providers/openai/.../models` creates a new route layer, so component-local state
 *   inside the OpenAI edit layout is not shared between the edit screen and the model picker screen.
 * - This store makes the OpenAI edit draft shared across route layers keyed by provider index/new.
 */

import type { SetStateAction } from 'react';
import { create } from 'zustand';
import type { OpenAIFormState } from '@/components/providers/types';
import { buildApiKeyEntry } from '@/components/providers/utils';

export type OpenAITestStatus = 'idle' | 'loading' | 'success' | 'error';

export type KeyTestStatus = {
  status: OpenAITestStatus;
  message: string;
};

export type OpenAIEditDraft = {
  initialized: boolean;
  baselineSignature: string;
  form: OpenAIFormState;
  testModel: string;
  testStatus: OpenAITestStatus;
  testMessage: string;
  keyTestStatuses: KeyTestStatus[];
};

interface OpenAIEditDraftState {
  drafts: Record<string, OpenAIEditDraft>;
  refCounts: Record<string, number>;
  acquireDraft: (key: string) => void;
  releaseDraft: (key: string) => void;
  ensureDraft: (key: string) => void;
  initDraft: (key: string, draft: Omit<OpenAIEditDraft, 'initialized'>) => void;
  setDraftBaselineSignature: (key: string, signature: string) => void;
  setDraftForm: (key: string, action: SetStateAction<OpenAIFormState>) => void;
  setDraftTestModel: (key: string, action: SetStateAction<string>) => void;
  setDraftTestStatus: (key: string, action: SetStateAction<OpenAITestStatus>) => void;
  setDraftTestMessage: (key: string, action: SetStateAction<string>) => void;
  setDraftKeyTestStatus: (draftKey: string, keyIndex: number, status: KeyTestStatus) => void;
  resetDraftKeyTestStatuses: (draftKey: string, count: number) => void;
  clearDraft: (key: string) => void;
}

const resolveAction = <T,>(action: SetStateAction<T>, prev: T): T =>
  typeof action === 'function' ? (action as (previous: T) => T)(prev) : action;

const buildEmptyForm = (): OpenAIFormState => ({
  name: '',
  prefix: '',
  baseUrl: '',
  headers: [],
  apiKeyEntries: [buildApiKeyEntry()],
  modelEntries: [{ name: '', alias: '' }],
  testModel: undefined,
});

const buildEmptyDraft = (): OpenAIEditDraft => ({
  initialized: false,
  baselineSignature: '',
  form: buildEmptyForm(),
  testModel: '',
  testStatus: 'idle',
  testMessage: '',
  keyTestStatuses: [],
});

export const useOpenAIEditDraftStore = create<OpenAIEditDraftState>((set, get) => ({
  drafts: {},
  refCounts: {},

  acquireDraft: (key) => {
    if (!key) return;
    set((state) => {
      const existingDraft = state.drafts[key];
      const currentCount = state.refCounts[key] ?? 0;
      return {
        drafts: existingDraft ? state.drafts : { ...state.drafts, [key]: buildEmptyDraft() },
        refCounts: { ...state.refCounts, [key]: currentCount + 1 },
      };
    });
  },

  releaseDraft: (key) => {
    if (!key) return;
    set((state) => {
      const currentCount = state.refCounts[key];
      if (!currentCount) return state;
      if (currentCount > 1) {
        return { refCounts: { ...state.refCounts, [key]: currentCount - 1 } };
      }
      const nextCounts = { ...state.refCounts };
      delete nextCounts[key];
      const nextDrafts = { ...state.drafts };
      delete nextDrafts[key];
      return { refCounts: nextCounts, drafts: nextDrafts };
    });
  },

  ensureDraft: (key) => {
    if (!key) return;
    const existing = get().drafts[key];
    if (existing) return;
    set((state) => ({
      drafts: { ...state.drafts, [key]: buildEmptyDraft() },
    }));
  },

  initDraft: (key, draft) => {
    if (!key) return;
    const existing = get().drafts[key];
    if (existing?.initialized) return;
    set((state) => ({
      drafts: {
        ...state.drafts,
        [key]: { ...draft, initialized: true },
      },
    }));
  },

  setDraftBaselineSignature: (key, signature) => {
    if (!key) return;
    set((state) => {
      const existing = state.drafts[key] ?? buildEmptyDraft();
      return {
        drafts: {
          ...state.drafts,
          [key]: { ...existing, initialized: true, baselineSignature: signature },
        },
      };
    });
  },

  setDraftForm: (key, action) => {
    if (!key) return;
    set((state) => {
      const existing = state.drafts[key] ?? buildEmptyDraft();
      const nextForm = resolveAction(action, existing.form);
      return {
        drafts: {
          ...state.drafts,
          [key]: { ...existing, initialized: true, form: nextForm },
        },
      };
    });
  },

  setDraftTestModel: (key, action) => {
    if (!key) return;
    set((state) => {
      const existing = state.drafts[key] ?? buildEmptyDraft();
      const nextValue = resolveAction(action, existing.testModel);
      return {
        drafts: {
          ...state.drafts,
          [key]: { ...existing, initialized: true, testModel: nextValue },
        },
      };
    });
  },

  setDraftTestStatus: (key, action) => {
    if (!key) return;
    set((state) => {
      const existing = state.drafts[key] ?? buildEmptyDraft();
      const nextValue = resolveAction(action, existing.testStatus);
      return {
        drafts: {
          ...state.drafts,
          [key]: { ...existing, initialized: true, testStatus: nextValue },
        },
      };
    });
  },

  setDraftTestMessage: (key, action) => {
    if (!key) return;
    set((state) => {
      const existing = state.drafts[key] ?? buildEmptyDraft();
      const nextValue = resolveAction(action, existing.testMessage);
      return {
        drafts: {
          ...state.drafts,
          [key]: { ...existing, initialized: true, testMessage: nextValue },
        },
      };
    });
  },

  setDraftKeyTestStatus: (draftKey, keyIndex, status) => {
    if (!draftKey) return;
    set((state) => {
      const existing = state.drafts[draftKey] ?? buildEmptyDraft();
      const nextStatuses = [...existing.keyTestStatuses];
      nextStatuses[keyIndex] = status;
      return {
        drafts: {
          ...state.drafts,
          [draftKey]: { ...existing, initialized: true, keyTestStatuses: nextStatuses },
        },
      };
    });
  },

  resetDraftKeyTestStatuses: (draftKey, count) => {
    if (!draftKey) return;
    set((state) => {
      const existing = state.drafts[draftKey] ?? buildEmptyDraft();
      return {
        drafts: {
          ...state.drafts,
          [draftKey]: {
            ...existing,
            initialized: true,
            keyTestStatuses: Array.from({ length: count }, () => ({ status: 'idle', message: '' })),
          },
        },
      };
    });
  },

  clearDraft: (key) => {
    if (!key) return;
    set((state) => {
      if (!state.drafts[key] && !state.refCounts[key]) return state;
      const nextDrafts = { ...state.drafts };
      delete nextDrafts[key];
      const nextCounts = { ...state.refCounts };
      delete nextCounts[key];
      return { drafts: nextDrafts, refCounts: nextCounts };
    });
  },
}));
