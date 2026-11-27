import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { IconChevronLeft, IconExternalLink } from '@/components/ui/icons';
import { getKimiAffiliateUrl } from '../kimi';
import {
  connectionCounts,
  getConnectionFormTarget,
  isQuickFillBrand,
  QUICK_FILL_BRAND_ORDER,
} from '../resourceView';
import { getSponsorProviderDefinition } from '../sponsorDefinitions';
import type { ProviderBrand, ProviderGroup } from '../types';
import { BrandLogo } from './BrandLogo';
import styles from './ProviderPicker.module.scss';

interface ProviderPickerProps {
  groups: ProviderGroup[];
  /** `page` lays the catalogue out wide for first-run onboarding. */
  variant?: 'sheet' | 'page';
  onPick: (brand: ProviderBrand) => void;
}

const CUSTOM_BRANDS: ReadonlySet<ProviderBrand> = new Set(['openaiCompatibility']);

/**
 * "Add provider" catalogue. Quick-connect partners lead as featured cards — every new
 * provider starts here, so this is where partners earn their exposure — followed by the
 * official APIs and the custom OpenAI-compatible entry as quiet tiles.
 */
export function ProviderPicker({ groups, variant = 'sheet', onPick }: ProviderPickerProps) {
  const { t, i18n } = useTranslation();
  const countOf = (brand: ProviderBrand) => {
    const group = groups.find((item) => item.id === brand);
    return group ? connectionCounts(group).total : 0;
  };
  const available = new Set(groups.map((group) => group.id));
  const featured = QUICK_FILL_BRAND_ORDER.filter((brand) => available.has(brand));
  const official = groups
    .map((group) => group.id)
    .filter((brand) => !isQuickFillBrand(brand) && !CUSTOM_BRANDS.has(brand) && brand !== 'kimi');
  const custom = groups.map((group) => group.id).filter((brand) => CUSTOM_BRANDS.has(brand));
  const name = (brand: ProviderBrand) => t(`providersPage.providerNames.${brand}`);
  const connected = (brand: ProviderBrand) => {
    const count = countOf(brand);
    return count > 0 ? (
      <span className={styles.connected}>{t('providersPage.picker.connected', { count })}</span>
    ) : null;
  };

  const actionLabel = (brand: ProviderBrand) => {
    const group = groups.find((item) => item.id === brand);
    return group && getConnectionFormTarget(group).mode === 'edit'
      ? t('providersPage.actions.edit')
      : t('providersPage.picker.connect');
  };

  const registerLink = (href: string | undefined, label: string) =>
    href ? (
      <a className={styles.registerLink} href={href} target="_blank" rel="noreferrer">
        {label}
        <IconExternalLink size={12} aria-hidden="true" />
      </a>
    ) : null;

  const featuredCard = (brand: ProviderBrand, body: ReactNode, href: string | undefined) => (
    <li key={brand} className={styles.feature}>
      <BrandLogo brand={brand} size={40} />
      <div className={styles.featureMain}>
        <div className={styles.featureTitleLine}>
          <h4 className={styles.featureTitle}>{name(brand)}</h4>
          {connected(brand)}
        </div>
        {body}
      </div>
      <div className={styles.featureActions}>
        <button
          type="button"
          className={styles.connectButton}
          onClick={() => onPick(brand)}
          aria-label={`${actionLabel(brand)} ${name(brand)}`}
        >
          {actionLabel(brand)}
        </button>
        {registerLink(href, t('providersPage.sponsor.registerLink'))}
      </div>
    </li>
  );

  const tile = (brand: ProviderBrand) => (
    <li key={brand}>
      <button type="button" className={styles.tile} onClick={() => onPick(brand)}>
        <BrandLogo brand={brand} size={28} />
        <span className={styles.tileName}>{name(brand)}</span>
        {connected(brand)}
        <IconChevronLeft size={14} className={styles.tileChevron} aria-hidden="true" />
      </button>
    </li>
  );

  return (
    <div className={`${styles.picker} ${variant === 'page' ? styles.pickerPage : ''}`}>
      {featured.length > 0 ? (
        <section className={`${styles.section} ${styles.sectionFeatured}`}>
          <h3 className={styles.heading}>
            {t('providersPage.picker.featured')}
            <span className={styles.badge}>{t('providersPage.picker.featuredBadge')}</span>
          </h3>
          <ul className={styles.features}>
            {featured.map((brand) => {
              const definition = getSponsorProviderDefinition(brand);
              return featuredCard(
                brand,
                <>
                  <span className={styles.featureLead}>
                    {t('providersPage.picker.oneKey', {
                      count: definition.protocolLabels.length,
                    })}
                  </span>
                  <span className={styles.protocols}>
                    {definition.protocolLabels.map((label) => (
                      <span key={label} className={styles.protocol}>
                        {t(`providersPage.sponsor.protocols.${label}`)}
                      </span>
                    ))}
                  </span>
                </>,
                definition.affiliateUrl
              );
            })}
          </ul>
        </section>
      ) : null}

      {official.length > 0 || available.has('kimi') ? (
        <section className={styles.section}>
          <h3 className={styles.heading}>{t('providersPage.picker.official')}</h3>
          {available.has('kimi') ? (
            <ul className={styles.features}>
              {featuredCard(
                'kimi',
                null,
                getKimiAffiliateUrl(i18n.resolvedLanguage ?? i18n.language ?? '')
              )}
            </ul>
          ) : null}
          <ul className={styles.tiles}>{official.map(tile)}</ul>
        </section>
      ) : null}

      {custom.length > 0 ? (
        <section className={styles.section}>
          <h3 className={styles.heading}>{t('providersPage.picker.custom')}</h3>
          <ul className={styles.tiles}>{custom.map(tile)}</ul>
        </section>
      ) : null}
    </div>
  );
}
