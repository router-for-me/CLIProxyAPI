import { createContext, useCallback, useContext, useEffect, useState } from 'react';

const STORAGE_KEY = 'nixllm.theme';
const MEDIA_QUERY = '(prefers-color-scheme: dark)';

const ThemeContext = createContext(null);

// Resolve the effective theme ('light' | 'dark') from the user preference.
function resolve(pref) {
  if (pref === 'light' || pref === 'dark') return pref;
  // 'system' — follow OS preference
  return window.matchMedia(MEDIA_QUERY).matches ? 'dark' : 'light';
}

// Apply the resolved theme to the document root so CSS variable overrides kick
// in immediately. Kept as a standalone function because the inline script in
// index.html performs the same logic before React boots.
function apply(resolved) {
  document.documentElement.setAttribute('data-theme', resolved);
}

// ThemeProvider — wraps the app tree and manages the theme preference.
export function ThemeProvider({ children }) {
  const [theme, setThemeState] = useState(() => {
    const stored = localStorage.getItem(STORAGE_KEY);
    return stored === 'light' || stored === 'dark' ? stored : 'system';
  });

  const setTheme = useCallback((next) => {
    setThemeState(next);
    localStorage.setItem(STORAGE_KEY, next);
    apply(resolve(next));
  }, []);

  // Apply theme on mount and whenever the preference changes.
  useEffect(() => {
    apply(resolve(theme));
  }, [theme]);

  // When 'system' is active, listen for OS-level changes and re-apply.
  useEffect(() => {
    if (theme !== 'system') return;
    const mql = window.matchMedia(MEDIA_QUERY);
    const handler = () => apply(resolve('system'));
    mql.addEventListener('change', handler);
    return () => mql.removeEventListener('change', handler);
  }, [theme]);

  return (
    <ThemeContext.Provider value={{ theme, setTheme }}>
      {children}
    </ThemeContext.Provider>
  );
}

// useTheme — returns { theme, setTheme } where theme is the preference
// ('light' | 'dark' | 'system'), not the resolved value.
export function useTheme() {
  const ctx = useContext(ThemeContext);
  if (!ctx) throw new Error('useTheme must be used within ThemeProvider');
  return ctx;
}
