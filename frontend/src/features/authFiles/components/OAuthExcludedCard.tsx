import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import styles from '@/pages/AuthFilesPage.module.scss';

type UnsupportedError = 'unsupported' | null;

export type OAuthExcludedCardProps = {
  disableControls: boolean;
  excludedError: UnsupportedError;
  excluded: Record<string, string[]>;
  onAdd: () => void;
  onEdit: (provider: string) => void;
  onDelete: (provider: string) => void;
};

export function OAuthExcludedCard(props: OAuthExcludedCardProps) {
  const { t } = useTranslation();
  const { disableControls, excludedError, excluded, onAdd, onEdit, onDelete } = props;

  return (
    <Card
      title={t('oauth_excluded.title')}
      extra={
        <Button size="sm" onClick={onAdd} disabled={disableControls || excludedError === 'unsupported'}>
          {t('oauth_excluded.add')}
        </Button>
      }
    >
      {excludedError === 'unsupported' ? (
        <EmptyState
          title={t('oauth_excluded.upgrade_required_title')}
          description={t('oauth_excluded.upgrade_required_desc')}
        />
      ) : Object.keys(excluded).length === 0 ? (
        <EmptyState title={t('oauth_excluded.list_empty_all')} />
      ) : (
        <div className={styles.excludedList}>
          {Object.entries(excluded).map(([provider, models]) => (
            <div key={provider} className={styles.excludedItem}>
              <div className={styles.excludedInfo}>
                <div className={styles.excludedProvider}>{provider}</div>
                <div className={styles.excludedModels}>
                  {models?.length
                    ? t('oauth_excluded.model_count', { count: models.length })
                    : t('oauth_excluded.no_models')}
                </div>
              </div>
              <div className={styles.excludedActions}>
                <Button variant="secondary" size="sm" onClick={() => onEdit(provider)}>
                  {t('common.edit')}
                </Button>
                <Button variant="danger" size="sm" onClick={() => onDelete(provider)}>
                  {t('oauth_excluded.delete')}
                </Button>
              </div>
            </div>
          ))}
        </div>
      )}
    </Card>
  );
}

