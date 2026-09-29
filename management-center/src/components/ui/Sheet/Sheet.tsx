import {
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
  type ReactNode,
  type PropsWithChildren,
} from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { IconX } from '../icons';
import { FOCUSABLE_SELECTOR, lockScroll, unlockScroll } from '../scrollLock';
import styles from './Sheet.module.scss';

export type SheetSize = 'md' | 'lg' | 'xl';

interface SheetProps {
  open: boolean;
  onClose: () => void;
  size?: SheetSize;
  eyebrow?: ReactNode;
  title?: ReactNode;
  description?: ReactNode;
  footer?: ReactNode;
  closeDisabled?: boolean;
  className?: string;
  ariaLabel?: string;
  /**
   * If provided, called before starting the close animation when the user
   * triggers a close (Escape, overlay click, or close button). Return false
   * (or a Promise that resolves to false) to keep the sheet open.
   */
  confirmClose?: () => boolean | Promise<boolean>;
}

const CLOSE_ANIMATION_DURATION = 280;
const SIZE_CLASS: Record<SheetSize, string> = {
  md: styles.sizeMd,
  lg: styles.sizeLg,
  xl: styles.sizeXl,
};

export function Sheet({
  open,
  onClose,
  size = 'md',
  eyebrow,
  title,
  description,
  footer,
  closeDisabled = false,
  className,
  ariaLabel,
  confirmClose,
  children,
}: PropsWithChildren<SheetProps>) {
  const { t } = useTranslation();
  const titleId = useId();
  const descId = useId();
  const [isVisible, setIsVisible] = useState(false);
  const [isClosing, setIsClosing] = useState(false);
  const closeTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const sheetRef = useRef<HTMLDivElement | null>(null);
  const bodyRef = useRef<HTMLDivElement | null>(null);
  const closeBtnRef = useRef<HTMLButtonElement | null>(null);
  const previouslyFocusedRef = useRef<HTMLElement | null>(null);

  const getFocusableElements = useCallback(() => {
    if (!sheetRef.current) return [] as HTMLElement[];
    return Array.from(sheetRef.current.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)).filter(
      (el) => !el.hasAttribute('disabled') && el.tabIndex !== -1
    );
  }, []);

  const startClose = useCallback(
    (notifyParent: boolean) => {
      if (closeTimerRef.current !== null) return;
      setIsClosing(true);
      closeTimerRef.current = window.setTimeout(() => {
        setIsVisible(false);
        setIsClosing(false);
        closeTimerRef.current = null;
        if (notifyParent) {
          onClose();
        }
      }, CLOSE_ANIMATION_DURATION);
    },
    [onClose]
  );

  useEffect(() => {
    let cancelled = false;

    if (open) {
      if (closeTimerRef.current !== null) {
        window.clearTimeout(closeTimerRef.current);
        closeTimerRef.current = null;
      }
      queueMicrotask(() => {
        if (cancelled) return;
        setIsVisible(true);
        setIsClosing(false);
      });
    } else if (isVisible) {
      queueMicrotask(() => {
        if (cancelled) return;
        startClose(false);
      });
    }

    return () => {
      cancelled = true;
    };
  }, [open, isVisible, startClose]);

  const handleClose = useCallback(async () => {
    if (confirmClose) {
      try {
        const ok = await confirmClose();
        if (ok === false) return;
      } catch {
        return;
      }
    }
    startClose(true);
  }, [confirmClose, startClose]);

  useEffect(() => {
    return () => {
      if (closeTimerRef.current !== null) {
        window.clearTimeout(closeTimerRef.current);
      }
    };
  }, []);

  const shouldLockScroll = open || isVisible;

  useEffect(() => {
    if (!shouldLockScroll) return;
    lockScroll();
    return () => unlockScroll();
  }, [shouldLockScroll]);

  useEffect(() => {
    if (!open) return;
    previouslyFocusedRef.current =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const t = window.setTimeout(() => {
      if (bodyRef.current) bodyRef.current.scrollTop = 0;
      const first = getFocusableElements()[0];
      (first ?? closeBtnRef.current ?? sheetRef.current)?.focus({ preventScroll: true });
    }, 0);
    return () => window.clearTimeout(t);
  }, [getFocusableElements, open]);

  useEffect(() => {
    if (open || isVisible) return;
    previouslyFocusedRef.current?.focus();
    previouslyFocusedRef.current = null;
  }, [isVisible, open]);

  useEffect(() => {
    if (!open) return;
    const handleKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        if (closeDisabled) return;
        event.preventDefault();
        handleClose();
        return;
      }
      if (event.key !== 'Tab') return;
      const focusables = getFocusableElements();
      if (focusables.length === 0) {
        event.preventDefault();
        sheetRef.current?.focus();
        return;
      }
      const firstEl = focusables[0];
      const lastEl = focusables[focusables.length - 1];
      const active = document.activeElement as HTMLElement | null;
      if (event.shiftKey) {
        if (active === firstEl || active === sheetRef.current) {
          event.preventDefault();
          lastEl.focus();
        }
        return;
      }
      if (active === lastEl) {
        event.preventDefault();
        firstEl.focus();
      }
    };
    document.addEventListener('keydown', handleKey);
    return () => document.removeEventListener('keydown', handleKey);
  }, [closeDisabled, getFocusableElements, handleClose, open]);

  if (!open && !isVisible) return null;

  const stateClass = isClosing ? styles.exiting : styles.entering;
  const overlayCls = `${styles.overlay} ${stateClass}`.trim();
  const contentCls = [styles.content, SIZE_CLASS[size], stateClass, className]
    .filter(Boolean)
    .join(' ');

  const content = (
    <div
      className={overlayCls}
      role="presentation"
      onMouseDown={(e) => {
        if (closeDisabled) return;
        if (e.target === e.currentTarget) handleClose();
      }}
    >
      <div
        ref={sheetRef}
        className={contentCls}
        role="dialog"
        aria-modal="true"
        aria-labelledby={title ? titleId : undefined}
        aria-describedby={description ? descId : undefined}
        aria-label={!title && ariaLabel ? ariaLabel : undefined}
        tabIndex={-1}
        onMouseDown={(e) => e.stopPropagation()}
      >
        <button
          ref={closeBtnRef}
          type="button"
          className={styles.closeBtn}
          onClick={closeDisabled ? undefined : handleClose}
          disabled={closeDisabled}
          aria-label={t('common.close')}
        >
          <IconX size={18} />
        </button>
        {(eyebrow || title || description) && (
          <div className={styles.header}>
            {eyebrow ? <div className={styles.eyebrow}>{eyebrow}</div> : null}
            {title ? (
              <h2 id={titleId} className={styles.title}>
                {title}
              </h2>
            ) : null}
            {description ? (
              <p id={descId} className={styles.description}>
                {description}
              </p>
            ) : null}
          </div>
        )}
        <div ref={bodyRef} className={styles.body}>
          {children}
        </div>
        {footer ? <div className={styles.footer}>{footer}</div> : null}
      </div>
    </div>
  );

  if (typeof document === 'undefined') return content;
  return createPortal(content, document.body);
}
