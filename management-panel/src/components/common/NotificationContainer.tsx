import { useEffect, useRef, useState } from 'react';
import { useNotificationStore } from '@/stores';
import { IconX } from '@/components/ui/icons';
import type { Notification } from '@/types';

interface AnimatedNotification extends Notification {
  isExiting?: boolean;
}

const ANIMATION_DURATION = 300; // ms

export function NotificationContainer() {
  const { notifications, removeNotification } = useNotificationStore();
  const [animatedNotifications, setAnimatedNotifications] = useState<AnimatedNotification[]>([]);
  const prevNotificationsRef = useRef<Notification[]>([]);

  // Track notifications and manage animation states
  useEffect(() => {
    const prevNotifications = prevNotificationsRef.current;
    const prevIds = new Set(prevNotifications.map((n) => n.id));
    const currentIds = new Set(notifications.map((n) => n.id));

    // Find new notifications (for enter animation)
    const newNotifications = notifications.filter((n) => !prevIds.has(n.id));

    // Find removed notifications (for exit animation)
    const removedIds = new Set(
      prevNotifications.filter((n) => !currentIds.has(n.id)).map((n) => n.id)
    );

    setAnimatedNotifications((prev) => {
      // Mark removed notifications as exiting
      let updated = prev.map((n) =>
        removedIds.has(n.id) ? { ...n, isExiting: true } : n
      );

      // Add new notifications
      newNotifications.forEach((n) => {
        if (!updated.find((an) => an.id === n.id)) {
          updated.push({ ...n, isExiting: false });
        }
      });

      // Remove notifications that are not in current and not exiting
      // (they've already completed their exit animation)
      updated = updated.filter(
        (n) => currentIds.has(n.id) || n.isExiting
      );

      return updated;
    });

    // Clean up exited notifications after animation
    if (removedIds.size > 0) {
      setTimeout(() => {
        setAnimatedNotifications((prev) =>
          prev.filter((n) => !removedIds.has(n.id))
        );
      }, ANIMATION_DURATION);
    }

    prevNotificationsRef.current = notifications;
  }, [notifications]);

  const handleClose = (id: string) => {
    // Start exit animation
    setAnimatedNotifications((prev) =>
      prev.map((n) => (n.id === id ? { ...n, isExiting: true } : n))
    );

    // Actually remove after animation
    setTimeout(() => {
      removeNotification(id);
    }, ANIMATION_DURATION);
  };

  if (!animatedNotifications.length) return null;

  return (
    <div className="notification-container">
      {animatedNotifications.map((notification) => (
        <div
          key={notification.id}
          className={`notification ${notification.type} ${notification.isExiting ? 'exiting' : 'entering'}`}
        >
          <div className="message">{notification.message}</div>
          <button className="close-btn" onClick={() => handleClose(notification.id)} aria-label="Close">
            <IconX size={16} />
          </button>
        </div>
      ))}
    </div>
  );
}
