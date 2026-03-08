import { useCallback, useMemo } from 'react'
import { create } from 'zustand'
import zh, { type TranslationKeys } from './zh'
import en from './en'

/* ============================================================
 * 国际化 — Zustand 全局共享状态 + 自定义 Hook
 * - 语言偏好持久化到 localStorage
 * - 所有组件共享同一语言状态，切换即时生效
 * ============================================================ */

export type Language = 'zh' | 'en'

const STORAGE_KEY = 'community-panel-lang'

const translations: Record<Language, TranslationKeys> = { zh, en }

/**
 * 从 localStorage 读取语言偏好
 * 回退逻辑：localStorage -> 浏览器语言 -> 'zh'
 */
function getInitialLanguage(): Language {
  try {
    const stored = localStorage.getItem(STORAGE_KEY)
    if (stored === 'zh' || stored === 'en') return stored
  } catch {
    // localStorage 不可用时静默降级
  }
  const browserLang = navigator.language.toLowerCase()
  return browserLang.startsWith('zh') ? 'zh' : 'en'
}

/**
 * 通过 dot-separated key 从嵌套对象中取值
 */
function getNestedValue(obj: Record<string, unknown>, path: string): string {
  const keys = path.split('.')
  let current: unknown = obj
  for (const key of keys) {
    if (current === null || current === undefined || typeof current !== 'object') {
      return path
    }
    current = (current as Record<string, unknown>)[key]
  }
  return typeof current === 'string' ? current : path
}

/* ----------------------------------------------------------
 * 内部 Zustand Store（共享语言状态）
 * ---------------------------------------------------------- */

interface I18nStore {
  language: Language
  setLanguage: (lang: Language) => void
}

const useI18nStore = create<I18nStore>()((set) => ({
  language: getInitialLanguage(),

  setLanguage: (lang: Language) => {
    set({ language: lang })
    try {
      localStorage.setItem(STORAGE_KEY, lang)
    } catch {
      // 写入失败静默降级
    }
  },
}))

/* ----------------------------------------------------------
 * 公开 Hook — 组件通过此 Hook 消费 i18n
 * language 变化时，t() 引用更新，触发组件重渲染
 * ---------------------------------------------------------- */

export function useI18n() {
  const language = useI18nStore((s) => s.language)
  const setLanguage = useI18nStore((s) => s.setLanguage)

  const t = useCallback(
    (key: string): string =>
      getNestedValue(
        translations[language] as unknown as Record<string, unknown>,
        key,
      ),
    [language],
  )

  return useMemo(
    () => ({ language, setLanguage, t }),
    [language, setLanguage, t],
  )
}

export type { TranslationKeys }
export { zh, en }
