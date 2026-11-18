import { useId } from 'react';
import { useTranslation } from 'react-i18next';
import { IconExternalLink, IconTimer } from '@/components/ui/icons';
import {
  APIKEY_FUN_DISPLAY_NAME,
  APIKEY_FUN_DOUBLE11_BONUS_PERCENT,
  APIKEY_FUN_DOUBLE11_URL,
  isApiKeyFunDouble11Active,
} from '../sponsor';
import styles from './SponsorCampaignCard.module.scss';

/** 三团光池：与 OAuth 赞助卡同一套「无中心流体」光效，换成节日红 / 橙 / 金。 */
const LIGHT_POOLS = ['a', 'b', 'c'] as const;

interface SponsorCampaignCardProps {
  /** 便于测试注入当前时间；默认取 Date.now()。 */
  now?: number;
}

/**
 * APIKEY.FUN 双 11 充值赠送活动卡。活动结束后自动不渲染。
 * 整张卡只有一个交互目标（「立即参与」），宣传信息全部是静态文本。
 */
export function SponsorCampaignCard({ now }: SponsorCampaignCardProps) {
  const { t } = useTranslation();
  const titleId = useId();

  if (!isApiKeyFunDouble11Active(now)) return null;

  const percent = APIKEY_FUN_DOUBLE11_BONUS_PERCENT;
  const pools = LIGHT_POOLS.map((pool) => (
    <span key={pool} className={styles.pool} data-pool={pool} />
  ));

  return (
    <section className={styles.card} aria-labelledby={titleId} data-campaign="double11">
      <div className={styles.field} aria-hidden="true">
        {pools}
      </div>
      <div className={styles.rim} aria-hidden="true">
        {pools}
      </div>

      <div className={styles.body}>
        <span className={styles.badge}>
          <IconTimer size={13} aria-hidden="true" />
          {t('providersPage.sponsor.double11.badge')}
        </span>
        <h2 id={titleId} className={styles.title}>
          {t('providersPage.sponsor.double11.title', { provider: APIKEY_FUN_DISPLAY_NAME })}
        </h2>
        <p className={styles.caption}>
          {t('providersPage.sponsor.double11.description', {
            provider: APIKEY_FUN_DISPLAY_NAME,
            percent,
          })}
        </p>
      </div>

      <div className={styles.offer}>
        <span className={styles.watermark} aria-hidden="true">
          11.11
        </span>
        <p className={styles.figure}>
          <span className={styles.figureLabel}>{t('providersPage.sponsor.double11.bonus')}</span>
          <span className={styles.figureValue}>
            <span className={styles.figureSign}>+</span>
            {percent}
            <span className={styles.figureUnit}>%</span>
          </span>
        </p>
        <a
          className={styles.cta}
          href={APIKEY_FUN_DOUBLE11_URL}
          target="_blank"
          rel="noopener noreferrer"
          aria-label={t('providersPage.sponsor.double11.ctaAria', {
            provider: APIKEY_FUN_DISPLAY_NAME,
          })}
        >
          {t('providersPage.sponsor.double11.cta')}
          <IconExternalLink size={14} aria-hidden="true" className={styles.ctaIcon} />
        </a>
      </div>
    </section>
  );
}
