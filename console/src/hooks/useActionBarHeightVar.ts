import { useLayoutEffect, type RefObject } from 'react';

/**
 * 将悬浮操作条的实时高度同步到根元素 CSS 变量，供页面底部留白使用。
 * active 为 false 或元素未挂载时清除变量。
 */
export function useActionBarHeightVar(
  ref: RefObject<HTMLElement | null>,
  cssVar: string,
  active: boolean
) {
  useLayoutEffect(() => {
    if (typeof window === 'undefined') return;

    const actionsEl = active ? ref.current : null;
    if (!actionsEl) {
      document.documentElement.style.removeProperty(cssVar);
      return;
    }

    const updateHeight = () => {
      const height = actionsEl.getBoundingClientRect().height;
      document.documentElement.style.setProperty(cssVar, `${height}px`);
    };

    updateHeight();
    window.addEventListener('resize', updateHeight);

    const ro = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(updateHeight);
    ro?.observe(actionsEl);

    return () => {
      ro?.disconnect();
      window.removeEventListener('resize', updateHeight);
      document.documentElement.style.removeProperty(cssVar);
    };
  }, [ref, cssVar, active]);
}
