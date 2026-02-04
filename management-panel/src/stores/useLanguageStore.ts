/**
 * 语言状态管理
 * 从原项目 src/modules/language.js 迁移
 */

import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import type { Language } from '@/types';
import { STORAGE_KEY_LANGUAGE } from '@/utils/constants';
import i18n from '@/i18n';
import { getInitialLanguage } from '@/utils/language';

interface LanguageState {
  language: Language;
  setLanguage: (language: Language) => void;
  toggleLanguage: () => void;
}

export const useLanguageStore = create<LanguageState>()(
  persist(
    (set, get) => ({
      language: getInitialLanguage(),

      setLanguage: (language) => {
        // 切换 i18next 语言
        i18n.changeLanguage(language);
        set({ language });
      },

      toggleLanguage: () => {
        const { language, setLanguage } = get();
        const newLanguage: Language = language === 'zh-CN' ? 'en' : 'zh-CN';
        setLanguage(newLanguage);
      }
    }),
    {
      name: STORAGE_KEY_LANGUAGE
    }
  )
);
