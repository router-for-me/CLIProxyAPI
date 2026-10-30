import { useMemo, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import { Select } from '@/components/ui/Select';
import { number } from './hooks';
import { getPagination } from '@/utils/pagination';
import type { Summary } from './types';
import styles from './Sharing.module.scss';

export function PageHeader({
  title,
  hint,
  actions,
}: {
  title: string;
  hint?: string;
  actions?: ReactNode;
}) {
  return (
    <header className={styles.header}>
      <div>
        <h1>{title}</h1>
        {hint && <p>{hint}</p>}
      </div>
      <div className={styles.actions}>{actions}</div>
    </header>
  );
}

export function LoadState({ loading, error }: { loading: boolean; error: string }) {
  if (error)
    return (
      <div className="error-box" role="alert">
        {error}
      </div>
    );
  return loading ? <LoadingSpinner /> : null;
}

export function Pager({
  page,
  total,
  pageSize = 50,
  onPage,
  onPageSize,
  loading = false,
}: {
  page: number;
  total: number;
  pageSize?: number;
  onPage: (page: number) => void;
  onPageSize?: (size: number) => void;
  loading?: boolean;
}) {
  const { t } = useTranslation();
  const { totalPages } = getPagination(total, page, pageSize);
  const label = t('sharing.pagination', { page, total, totalPages });
  return (
    <nav className={styles.pager} aria-label={label} aria-busy={loading}>
      <Button
        variant="secondary"
        size="sm"
        disabled={loading || page <= 1}
        onClick={() => onPage(Math.max(1, page - 1))}
      >
        {t('auth_files.pagination_prev')}
      </Button>
      <span role="status" aria-live="polite" aria-atomic="true">
        {loading ? t('common.loading') : label}
      </span>
      <Button
        variant="secondary"
        size="sm"
        disabled={loading || page >= totalPages}
        onClick={() => onPage(Math.min(totalPages, page + 1))}
      >
        {t('auth_files.pagination_next')}
      </Button>
      {onPageSize && (
        <span className={styles.pageSize}>
          {t('sharing.page_size')}
          <Select
            value={String(pageSize)}
            onChange={(value) => onPageSize(Number(value))}
            ariaLabel={t('sharing.page_size')}
            size="sm"
            disabled={loading}
            options={[...new Set([pageSize, 50, 100, 200])]
              .sort((a, b) => a - b)
              .map((value) => ({ value: String(value), label: String(value) }))}
          />
        </span>
      )}
    </nav>
  );
}

/** Daily token bars aggregated from summary groups; maxDays keeps the latest N days. */
export function DailyTrend({ groups, maxDays }: { groups: Summary[]; maxDays?: number }) {
  const { t } = useTranslation();
  const days = useMemo(() => {
    const byDay = new Map<string, { day: string; tokens: number; calls: number }>();
    groups.forEach((group) => {
      if (!group.day) return;
      const entry = byDay.get(group.day) || { day: group.day, tokens: 0, calls: 0 };
      entry.tokens += group.total_tokens;
      entry.calls += group.calls;
      byDay.set(group.day, entry);
    });
    const all = [...byDay.values()].sort((a, b) => a.day.localeCompare(b.day));
    return maxDays && all.length > maxDays ? all.slice(-maxDays) : all;
  }, [groups, maxDays]);
  if (!days.length) return <p className={styles.chartEmpty}>{t('sharing.trend_empty')}</p>;
  const max = Math.max(...days.map((entry) => entry.tokens), 1);
  const mid = days.length > 2 ? days[Math.floor(days.length / 2)] : null;
  return (
    <div className={styles.dayChart}>
      <div className={styles.dayBars}>
        {days.map((entry, index) => (
          <div
            key={entry.day}
            className={styles.dayBar}
            title={t('sharing.trend_point', {
              date: entry.day,
              calls: number(entry.calls),
              tokens: number(entry.tokens),
            })}
          >
            <div
              className={styles.dayBarFill}
              style={{
                height: `${Math.max(2, (entry.tokens / max) * 100)}%`,
                animationDelay: `${Math.min(index * 12, 400)}ms`,
              }}
            />
          </div>
        ))}
      </div>
      <div className={styles.chartAxis}>
        <span>{days[0].day.slice(5)}</span>
        {mid && <span>{mid.day.slice(5)}</span>}
        {days.length > 1 && <span>{days[days.length - 1].day.slice(5)}</span>}
      </div>
    </div>
  );
}
