/**
 * 语言状态管理
 * 从原项目 src/modules/language.js 迁移
 */

import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import type { Language } from '@/types';
import { LANGUAGE_ORDER, STORAGE_KEY_LANGUAGE } from '@/utils/constants';
import i18n from '@/i18n';
import { getInitialLanguage, isSupportedLanguage } from '@/utils/language';

interface LanguageState {
  language: Language;
  setLanguage: (language: string) => void;
  toggleLanguage: () => void;
}

export const useLanguageStore = create<LanguageState>()(
  persist(
    (set, get) => ({
      language: getInitialLanguage(),

      setLanguage: (language) => {
        if (!isSupportedLanguage(language)) {
          return;
        }
        // 切换 i18next 语言
        i18n.changeLanguage(language);
        set({ language });
      },

      toggleLanguage: () => {
        const { language, setLanguage } = get();
        const currentIndex = LANGUAGE_ORDER.indexOf(language);
        const nextLanguage = LANGUAGE_ORDER[(currentIndex + 1) % LANGUAGE_ORDER.length];
        setLanguage(nextLanguage);
      }
    }),
    {
      name: STORAGE_KEY_LANGUAGE,
      merge: (persistedState, currentState) => {
        const nextLanguage = (persistedState as Partial<LanguageState>)?.language;
        if (typeof nextLanguage === 'string' && isSupportedLanguage(nextLanguage)) {
          return {
            ...currentState,
            ...(persistedState as Partial<LanguageState>),
            language: nextLanguage
          };
        }
        return currentState;
      }
    }
  )
);
