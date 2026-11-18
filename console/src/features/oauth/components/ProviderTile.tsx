import { useId, useState, type CSSProperties } from 'react';
import { IconPlus } from '@/components/ui/icons';
import type { ProviderFlowState } from '../hooks/useOAuthFlows';
import { BrandGlyph } from './BrandGlyph';
import styles from './ProviderTile.module.scss';

/** 首屏磁贴级联：起点让位给头部（140ms），级差 30ms，整组封顶 360ms。 */
const TILE_BASE_DELAY_MS = 140;
const TILE_STAGGER_MS = 30;
const TILE_MAX_DELAY_MS = 360;

export interface ProviderTileProps {
  label: string;
  caption: string;
  icon: string;
  index: number;
  status?: ProviderFlowState['status'];
  statusLabel?: string;
  onOpen: () => void;
}

/**
 * 提供商磁贴：整块可点（stretched button），点击即开始登录并打开授权对话框。
 * 副行平时说明登录方式，登录进行中改为实时状态 —— 对话框关着也能看到后台进度。
 * 赞助商不走磁贴，见 SponsorCard。
 */
export function ProviderTile({
  label,
  caption,
  icon,
  index,
  status,
  statusLabel,
  onOpen,
}: ProviderTileProps) {
  const captionId = useId();
  // 挂载时捕获延迟：之后的重渲染（状态轮询）不重播入场
  const [enterDelay] = useState(
    () => `${Math.min(TILE_BASE_DELAY_MS + index * TILE_STAGGER_MS, TILE_MAX_DELAY_MS)}ms`
  );

  return (
    <div
      className={styles.tile}
      data-status={status ?? 'idle'}
      style={{ '--tile-delay': enterDelay } as CSSProperties}
    >
      <span className={styles.glyph}>
        <BrandGlyph src={icon} size={20} className={styles.glyphImage} />
      </span>
      <span className={styles.text}>
        <button
          type="button"
          className={styles.main}
          onClick={onOpen}
          aria-haspopup="dialog"
          aria-describedby={captionId}
        >
          {label}
        </button>
        <span className={styles.caption} id={captionId}>
          {status && statusLabel ? (
            <span className={styles.status}>
              <span className={styles.statusDot} aria-hidden="true" />
              {statusLabel}
            </span>
          ) : (
            <span className={styles.captionText}>{caption}</span>
          )}
        </span>
      </span>
      <span className={styles.trail} aria-hidden="true">
        <IconPlus size={15} />
      </span>
    </div>
  );
}
