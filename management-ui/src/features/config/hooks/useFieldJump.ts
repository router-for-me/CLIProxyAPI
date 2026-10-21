// 搜索跳转：切换到目标分区 tab → 等目标挂载 → 展开折叠组 → 滚动居中 → 1800ms 脉冲高亮。
// 旧实现要与横向滚动吸附轮播搏斗（两段式 scrollIntoView）；轮播已退役，只剩纵向滚动。

import { useCallback, useEffect, useRef, useState } from 'react';
import { prefersReducedMotion } from '@/hooks/motion';
import type { VisualConfigValues } from '@/types/visualConfig';
import { FIELD_HIGHLIGHT_CLASS } from '../components/fields/FieldPrimitives';
import type { ConfigTabId } from '../constants';
import {
  configFieldDomId,
  type ConfigFieldSearchEntry,
  type VisualSectionId,
} from '../searchIndex';

export type UseFieldJumpArgs = {
  values: VisualConfigValues;
  /** 切换激活 tab（页面的 handleSectionChange，含 localStorage 持久化）。 */
  setActiveSection: (id: ConfigTabId) => void;
};

export function useFieldJump({ values, setActiveSection }: UseFieldJumpArgs) {
  // A fresh object per jump; the effect handles it once (guarded by handledJumpRef) so it
  // never needs to clear state from inside the effect.
  const [jumpRequest, setJumpRequest] = useState<{
    fieldId: string;
    sectionId: VisualSectionId;
  } | null>(null);
  const handledJumpRef = useRef<{ fieldId: string; sectionId: VisualSectionId } | null>(null);
  const highlightTimerRef = useRef<number | null>(null);
  const highlightedElRef = useRef<HTMLElement | null>(null);

  const jumpToField = useCallback(
    (entry: ConfigFieldSearchEntry) => {
      // 永远跳正典分区（常用 tab 是别名视图，字段的老家在各自分区）。
      setActiveSection(entry.sectionId);
      setJumpRequest({ fieldId: entry.fieldId, sectionId: entry.sectionId });
    },
    [setActiveSection]
  );

  // Imperatively scroll to and pulse-highlight the jumped-to field once the target
  // section tab has mounted.
  useEffect(() => {
    if (!jumpRequest || handledJumpRef.current === jumpRequest) return;
    handledJumpRef.current = jumpRequest; // handle each request once, even if deps re-fire
    const { fieldId } = jumpRequest;
    // TLS cert/key 在 TLS 关闭时不渲染 —— 重定向到 tlsEnable 开关。
    const targetFieldId =
      (fieldId === 'tlsCert' || fieldId === 'tlsKey') && !values.tlsEnable ? 'tlsEnable' : fieldId;

    const attempt = (retriesLeft: number) => {
      const el = document.getElementById(configFieldDomId(targetFieldId));
      if (!el) {
        // Tab 刚切换：目标分区可能还没提交到 DOM，隔帧重试一次。
        if (retriesLeft > 0) requestAnimationFrame(() => attempt(retriesLeft - 1));
        return;
      }

      // Expand the collapsed <details> group this field belongs to: an ancestor when the
      // anchor sits inside the group (TLS / remote / advanced fields), or a descendant when
      // the anchor wraps the whole group (payload rule groups).
      const details = el.closest('details') ?? el.querySelector('details');
      if (details && !details.open) details.open = true;

      // Clear any in-flight highlight before starting a new one.
      if (highlightTimerRef.current !== null) {
        clearTimeout(highlightTimerRef.current);
        highlightedElRef.current?.classList.remove(FIELD_HIGHLIGHT_CLASS);
      }

      el.scrollIntoView({
        behavior: prefersReducedMotion() ? 'auto' : 'smooth',
        block: 'center',
        inline: 'nearest',
      });
      el.classList.add(FIELD_HIGHLIGHT_CLASS);
      highlightedElRef.current = el;
      highlightTimerRef.current = window.setTimeout(() => {
        el.classList.remove(FIELD_HIGHLIGHT_CLASS);
        highlightTimerRef.current = null;
        highlightedElRef.current = null;
      }, 1800);
    };

    requestAnimationFrame(() => attempt(1));
  }, [jumpRequest, values.tlsEnable]);

  // Clear the highlight timer on unmount.
  useEffect(
    () => () => {
      if (highlightTimerRef.current !== null) clearTimeout(highlightTimerRef.current);
    },
    []
  );

  return { jumpToField };
}
