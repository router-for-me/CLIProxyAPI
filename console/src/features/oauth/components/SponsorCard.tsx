import { useId, type CSSProperties } from 'react';
import { IconExternalLink, IconPlus } from '@/components/ui/icons';
import type { ProviderFlowState } from '../hooks/useOAuthFlows';
import { BrandGlyph } from './BrandGlyph';
import styles from './SponsorCard.module.scss';

/** 与磁贴级联同一起点：让位给页头 140ms。 */
const CARD_ENTER_DELAY = '140ms';

/** 三团光池：各自独立漂移，没有共同的旋转中心。 */
const LIGHT_POOLS = ['a', 'b', 'c'] as const;

export interface SponsorCardSite {
  id: string;
  /** 完整站点名（如「Kimi 国内站」），作为登录按钮的可访问名称。 */
  name: string;
  /** 卡内可见的站点短名（如「国内站」），是 name 的子串，满足 label-in-name。 */
  region: string;
  domain: string;
  status?: ProviderFlowState['status'];
  statusLabel?: string;
  signUpUrl: string;
}

export interface SponsorCardProps {
  title: string;
  caption: string;
  icon: string;
  signUpLabel: string;
  sites: SponsorCardSite[];
  /** 点站点入口：开始该站登录并打开授权对话框。 */
  onOpen: (siteId: string) => void;
}

/**
 * 赞助商品牌卡：同一品牌的多个站点合并成一张卡，每站一个登录入口 + 开放平台注册。
 *
 * 光效是「无中心流体」：三团品牌蓝光池在卡内沿各自的 Lissajous 轨迹漂移（x / y / 形变
 * 三条动画周期互不整除），同一组光池同时渲染在两层上 ——
 * 内层是低透明度的光晕底（边缘略强），外层被遮罩成 1px 描边。光池漂到哪，哪段边框就被照亮，
 * 于是边框光会自然地出现、拉长、汇合、消散，而不是绕着一个圆心扫圈。
 */
export function SponsorCard({
  title,
  caption,
  icon,
  signUpLabel,
  sites,
  onOpen,
}: SponsorCardProps) {
  const titleId = useId();
  const pools = LIGHT_POOLS.map((pool) => (
    <span key={pool} className={styles.pool} data-pool={pool} />
  ));

  return (
    <div
      className={styles.card}
      role="group"
      aria-labelledby={titleId}
      style={{ '--card-delay': CARD_ENTER_DELAY } as CSSProperties}
    >
      <div className={styles.field} aria-hidden="true">
        {pools}
      </div>
      <div className={styles.rim} aria-hidden="true">
        {pools}
      </div>

      <div className={styles.head}>
        <BrandGlyph src={icon} size={40} className={styles.glyph} />
        <div className={styles.headText}>
          <p className={styles.title} id={titleId}>
            {title}
          </p>
          <p className={styles.caption}>{caption}</p>
        </div>
      </div>

      <div className={styles.sites}>
        {sites.map((site) => (
          <SiteEntry
            key={site.id}
            site={site}
            signUpLabel={signUpLabel}
            onOpen={() => onOpen(site.id)}
          />
        ))}
      </div>
    </div>
  );
}

function SiteEntry({
  site,
  signUpLabel,
  onOpen,
}: {
  site: SponsorCardSite;
  signUpLabel: string;
  onOpen: () => void;
}) {
  const metaId = useId();

  return (
    <div className={styles.site} data-status={site.status ?? 'idle'}>
      <div className={styles.siteBody}>
        <span className={styles.siteText}>
          <button
            type="button"
            className={styles.siteMain}
            onClick={onOpen}
            aria-label={site.name}
            aria-haspopup="dialog"
            aria-describedby={metaId}
          >
            {site.region}
          </button>
          <span className={styles.siteMeta} id={metaId}>
            {site.status && site.statusLabel ? (
              <span className={styles.status}>
                <span className={styles.statusDot} aria-hidden="true" />
                {site.statusLabel}
              </span>
            ) : (
              <span className={styles.domain}>{site.domain}</span>
            )}
          </span>
        </span>
        <a
          className={styles.signUp}
          href={site.signUpUrl}
          target="_blank"
          rel="noopener noreferrer"
          aria-label={`${signUpLabel} · ${site.name}`}
        >
          {signUpLabel}
          <IconExternalLink size={12} aria-hidden="true" />
        </a>
        <span className={styles.trail} aria-hidden="true">
          <IconPlus size={15} />
        </span>
      </div>
    </div>
  );
}
