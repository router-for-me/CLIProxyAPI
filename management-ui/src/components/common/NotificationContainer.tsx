import { useCallback, useEffect, useRef, useState, type RefObject } from 'react';
import { useTranslation } from 'react-i18next';
import { useNotificationStore } from '@/stores';
import { IconAlertTriangle, IconCheckCircle2, IconInfo, IconX } from '@/components/ui/icons';
import { NOTIFICATION_DURATION_MS } from '@/utils/constants';
import type { Notification } from '@/types';
import { createNotificationTimer } from './notificationTimer';
import styles from './NotificationContainer.module.scss';

interface AnimatedNotification extends Notification {
  isExiting?: boolean;
}

// Matches the exit transition; each card owns and cleans up its removal timer.
const EXIT_DURATION_MS = 160;

const notificationIcons = {
  success: IconCheckCircle2,
  info: IconInfo,
  warning: IconAlertTriangle,
  error: IconInfo,
};

function NotificationCard({
  notification,
  onExited,
  returnFocusRef,
}: {
  notification: AnimatedNotification;
  onExited: (id: string) => void;
  returnFocusRef: RefObject<HTMLElement | null>;
}) {
  const { t } = useTranslation();
  const removeNotification = useNotificationStore((state) => state.removeNotification);
  const timerRef = useRef<ReturnType<typeof createNotificationTimer> | null>(null);
  const cardRef = useRef<HTMLDivElement>(null);
  const { id, type, message, duration = NOTIFICATION_DURATION_MS, isExiting } = notification;
  const Icon = notificationIcons[type];

  useEffect(() => {
    if (isExiting) {
      const exitTimer = setTimeout(() => onExited(id), EXIT_DURATION_MS);
      return () => clearTimeout(exitTimer);
    }

    const timer = createNotificationTimer(duration, () => removeNotification(id));
    timerRef.current = timer;
    const syncVisibility = () => timer.setPaused('hidden', document.hidden);
    syncVisibility();
    timer.setPaused('focus', Boolean(cardRef.current?.contains(document.activeElement)));
    timer.setPaused('hover', Boolean(cardRef.current?.matches(':hover')));
    document.addEventListener('visibilitychange', syncVisibility);
    return () => {
      document.removeEventListener('visibilitychange', syncVisibility);
      timer.dispose();
      timerRef.current = null;
    };
  }, [duration, id, isExiting, onExited, removeNotification]);

  const dismiss = (skipAnimation = false) => {
    const card = cardRef.current;
    if (card?.contains(document.activeElement)) {
      const buttons = Array.from(
        card.parentElement?.querySelectorAll<HTMLButtonElement>('button:not(:disabled)') ?? []
      );
      const index = buttons.indexOf(card.querySelector('button')!);
      const target = buttons[index + 1] ?? buttons[index - 1] ?? returnFocusRef.current;
      if (target?.isConnected) target.focus({ preventScroll: true });
    }
    removeNotification(id);
    if (skipAnimation) onExited(id);
  };

  return (
    <div
      ref={cardRef}
      className={`${styles.notification} ${styles[type]}`}
      data-exiting={isExiting || undefined}
      aria-hidden={isExiting || undefined}
      inert={isExiting || undefined}
      onPointerEnter={(event) => {
        if (event.pointerType !== 'touch') timerRef.current?.setPaused('hover', true);
      }}
      onPointerLeave={() => timerRef.current?.setPaused('hover', false)}
      onFocusCapture={() => timerRef.current?.setPaused('focus', true)}
      onBlurCapture={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget)) {
          timerRef.current?.setPaused('focus', false);
        }
      }}
      onKeyDown={(event) => {
        if (event.key !== 'Escape') return;
        event.stopPropagation();
        dismiss(true);
      }}
    >
      <span className={styles.icon} aria-hidden="true">
        <Icon size={18} />
      </span>
      <div
        className={styles.message}
        role={type === 'error' ? 'alert' : 'status'}
        aria-atomic="true"
      >
        <span className={styles.srOnly}>{t(`notification.type_${type}`)}: </span>
        {message}
      </div>
      <button
        type="button"
        className={styles.closeButton}
        onClick={(event) => dismiss(event.detail === 0)}
        disabled={isExiting}
        aria-label={t('common.close')}
      >
        <IconX size={16} />
      </button>
    </div>
  );
}

export function NotificationContainer() {
  const { t } = useTranslation();
  const notifications = useNotificationStore((state) => state.notifications);
  const [rendered, setRendered] = useState<AnimatedNotification[]>(notifications);
  const returnFocusRef = useRef<HTMLElement | null>(null);

  useEffect(() => {
    setRendered((previous) => {
      const current = new Map(notifications.map((notification) => [notification.id, notification]));
      const previousIds = new Set(previous.map((notification) => notification.id));
      return [
        ...previous.map((notification) =>
          current.has(notification.id)
            ? current.get(notification.id)!
            : { ...notification, isExiting: true }
        ),
        ...notifications.filter((notification) => !previousIds.has(notification.id)),
      ];
    });
  }, [notifications]);

  const handleExited = useCallback((id: string) => {
    setRendered((previous) => previous.filter((notification) => notification.id !== id));
  }, []);

  return (
    <section
      className={styles.container}
      aria-label={t('notification.region_label')}
      onFocusCapture={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget)) {
          returnFocusRef.current =
            event.relatedTarget instanceof HTMLElement ? event.relatedTarget : null;
        }
      }}
    >
      {rendered.map((notification) => (
        <NotificationCard
          key={notification.id}
          notification={notification}
          onExited={handleExited}
          returnFocusRef={returnFocusRef}
        />
      ))}
    </section>
  );
}
