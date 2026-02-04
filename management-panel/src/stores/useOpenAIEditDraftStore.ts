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

export type OpenAIEditDraft = {
  initialized: boolean;
  form: OpenAIFormState;
  testModel: string;
  testStatus: OpenAITestStatus;
  testMessage: string;
};

interface OpenAIEditDraftState {
  drafts: Record<string, OpenAIEditDraft>;
  ensureDraft: (key: string) => void;
  initDraft: (key: string, draft: Omit<OpenAIEditDraft, 'initialized'>) => void;
  setDraftForm: (key: string, action: SetStateAction<OpenAIFormState>) => void;
  setDraftTestModel: (key: string, action: SetStateAction<string>) => void;
  setDraftTestStatus: (key: string, action: SetStateAction<OpenAITestStatus>) => void;
  setDraftTestMessage: (key: string, action: SetStateAction<string>) => void;
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
  form: buildEmptyForm(),
  testModel: '',
  testStatus: 'idle',
  testMessage: '',
});

export const useOpenAIEditDraftStore = create<OpenAIEditDraftState>((set, get) => ({
  drafts: {},

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

  clearDraft: (key) => {
    if (!key) return;
    set((state) => {
      if (!state.drafts[key]) return state;
      const next = { ...state.drafts };
      delete next[key];
      return { drafts: next };
    });
  },
}));

