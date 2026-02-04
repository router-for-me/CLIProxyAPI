import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { formatTokensInMillions, formatUsd, type ApiStats } from '@/utils/usage';
import styles from '@/pages/UsagePage.module.scss';

export interface ApiDetailsCardProps {
  apiStats: ApiStats[];
  loading: boolean;
  hasPrices: boolean;
}

export function ApiDetailsCard({ apiStats, loading, hasPrices }: ApiDetailsCardProps) {
  const { t } = useTranslation();
  const [expandedApis, setExpandedApis] = useState<Set<string>>(new Set());

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

  return (
    <Card title={t('usage_stats.api_details')}>
      {loading ? (
        <div className={styles.hint}>{t('common.loading')}</div>
      ) : apiStats.length > 0 ? (
        <div className={styles.apiList}>
          {apiStats.map((api) => (
            <div key={api.endpoint} className={styles.apiItem}>
              <div className={styles.apiHeader} onClick={() => toggleExpand(api.endpoint)}>
                <div className={styles.apiInfo}>
                  <span className={styles.apiEndpoint}>{api.endpoint}</span>
                  <div className={styles.apiStats}>
                    <span className={styles.apiBadge}>
                      {t('usage_stats.requests_count')}: {api.totalRequests}
                    </span>
                    <span className={styles.apiBadge}>
                      Tokens: {formatTokensInMillions(api.totalTokens)}
                    </span>
                    {hasPrices && api.totalCost > 0 && (
                      <span className={styles.apiBadge}>
                        {t('usage_stats.total_cost')}: {formatUsd(api.totalCost)}
                      </span>
                    )}
                  </div>
                </div>
                <span className={styles.expandIcon}>
                  {expandedApis.has(api.endpoint) ? '▼' : '▶'}
                </span>
              </div>
              {expandedApis.has(api.endpoint) && (
                <div className={styles.apiModels}>
                  {Object.entries(api.models).map(([model, stats]) => (
                    <div key={model} className={styles.modelRow}>
                      <span className={styles.modelName}>{model}</span>
                      <span className={styles.modelStat}>
                        {stats.requests} {t('usage_stats.requests_count')}
                      </span>
                      <span className={styles.modelStat}>{formatTokensInMillions(stats.tokens)}</span>
                    </div>
                  ))}
                </div>
              )}
            </div>
          ))}
        </div>
      ) : (
        <div className={styles.hint}>{t('usage_stats.no_data')}</div>
      )}
    </Card>
  );
}
