import { useEffect, useRef, useState, type KeyboardEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { scrollProviderTabs } from '@/features/authFiles/components/providerTabsWheel';
import { connectionCounts, isQuickFillBrand } from '../resourceView';
import type { ProviderBrand, ProviderGroup } from '../types';
import { BrandLogo } from './BrandLogo';
import styles from './BrandTabs.module.scss';

interface BrandTabsProps {
  /** Every brand in display order; quick-connect partners follow a hairline divider. */
  groups: ProviderGroup[];
  activeBrand: ProviderBrand;
  onSelect: (brand: ProviderBrand) => void;
  /** Builds the id shared by a tab and the panel it controls. */
  tabId: (brand: ProviderBrand) => string;
  panelId: string;
  onAdd: () => void;
  /** Opens the "add provider" catalogue from the quick-connect invitation. */
  partnerBrands?: ProviderBrand[];
  addDisabled?: boolean;
}

/**
 * Brand strip: quiet underline tabs, brand colour only in the mark. The strip scrolls when
 * it overflows, while the quick-connect invitation stays pinned on the right so partners
 * remain visible at any width.
 */
export function BrandTabs({
  groups,
  activeBrand,
  onSelect,
  tabId,
  panelId,
  onAdd,
  partnerBrands = [],
  addDisabled = false,
}: BrandTabsProps) {
  const { t } = useTranslation();
  const stripRef = useRef<HTMLDivElement>(null);
  const [overflow, setOverflow] = useState({ start: false, end: false });

  useEffect(() => {
    const strip = stripRef.current;
    if (!strip) return;
    const onWheel = (event: WheelEvent) => scrollProviderTabs(strip, event);
    // Fade only the edges that actually hide tabs, so the strip never looks cut off for nothing.
    const measure = () => {
      const max = strip.scrollWidth - strip.clientWidth;
      const next = { start: strip.scrollLeft > 1, end: strip.scrollLeft < max - 1 };
      setOverflow((prev) => (prev.start === next.start && prev.end === next.end ? prev : next));
    };
    measure();
    strip.addEventListener('wheel', onWheel, { passive: false });
    strip.addEventListener('scroll', measure, { passive: true });
    const observer = typeof ResizeObserver !== 'undefined' ? new ResizeObserver(measure) : null;
    observer?.observe(strip);
    return () => {
      strip.removeEventListener('wheel', onWheel);
      strip.removeEventListener('scroll', measure);
      observer?.disconnect();
    };
  }, [groups.length]);

  // Keep the active tab visible when it changes (narrow screens scroll the strip).
  useEffect(() => {
    const active = stripRef.current?.querySelector<HTMLElement>('[aria-selected="true"]');
    active?.scrollIntoView?.({ block: 'nearest', inline: 'nearest' });
  }, [activeBrand]);

  const focusBrand = (brand: ProviderBrand) => {
    onSelect(brand);
    // Focus after React commits the new roving tabindex.
    requestAnimationFrame(() => document.getElementById(tabId(brand))?.focus());
  };

  const handleKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
    const index = groups.findIndex((group) => group.id === activeBrand);
    if (index < 0 || groups.length === 0) return;
    let next = -1;
    if (event.key === 'ArrowRight') next = (index + 1) % groups.length;
    else if (event.key === 'ArrowLeft') next = (index - 1 + groups.length) % groups.length;
    else if (event.key === 'Home') next = 0;
    else if (event.key === 'End') next = groups.length - 1;
    if (next < 0) return;
    event.preventDefault();
    focusBrand(groups[next].id);
  };

  return (
    <div className={styles.bar}>
      <div
        ref={stripRef}
        className={styles.strip}
        data-overflow-start={overflow.start || undefined}
        data-overflow-end={overflow.end || undefined}
        role="tablist"
        aria-label={t('providersPage.categories.title')}
      >
        {groups.map((group, index) => {
          const active = group.id === activeBrand;
          const startsPartners =
            isQuickFillBrand(group.id) && (index === 0 || !isQuickFillBrand(groups[index - 1].id));
          const counts = connectionCounts(group);
          const total = counts.total;
          return [
            startsPartners ? (
              <span key="partners-divider" className={styles.divider} aria-hidden="true">
                {t('providersPage.categories.quickFill')}
              </span>
            ) : null,
            <button
              key={group.id}
              id={tabId(group.id)}
              type="button"
              role="tab"
              aria-selected={active}
              aria-controls={panelId}
              tabIndex={active ? 0 : -1}
              className={`${styles.tab} ${active ? styles.tabActive : ''}`}
              onClick={() => onSelect(group.id)}
              onKeyDown={handleKeyDown}
            >
              <BrandLogo brand={group.id} size={18} />
              <span className={styles.label}>{t(`providersPage.providerNames.${group.id}`)}</span>
              {total > 0 ? (
                <span
                  className={styles.count}
                  title={t('providersPage.categories.activeCount', {
                    active: counts.active,
                    total,
                  })}
                >
                  {total}
                </span>
              ) : null}
            </button>,
          ];
        })}
      </div>

      {partnerBrands.length > 0 ? (
        <button
          type="button"
          className={styles.partners}
          onClick={onAdd}
          disabled={addDisabled}
          aria-label={t('providersPage.picker.moreQuickConnect')}
          title={partnerBrands
            .map((brand) => t(`providersPage.providerNames.${brand}`))
            .join(' · ')}
        >
          <span className={styles.partnersLabel}>{t('providersPage.picker.featured')}</span>
          <span className={styles.partnersStack} aria-hidden="true">
            {partnerBrands.map((brand) => (
              <BrandLogo key={brand} brand={brand} size={24} className={styles.partnerLogo} />
            ))}
          </span>
        </button>
      ) : null}
    </div>
  );
}
