import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { animate } from 'motion/mini';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import { IconCheck } from '@/components/ui/icons';
import { prefersReducedMotion } from '@/hooks/motion';
import { useActionBarHeightVar } from '@/hooks/useActionBarHeightVar';
import type { ConfigStatusTone } from '../uiState';
import styles from './FloatingSaveBar.module.scss';

const easeOutQuart = (progress: number) => 1 - (1 - progress) ** 4;
const easeInCubic = (progress: number) => progress ** 3;
const BASE_TRANSFORM = 'translateX(-50%)';
const HIDDEN_TRANSFORM = 'translateX(-50%) translateY(56px)';

export type FloatingSaveBarProps = {
  /** 有未保存修改时可见（与未保存离开守卫的 block 条件一致）。 */
  visible: boolean;
  statusText: string;
  statusTone: ConfigStatusTone;
  saving: boolean;
  saveDisabled: boolean;
  discardDisabled: boolean;
  onSave: () => void;
  onDiscard: () => void;
};

/**
 * 悬浮保存栏：portal 到 body 的玻璃工具栏，仅在 dirty 时出现。
 * - 上浮入场 0.28s 强减速，退场 0.22s 加速后卸载；
 * - reduced-motion 只做透明度淡入淡出（保留 translateX(-50%)，防止错位半宽）；
 * - 实时高度写入 --config-action-bar-height 供页面底部留白。
 */
export function FloatingSaveBar(props: FloatingSaveBarProps) {
  const {
    visible,
    statusText,
    statusTone,
    saving,
    saveDisabled,
    discardDisabled,
    onSave,
    onDiscard,
  } = props;
  const { t } = useTranslation();

  const [mounted, setMounted] = useState(false);
  const containerRef = useRef<HTMLDivElement | null>(null);
  const animationRef = useRef<ReturnType<typeof animate> | null>(null);
  const visibleRef = useRef(visible);
  const previousVisibleRef = useRef(false);

  useActionBarHeightVar(containerRef, '--config-action-bar-height', mounted);

  useEffect(() => {
    visibleRef.current = visible;
    if (visible) setMounted(true);
  }, [visible]);

  useLayoutEffect(() => {
    if (!mounted) return;
    const el = containerRef.current;
    if (!el) return;
    const wasVisible = previousVisibleRef.current;

    animationRef.current?.stop();
    animationRef.current = null;

    const reduced = prefersReducedMotion();

    if (visible && !wasVisible) {
      if (reduced) {
        el.style.transform = BASE_TRANSFORM;
        animationRef.current = animate(
          el,
          { opacity: [0, 1] },
          {
            duration: 0.15,
            ease: 'linear',
            onComplete: () => {
              el.style.opacity = '1';
            },
          }
        );
      } else {
        animationRef.current = animate(
          el,
          { transform: [HIDDEN_TRANSFORM, BASE_TRANSFORM], opacity: [0, 1] },
          {
            duration: 0.28,
            ease: easeOutQuart,
            onComplete: () => {
              el.style.transform = BASE_TRANSFORM;
              el.style.opacity = '1';
            },
          }
        );
      }
    } else if (!visible && wasVisible) {
      const finishExit = () => {
        if (!visibleRef.current) setMounted(false);
      };
      if (reduced) {
        el.style.transform = BASE_TRANSFORM;
        animationRef.current = animate(
          el,
          { opacity: [1, 0] },
          { duration: 0.12, ease: 'linear', onComplete: finishExit }
        );
      } else {
        animationRef.current = animate(
          el,
          { transform: [BASE_TRANSFORM, HIDDEN_TRANSFORM], opacity: [1, 0] },
          { duration: 0.22, ease: easeInCubic, onComplete: finishExit }
        );
      }
    }

    previousVisibleRef.current = visible;
  }, [mounted, visible]);

  useEffect(
    () => () => {
      animationRef.current?.stop();
      animationRef.current = null;
    },
    []
  );

  if (!mounted || typeof document === 'undefined') return null;

  const toneClass: Record<ConfigStatusTone, string> = {
    error: styles.statusError,
    warning: styles.statusWarning,
    busy: styles.statusBusy,
    muted: styles.statusMuted,
    ok: styles.statusOk,
  };

  return createPortal(
    <div className={styles.container} ref={containerRef}>
      <div className={styles.bar} role="group" aria-label={t('config_management.status_dirty')}>
        <span className={`${styles.status} ${toneClass[statusTone]}`} aria-live="polite">
          {statusText}
        </span>
        <div className={styles.actionsGroup}>
          <button
            type="button"
            className={styles.ghostAction}
            onClick={onDiscard}
            disabled={discardDisabled}
          >
            {t('config_management.actions.discard')}
          </button>
          <button
            type="button"
            className={styles.savePill}
            onClick={onSave}
            disabled={saveDisabled}
          >
            {saving ? <LoadingSpinner size={14} /> : <IconCheck size={15} />}
            {t('config_management.actions.save')}
          </button>
        </div>
      </div>
    </div>,
    document.body
  );
}
