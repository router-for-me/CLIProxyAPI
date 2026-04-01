import { useState, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { formatCompactNumber, formatUsd, type ApiStats } from '@/utils/usage';
import styles from '@/pages/UsagePage.module.scss';

export interface ApiDetailsCardProps {
  apiStats: ApiStats[];
  loading: boolean;
  hasPrices: boolean;
}

type ApiSortKey = 'endpoint' | 'requests' | 'tokens' | 'cost';
type SortDir = 'asc' | 'desc';

export function ApiDetailsCard({ apiStats, loading, hasPrices }: ApiDetailsCardProps) {
  const { t } = useTranslation();
  const [expandedApis, setExpandedApis] = useState<Set<string>>(new Set());
  const [sortKey, setSortKey] = useState<ApiSortKey>('requests');
  const [sortDir, setSortDir] = useState<SortDir>('desc');

  const toggleExpand = (endpoint: string) => {
    setExpandedApis((prev) => {
      const newSet = new Set(prev);
      if (newSet.has(endpoint)) {
        newSet.delete(endpoint);
      } else {
        newSet.add(endpoint);
      }
      return newSet;
    });
  };

  const handleSort = (key: ApiSortKey) => {
    if (sortKey === key) {
      setSortDir((d) => (d === 'asc' ? 'desc' : 'asc'));
    } else {
      setSortKey(key);
      setSortDir(key === 'endpoint' ? 'asc' : 'desc');
    }
  };

  const sorted = useMemo(() => {
    const list = [...apiStats];
    const dir = sortDir === 'asc' ? 1 : -1;
    list.sort((a, b) => {
      switch (sortKey) {
        case 'endpoint': return dir * a.endpoint.localeCompare(b.endpoint);
        case 'requests': return dir * (a.totalRequests - b.totalRequests);
        case 'tokens': return dir * (a.totalTokens - b.totalTokens);
        case 'cost': return dir * (a.totalCost - b.totalCost);
        default: return 0;
      }
    });
    return list;
  }, [apiStats, sortKey, sortDir]);

  const arrow = (key: ApiSortKey) =>
    sortKey === key ? (sortDir === 'asc' ? ' ▲' : ' ▼') : '';

  return (
    <Card title={t('usage_stats.api_details')} className={styles.detailsFixedCard}>
      {loading ? (
        <div className={styles.hint}>{t('common.loading')}</div>
      ) : sorted.length > 0 ? (
        <>
          <div className={styles.apiSortBar}>
            {([
              ['endpoint', 'usage_stats.api_endpoint'],
              ['requests', 'usage_stats.requests_count'],
              ['tokens', 'usage_stats.tokens_count'],
              ...(hasPrices ? [['cost', 'usage_stats.total_cost']] : []),
            ] as [ApiSortKey, string][]).map(([key, labelKey]) => (
              <button
                key={key}
                type="button"
                aria-pressed={sortKey === key}
                className={`${styles.apiSortBtn} ${sortKey === key ? styles.apiSortBtnActive : ''}`}
                onClick={() => handleSort(key)}
              >
                {t(labelKey)}{arrow(key)}
              </button>
            ))}
          </div>
          <div className={styles.detailsScroll}>
            <div className={styles.apiList}>
              {sorted.map((api, index) => {
                const isExpanded = expandedApis.has(api.endpoint);
                const panelId = `api-models-${index}`;

                return (
                  <div key={api.endpoint} className={styles.apiItem}>
                    <button
                      type="button"
                      className={styles.apiHeader}
                      onClick={() => toggleExpand(api.endpoint)}
                      aria-expanded={isExpanded}
                      aria-controls={panelId}
                    >
                      <div className={styles.apiInfo}>
                        <span className={styles.apiEndpoint}>{api.endpoint}</span>
                        <div className={styles.apiStats}>
                          <span className={styles.apiBadge}>
                            <span className={styles.requestCountCell}>
                              <span>
                                {t('usage_stats.requests_count')}: {api.totalRequests.toLocaleString()}
                              </span>
                              <span className={styles.requestBreakdown}>
                                (<span className={styles.statSuccess}>{api.successCount.toLocaleString()}</span>{' '}
                                <span className={styles.statFailure}>{api.failureCount.toLocaleString()}</span>)
                              </span>
                            </span>
                          </span>
                          <span className={styles.apiBadge}>
                            {t('usage_stats.tokens_count')}: {formatCompactNumber(api.totalTokens)}
                          </span>
                          {hasPrices && api.totalCost > 0 && (
                            <span className={styles.apiBadge}>
                              {t('usage_stats.total_cost')}: {formatUsd(api.totalCost)}
                            </span>
                          )}
                        </div>
                      </div>
                      <span className={styles.expandIcon}>
                        {isExpanded ? '▼' : '▶'}
                      </span>
                    </button>
                    {isExpanded && (
                      <div id={panelId} className={styles.apiModels}>
                        {Object.entries(api.models).map(([model, stats]) => (
                          <div key={model} className={styles.modelRow}>
                            <span className={styles.modelName}>{model}</span>
                            <span className={styles.modelStat}>
                              <span className={styles.requestCountCell}>
                                <span>{stats.requests.toLocaleString()}</span>
                                <span className={styles.requestBreakdown}>
                                  (<span className={styles.statSuccess}>{stats.successCount.toLocaleString()}</span>{' '}
                                  <span className={styles.statFailure}>{stats.failureCount.toLocaleString()}</span>)
                                </span>
                              </span>
                            </span>
                            <span className={styles.modelStat}>{formatCompactNumber(stats.tokens)}</span>
                          </div>
                        ))}
                      </div>
                    )}
                  </div>
                );
              })}
            </div>
          </div>
        </>
      ) : (
        <div className={styles.hint}>{t('usage_stats.no_data')}</div>
      )}
    </Card>
  );
}
