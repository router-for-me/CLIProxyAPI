import DefaultTheme from 'vitepress/theme'
import { tabsClientScript } from '../plugins/content-tabs'

export default {
  extends: DefaultTheme,
<<<<<<< HEAD
  enhanceApp() {
    if (typeof window === 'undefined') {
      return
    }

    // Mermaid rejects CSS variable strings in themeVariables in some builds.
    // Force plain hex colors to avoid runtime parse failures.
    const applyMermaidColorFallback = () => {
      const mermaid = (window as { mermaid?: { initialize?: (cfg: unknown) => void } }).mermaid
      if (!mermaid || typeof mermaid.initialize !== 'function') {
        return
      }

      mermaid.initialize({
        theme: 'base',
        themeVariables: {
          primaryColor: '#3b82f6',
          primaryBorderColor: '#2563eb',
          primaryTextColor: '#0f172a',
          lineColor: '#64748b',
          textColor: '#0f172a',
          background: '#ffffff'
        }
      })
    }

    window.setTimeout(applyMermaidColorFallback, 0)
  },
=======
>>>>>>> archive/pr-234-head-20260223
  scripts: [
    {
      src: 'data:text/javascript,' + encodeURIComponent(tabsClientScript),
      type: 'text/javascript',
    },
  ],
}
