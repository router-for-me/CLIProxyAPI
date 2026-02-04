import { useState, useEffect, useCallback, useRef, type PropsWithChildren, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { IconX } from './icons';

interface ModalProps {
  open: boolean;
  title?: ReactNode;
  onClose: () => void;
  footer?: ReactNode;
  width?: number | string;
  closeDisabled?: boolean;
}

const CLOSE_ANIMATION_DURATION = 350;
const MODAL_LOCK_CLASS = 'modal-open';
let activeModalCount = 0;

const lockScroll = () => {
  if (typeof document === 'undefined') return;
  if (activeModalCount === 0) {
    document.body?.classList.add(MODAL_LOCK_CLASS);
    document.documentElement?.classList.add(MODAL_LOCK_CLASS);
  }
  activeModalCount += 1;
};

const unlockScroll = () => {
  if (typeof document === 'undefined') return;
  activeModalCount = Math.max(0, activeModalCount - 1);
  if (activeModalCount === 0) {
    document.body?.classList.remove(MODAL_LOCK_CLASS);
    document.documentElement?.classList.remove(MODAL_LOCK_CLASS);
  }
};

export function Modal({
  open,
  title,
  onClose,
  footer,
  width = 520,
  closeDisabled = false,
  children
}: PropsWithChildren<ModalProps>) {
  const [isVisible, setIsVisible] = useState(false);
  const [isClosing, setIsClosing] = useState(false);
  const closeTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

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

  const handleClose = useCallback(() => {
    startClose(true);
  }, [startClose]);

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

  if (!open && !isVisible) return null;

  const overlayClass = `modal-overlay ${isClosing ? 'modal-overlay-closing' : 'modal-overlay-entering'}`;
  const modalClass = `modal ${isClosing ? 'modal-closing' : 'modal-entering'}`;

  const modalContent = (
    <div className={overlayClass}>
      <div className={modalClass} style={{ width }} role="dialog" aria-modal="true">
        <button
          type="button"
          className="modal-close-floating"
          onClick={closeDisabled ? undefined : handleClose}
          aria-label="Close"
          disabled={closeDisabled}
        >
          <IconX size={20} />
        </button>
        <div className="modal-header">
          <div className="modal-title">{title}</div>
        </div>
        <div className="modal-body">{children}</div>
        {footer && <div className="modal-footer">{footer}</div>}
      </div>
    </div>
  );

  if (typeof document === 'undefined') {
    return modalContent;
  }

  return createPortal(modalContent, document.body);
}
