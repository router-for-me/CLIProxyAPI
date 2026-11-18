import { useRef, type KeyboardEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { PLUGIN_VIEWS, pluginViewPanelId, pluginViewTabId, type PluginsView } from '../pluginsView';
import styles from './PluginViewSwitch.module.scss';

interface PluginViewSwitchProps {
  view: PluginsView;
  /** `null` while unknown (still loading or failed), so no misleading zero is shown. */
  counts: Record<PluginsView, number | null>;
  updatesAvailable: boolean;
  onChange: (view: PluginsView) => void;
}

/**
 * Installed / Store segmented control with the tablist pattern of `ConfigTabs`: arrow keys,
 * Home and End move selection and focus, and only the active tab is in the tab order.
 * The indicator slides with `transform`; the panels themselves swap instantly.
 */
export function PluginViewSwitch({
  view,
  counts,
  updatesAvailable,
  onChange,
}: PluginViewSwitchProps) {
  const { t } = useTranslation();
  const buttonRefs = useRef<Partial<Record<PluginsView, HTMLButtonElement | null>>>({});

  const handleKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
    const count = PLUGIN_VIEWS.length;
    const currentIndex = PLUGIN_VIEWS.indexOf(view);
    let nextIndex = -1;
    if (event.key === 'ArrowRight') nextIndex = (currentIndex + 1) % count;
    else if (event.key === 'ArrowLeft') nextIndex = (currentIndex - 1 + count) % count;
    else if (event.key === 'Home') nextIndex = 0;
    else if (event.key === 'End') nextIndex = count - 1;
    if (nextIndex < 0) return;
    event.preventDefault();
    const next = PLUGIN_VIEWS[nextIndex];
    onChange(next);
    buttonRefs.current[next]?.focus();
  };

  return (
    <div
      className={styles.segmented}
      role="tablist"
      aria-label={t('plugin_management.views_label')}
      data-active={view}
    >
      <span className={styles.indicator} aria-hidden="true" />
      {PLUGIN_VIEWS.map((id) => {
        const isActive = view === id;
        const label = t(
          id === 'installed' ? 'plugin_management.view_installed' : 'plugin_management.view_store'
        );
        const count = counts[id];
        const showUpdateDot = id === 'installed' && updatesAvailable;
        const accessibleLabel = [
          label,
          count === null ? null : String(count),
          showUpdateDot ? t('plugin_management.updates_available') : null,
        ]
          .filter(Boolean)
          .join(', ');

        return (
          <button
            key={id}
            ref={(node) => {
              buttonRefs.current[id] = node;
            }}
            type="button"
            role="tab"
            id={pluginViewTabId(id)}
            aria-selected={isActive}
            aria-controls={pluginViewPanelId(id)}
            aria-label={accessibleLabel}
            tabIndex={isActive ? 0 : -1}
            className={`${styles.segment} ${isActive ? styles.segmentActive : ''}`}
            onClick={() => onChange(id)}
            onKeyDown={handleKeyDown}
          >
            <span>{label}</span>
            {count !== null ? <span className={styles.count}>{count}</span> : null}
            {showUpdateDot ? <span className={styles.updateDot} aria-hidden="true" /> : null}
          </button>
        );
      })}
    </div>
  );
}
