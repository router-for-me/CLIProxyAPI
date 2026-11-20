import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { IconChevronDown, IconPlus } from '@/components/ui/icons';
import { PROVIDER_LOGOS } from '../brandLogos';
import type { ProviderBrand, ProviderGroup } from '../types';
import styles from './ProviderCategoryList.module.scss';

interface ProviderCategoryListProps {
  groups: ProviderGroup[];
  activeBrand: ProviderBrand;
  onSelect: (brand: ProviderBrand) => void;
}

export function ProviderCategoryList({ groups, activeBrand, onSelect }: ProviderCategoryListProps) {
  const { t } = useTranslation();

  const [adding, setAdding] = useState(false);
  // Only providers that are set up are listed; the rest wait behind "Add provider".
  const setUp = groups.filter((g) => g.resources.length > 0);
  const notSetUp = groups.filter((g) => g.resources.length === 0);
  const addOpen = adding || setUp.length === 0 || notSetUp.some((g) => g.id === activeBrand);

  const renderGroups = (items: ProviderGroup[], withCounts: boolean) => (
    <div className={styles.list}>
      {items.map((group) => {
        const active = group.id === activeBrand;
        const total = group.resources.length;
        const activeCount = group.resources.filter((r) => !r.disabled).length;
        const logo = PROVIDER_LOGOS[group.id];
        const itemClass = [
          styles.item,
          active ? styles.active : '',
          group.id === 'kimi' ? styles.itemKimi : '',
        ]
          .filter(Boolean)
          .join(' ');
        const logoClassName = [
          styles.logo,
          logo?.transparent ? styles.logoTransparent : '',
          logo?.themeSurface ? styles.logoThemeSurface : '',
          logo?.darkSrc ? styles.logoThemeLight : '',
          logo?.invertOnDark ? styles.logoInvertOnDark : '',
        ]
          .filter(Boolean)
          .join(' ');
        const darkLogoClassName = [
          styles.logo,
          logo?.transparent ? styles.logoTransparent : '',
          logo?.themeSurface ? styles.logoThemeSurface : '',
          styles.logoThemeDark,
        ]
          .filter(Boolean)
          .join(' ');

        return (
          <button
            key={group.id}
            type="button"
            className={itemClass}
            onClick={() => onSelect(group.id)}
            aria-current={active ? 'page' : undefined}
          >
            <span className={styles.itemLeft}>
              {logo ? (
                <>
                  <img src={logo.src} alt="" aria-hidden="true" className={logoClassName} />
                  {logo.darkSrc ? (
                    <img
                      src={logo.darkSrc}
                      alt=""
                      aria-hidden="true"
                      className={darkLogoClassName}
                    />
                  ) : null}
                </>
              ) : null}
              <span className={styles.itemText}>
                <span className={styles.itemTitle}>
                  {t(`providersPage.providerNames.${group.id}`)}
                </span>
                {withCounts ? (
                  <span className={styles.itemSubtitle}>
                    {t('providersPage.categories.activeCount', {
                      active: activeCount,
                      total,
                    })}
                  </span>
                ) : null}
              </span>
            </span>
            {withCounts ? (
              <span
                className={[
                  styles.badge,
                  total === 0 ? (group.id === 'kimi' ? styles.badgeKimi : styles.badgeAmber) : '',
                ]
                  .filter(Boolean)
                  .join(' ')}
              >
                {total}
              </span>
            ) : null}
          </button>
        );
      })}
    </div>
  );

  return (
    <div className={styles.stack}>
      <aside className={styles.aside}>
        <p className={styles.eyebrow}>{t('providersPage.categories.title')}</p>
        {renderGroups(setUp, true)}
        {notSetUp.length > 0 ? (
          <>
            <button
              type="button"
              className={styles.addToggle}
              aria-expanded={addOpen}
              onClick={() => setAdding(!addOpen)}
            >
              <IconPlus size={16} />
              <span>{t('providersPage.categories.add')}</span>
              <IconChevronDown
                size={16}
                className={`${styles.addChevron} ${addOpen ? styles.addChevronOpen : ''}`}
              />
            </button>
            {addOpen ? renderGroups(notSetUp, false) : null}
          </>
        ) : null}
      </aside>
    </div>
  );
}
