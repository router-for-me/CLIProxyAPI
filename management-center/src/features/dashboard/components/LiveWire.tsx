import { useId, useMemo } from 'react';
import { buildSmoothLinePath, type CurvePoint } from './curve';
import styles from './LiveWire.module.scss';

const VIEW_WIDTH = 100;
const VIEW_HEIGHT = 40;
/** 顶部留白，让峰值和呼吸光点都不被裁切 */
const TOP_PADDING = 6;

interface LiveWireProps {
  points: number[];
  ariaLabel: string;
  className?: string;
}

/**
 * Hero 签名元素：横贯 hero 底部的实时流量脉搏线。
 * 真实桶数据 → 平滑曲线 + 渐变面积，最新一桶的末端带呼吸光点；
 * 无流量时退化为一条安静的虚线基线。
 */
export function LiveWire({ points, ariaLabel, className }: LiveWireProps) {
  const gradientId = useId();

  const geometry = useMemo(() => {
    const values = points.filter((value) => Number.isFinite(value));
    if (values.length < 2) return null;

    const max = Math.max(...values);
    const usableHeight = VIEW_HEIGHT - TOP_PADDING;
    const stepX = VIEW_WIDTH / (values.length - 1);

    const coordinates: CurvePoint[] = values.map((value, index) => ({
      x: index * stepX,
      y: VIEW_HEIGHT - (max > 0 ? value / max : 0) * usableHeight,
    }));

    const line = buildSmoothLinePath(coordinates, TOP_PADDING, VIEW_HEIGHT);
    const last = coordinates[coordinates.length - 1];
    const area = `${line} L${VIEW_WIDTH} ${VIEW_HEIGHT} L0 ${VIEW_HEIGHT} Z`;

    return {
      line,
      area,
      isFlat: max <= 0,
      lastYRatio: last.y / VIEW_HEIGHT,
    };
  }, [points]);

  const idle = !geometry || geometry.isFlat;

  return (
    <div className={[styles.wire, className].filter(Boolean).join(' ')}>
      {idle ? (
        <svg
          className={styles.canvas}
          viewBox={`0 0 ${VIEW_WIDTH} ${VIEW_HEIGHT}`}
          preserveAspectRatio="none"
          role="img"
          aria-label={ariaLabel}
        >
          <path
            className={styles.idleLine}
            d={`M0 ${VIEW_HEIGHT - 1} L${VIEW_WIDTH} ${VIEW_HEIGHT - 1}`}
            pathLength={1}
            vectorEffect="non-scaling-stroke"
          />
        </svg>
      ) : (
        <>
          <svg
            className={styles.canvas}
            viewBox={`0 0 ${VIEW_WIDTH} ${VIEW_HEIGHT}`}
            preserveAspectRatio="none"
            role="img"
            aria-label={ariaLabel}
          >
            <defs>
              <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stopColor="var(--wire-color)" stopOpacity="0.2" />
                <stop offset="100%" stopColor="var(--wire-color)" stopOpacity="0" />
              </linearGradient>
            </defs>
            <path className={styles.area} d={geometry.area} fill={`url(#${gradientId})`} />
            <path
              className={styles.line}
              d={geometry.line}
              pathLength={1}
              fill="none"
              vectorEffect="non-scaling-stroke"
            />
          </svg>
          <span
            className={styles.pulse}
            style={{ top: `${(geometry.lastYRatio * 100).toFixed(2)}%` }}
            aria-hidden="true"
          />
        </>
      )}
    </div>
  );
}
