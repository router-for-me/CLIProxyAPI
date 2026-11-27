import type { CSSProperties } from 'react';
import { PROVIDER_LOGOS } from '../brandLogos';
import type { ProviderBrand } from '../types';
import styles from './BrandLogo.module.scss';

interface BrandLogoProps {
  brand: ProviderBrand;
  /** Rendered tile size in px; the mark is inset inside it. */
  size?: number;
  className?: string;
}

/**
 * Decorative brand mark in a neutral tile. Light/dark artwork is swapped by the
 * document theme in CSS so the mark never flashes the wrong variant on theme changes.
 */
export function BrandLogo({ brand, size = 24, className }: BrandLogoProps) {
  const logo = PROVIDER_LOGOS[brand];
  const tileClass = [
    styles.tile,
    logo.transparent ? styles.tileTransparent : '',
    logo.themeSurface ? styles.tileThemeSurface : '',
    className,
  ]
    .filter(Boolean)
    .join(' ');
  const style = { '--brand-logo-size': `${size}px` } as CSSProperties;

  return (
    <span className={tileClass} style={style} aria-hidden="true">
      <img
        src={logo.src}
        alt=""
        className={[styles.mark, logo.darkSrc ? styles.markLight : ''].filter(Boolean).join(' ')}
      />
      {logo.darkSrc ? (
        <img src={logo.darkSrc} alt="" className={`${styles.mark} ${styles.markDark}`} />
      ) : null}
    </span>
  );
}
