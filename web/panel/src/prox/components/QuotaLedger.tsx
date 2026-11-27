/**
 * Ledger view of the quota page: one row per credential, grouped by provider,
 * with the provider's pool and next reset in the group header.
 */

import { Fragment, useState, type ReactNode } from 'react';
import { IconChevronDown } from '@/components/ui/icons';
import type { QuotaFileEntry } from '@/features/quota/logic';
import { providerLabel, when } from '../format';
import type { PoolChart } from '../types';
import styles from './QuotaLedger.module.scss';

export function QuotaLedger({
  entries,
  charts,
  renderRow,
}: {
  entries: QuotaFileEntry[];
  charts: PoolChart[];
  renderRow: (entry: QuotaFileEntry, index: number) => ReactNode;
}) {
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(() => new Set());
  // Group in first-seen order so a "soonest recovery" sort keeps its order inside each group.
  const groups: { type: string; items: { entry: QuotaFileEntry; index: number }[] }[] = [];
  entries.forEach((entry, index) => {
    let group = groups.find((g) => g.type === entry.type);
    if (!group) {
      group = { type: entry.type, items: [] };
      groups.push(group);
    }
    group.items.push({ entry, index });
  });

  const toggle = (type: string) =>
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (!next.delete(type)) next.add(type);
      return next;
    });

  return (
    <div className={styles.ledger}>
      {groups.map((group) => {
        const chart = charts.find((c) => c.provider === group.type);
        const open = !collapsed.has(group.type);
        return (
          <section key={group.type} className={styles.group}>
            <header className={styles.groupHead}>
              <span className={styles.groupName}>{providerLabel(group.type)}</span>
              <span className={styles.groupCount}>{group.items.length}</span>
              {chart && (
                <>
                  <span className={styles.groupPool}>
                    <strong>{Math.round(chart.current_percent)}%</strong> of{' '}
                    {Math.round(chart.capacity_percent)}% pool
                  </span>
                  {chart.next_reset && (
                    <span className={styles.groupReset}>
                      Next reset +{Math.round(chart.next_reset.gain)}% · {when(chart.next_reset.at)}
                    </span>
                  )}
                </>
              )}
              <button
                type="button"
                className={styles.collapse}
                aria-expanded={open}
                onClick={() => toggle(group.type)}
              >
                {open ? 'Collapse' : 'Expand'}
                <IconChevronDown size={14} />
              </button>
            </header>
            {open && (
              <div className={styles.rows}>
                {group.items.map(({ entry, index }) => (
                  <Fragment key={`${entry.type}:${index}`}>{renderRow(entry, index)}</Fragment>
                ))}
              </div>
            )}
          </section>
        );
      })}
    </div>
  );
}
