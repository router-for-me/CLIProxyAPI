/**
 * The quota runway: the next seven days of weekly resets, one lane per provider.
 * Each account is a capsule placed at its reset and filled by what it has left,
 * so "spend the leftmost account with quota first" is visible at a glance.
 */

import type { CSSProperties } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import type { ResolvedTheme } from '@/types';
import {
  getAuthFileIcon,
  getThemeSurfaceIconBackground,
  getTypeLabel,
  isThemeSurfaceIconProvider,
} from '@/features/authFiles/constants';
import { buildResetDisplay } from '@/utils/quota';
import {
  QUOTA_PROGRESS_HIGH_THRESHOLD,
  QUOTA_PROGRESS_MEDIUM_THRESHOLD,
} from '../components/QuotaMeter';
import {
  maskCredentialName,
  weeklyRemaining,
  type LedgerRow,
  type LedgerSummary,
} from './ledgerModel';
import { RUNWAY_SPAN_MS, placeRunwayMarkers, runwayDayTicks } from './runwayModel';

const DAY_FRACTION = (24 * 3_600_000) / RUNWAY_SPAN_MS;
import styles from './QuotaRunway.module.scss';

export interface RunwayLane {
  provider: string;
  rows: LedgerRow[];
  summary: LedgerSummary;
}

export interface QuotaRunwayProps {
  lanes: RunwayLane[];
  now: number;
  showEmails: boolean;
  resolvedTheme: ResolvedTheme;
}

const toneClass = (remaining: number | null) =>
  remaining === null
    ? styles.toneUnknown
    : remaining >= QUOTA_PROGRESS_HIGH_THRESHOLD
      ? styles.toneHigh
      : remaining >= QUOTA_PROGRESS_MEDIUM_THRESHOLD
        ? styles.toneMedium
        : styles.toneLow;

function Marker({
  row,
  now,
  showEmails,
  style,
  t,
}: {
  row: LedgerRow;
  now: number;
  showEmails: boolean;
  style?: CSSProperties;
  t: TFunction;
}) {
  const { i18n } = useTranslation();
  const remaining = weeklyRemaining(row);
  const name = showEmails ? row.entry.file.name : maskCredentialName(row.entry.file.name);
  const reset = buildResetDisplay(null, row.weeklyResetAtMs, now, i18n.resolvedLanguage);
  const percent = remaining === null ? '--' : Math.round(remaining);
  const when = reset?.relative ?? reset?.absolute;
  const label = when
    ? t('ledger.runway_marker', { name, percent, when })
    : t('ledger.runway_marker_unseen', { name, percent });
  return (
    <span
      role="img"
      aria-label={label}
      title={label}
      className={`${styles.marker} ${row.nextUp ? styles.markerNext : ''}`}
      style={style}
    >
      <span
        className={`${styles.markerFill} ${toneClass(remaining)}`}
        style={{ height: `${remaining ?? 0}%` }}
      />
    </span>
  );
}

function LaneLabel({ lane, theme, t }: { lane: RunwayLane; theme: ResolvedTheme; t: TFunction }) {
  const { summary } = lane;
  const icon = getAuthFileIcon(lane.provider, theme);
  return (
    <div className={styles.laneLabel}>
      <span className={styles.laneName}>
        <span
          className={styles.icon}
          style={
            isThemeSurfaceIconProvider(lane.provider)
              ? { background: getThemeSurfaceIconBackground(theme) }
              : undefined
          }
        >
          {icon && <img src={icon} alt="" />}
        </span>
        {getTypeLabel(t, lane.provider)}
      </span>
      <span className={styles.laneTotal}>
        <strong>
          {summary.remainingTotal === null ? '--' : `${Math.round(summary.remainingTotal)}%`}
        </strong>{' '}
        {t('ledger.runway_left_of', {
          capacity: summary.capacity || summary.credentialCount * 100,
        })}
      </span>
      {summary.primaryLabelKey && (
        <span className={styles.laneWindow}>{t(summary.primaryLabelKey)}</span>
      )}
    </div>
  );
}

export function QuotaRunway({ lanes, now, showEmails, resolvedTheme }: QuotaRunwayProps) {
  const { t, i18n } = useTranslation();
  const ticks = runwayDayTicks(now);
  const dayFormat = new Intl.DateTimeFormat(i18n.resolvedLanguage, { weekday: 'short' });
  const placedLanes = lanes.map((lane) => {
    const markers = placeRunwayMarkers(lane.rows, now);
    const placed = markers.filter((marker) => marker.x !== null);
    return {
      lane,
      placed,
      unseen: markers.filter((marker) => marker.x === null),
      levels: placed.reduce((max, marker) => Math.max(max, marker.level + 1), 1),
    };
  });
  const hasUnseen = placedLanes.some((entry) => entry.unseen.length > 0);

  return (
    <section className={styles.runway} aria-labelledby="quota-runway-title">
      <div className={styles.intro}>
        <h2 id="quota-runway-title" className={styles.title}>
          {t('ledger.runway_title')}
        </h2>
        <p className={styles.caption}>{t('ledger.runway_caption')}</p>
      </div>

      <div className={`${styles.chart} ${hasUnseen ? styles.chartWithGutter : ''}`}>
        <div className={styles.axis} aria-hidden="true">
          <span className={styles.now} style={{ left: 0 }}>
            {t('ledger.runway_now')}
          </span>
          {/* Each day is labelled in the middle of its span, between two midnights. */}
          {ticks
            .filter((tick) => tick.x + DAY_FRACTION / 2 < 1 && tick.x + DAY_FRACTION / 2 > 0.06)
            .map((tick) => (
              <span
                key={tick.atMs}
                className={styles.tick}
                style={{ left: `${(tick.x + DAY_FRACTION / 2) * 100}%` }}
              >
                {dayFormat.format(tick.atMs)}
              </span>
            ))}
        </div>
        {hasUnseen && (
          <div className={styles.gutterHead} aria-hidden="true">
            {t('ledger.runway_unseen')}
          </div>
        )}

        {placedLanes.map(({ lane, placed, unseen, levels }) => {
          return (
            <div key={lane.provider} className={styles.lane}>
              <LaneLabel lane={lane} theme={resolvedTheme} t={t} />
              <div
                className={styles.track}
                style={{ '--levels': levels } as CSSProperties}
                role="list"
                aria-label={getTypeLabel(t, lane.provider)}
              >
                {ticks.map((tick) => (
                  <span
                    key={tick.atMs}
                    className={styles.gridline}
                    style={{ left: `${tick.x * 100}%` }}
                    aria-hidden="true"
                  />
                ))}
                {placed.map((marker) => (
                  <span key={marker.row.key} role="listitem">
                    <Marker
                      row={marker.row}
                      now={now}
                      showEmails={showEmails}
                      t={t}
                      style={{
                        left: `${(marker.x as number) * 100}%`,
                        top: `calc(${marker.level} * var(--level-height))`,
                      }}
                    />
                  </span>
                ))}
              </div>
              {hasUnseen && (
                <div className={styles.gutter} role="list">
                  {unseen.map((marker) => (
                    <span key={marker.row.key} role="listitem">
                      <Marker row={marker.row} now={now} showEmails={showEmails} t={t} />
                    </span>
                  ))}
                </div>
              )}
            </div>
          );
        })}
      </div>
    </section>
  );
}
