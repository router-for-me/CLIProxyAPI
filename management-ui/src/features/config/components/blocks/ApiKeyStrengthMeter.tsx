import { memo, useEffect, useMemo, useRef, type CSSProperties } from 'react';
import { useTranslation } from 'react-i18next';
import {
  API_KEY_STRENGTH_SEGMENTS,
  evaluateApiKeyStrength,
  type ApiKeyStrengthTier,
} from '@/utils/apiKeyStrength';
import { segmentFillDelayMs } from './shared';
import styles from './Blocks.module.scss';

// 三档语义色 + 段数承担第四档的区分：翡翠绿留给「活的流量」，此处用语义 success。
const TIER_COLORS: Record<ApiKeyStrengthTier, string> = {
  weak: 'var(--error-color)',
  fair: 'var(--amber-color)',
  good: 'var(--success-color)',
  strong: 'var(--success-color)',
};

const SEGMENT_INDEXES = Array.from({ length: API_KEY_STRENGTH_SEGMENTS }, (_, index) => index);

/**
 * 自拟 API Key 的强度参考条：四段依次点亮，只给档位标签，不参与保存校验。
 */
export const ApiKeyStrengthMeter = memo(function ApiKeyStrengthMeter({ value }: { value: string }) {
  const { t } = useTranslation();
  const { tier, segments } = useMemo(() => evaluateApiKeyStrength(value), [value]);

  // 上一次的段数决定这次谁需要排队；渲染只读，提交后再推进
  const previousSegments = useRef(segments);
  const cascadeFrom = previousSegments.current;
  useEffect(() => {
    previousSegments.current = segments;
  }, [segments]);

  const empty = segments === 0;
  const tierLabel = empty
    ? t('config_management.visual.api_keys.strength.empty')
    : t(`config_management.visual.api_keys.strength.${tier}`);

  return (
    <div
      className={styles.strengthMeter}
      style={
        {
          '--strength-color': empty ? 'var(--text-quaternary)' : TIER_COLORS[tier],
        } as CSSProperties
      }
    >
      <div
        className={styles.strengthTrack}
        role="progressbar"
        aria-valuemin={0}
        aria-valuemax={API_KEY_STRENGTH_SEGMENTS}
        aria-valuenow={segments}
        aria-valuetext={tierLabel}
        aria-label={t('config_management.visual.api_keys.strength.label')}
      >
        {SEGMENT_INDEXES.map((index) => (
          <span key={index} className={styles.strengthSegment}>
            <span
              className={styles.strengthSegmentFill}
              data-filled={index < segments}
              style={
                {
                  '--segment-delay': `${segmentFillDelayMs(index, segments, cascadeFrom)}ms`,
                } as CSSProperties
              }
            />
          </span>
        ))}
      </div>
      <span className={styles.strengthLabel} aria-hidden="true">
        {empty ? '—' : tierLabel}
      </span>
    </div>
  );
});
