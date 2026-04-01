import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';

interface ProviderListProps<T> {
  items: T[];
  loading: boolean;
  keyField: (item: T, index: number) => string;
  renderContent: (item: T, index: number) => ReactNode;
  onEdit: (index: number) => void;
  onDelete: (index: number) => void;
  emptyTitle: string;
  emptyDescription: string;
  deleteLabel?: string;
  actionsDisabled?: boolean;
  getRowDisabled?: (item: T, index: number) => boolean;
  renderExtraActions?: (item: T, index: number) => ReactNode;
}

export function ProviderList<T>({
  items,
  loading,
  keyField,
  renderContent,
  onEdit,
  onDelete,
  emptyTitle,
  emptyDescription,
  deleteLabel,
  actionsDisabled = false,
  getRowDisabled,
  renderExtraActions,
}: ProviderListProps<T>) {
  const { t } = useTranslation();

  if (loading && items.length === 0) {
    return <div className="hint">{t('common.loading')}</div>;
  }

  if (!items.length) {
    return <EmptyState title={emptyTitle} description={emptyDescription} />;
  }

  return (
    <div className="item-list">
      {items.map((item, index) => {
        const rowDisabled = getRowDisabled ? getRowDisabled(item, index) : false;
        return (
          <div
            key={keyField(item, index)}
            className="item-row"
            style={rowDisabled ? { opacity: 0.6 } : undefined}
          >
            <div className="item-meta">{renderContent(item, index)}</div>
            <div className="item-actions">
              <Button
                variant="secondary"
                size="sm"
                onClick={() => onEdit(index)}
                disabled={actionsDisabled}
              >
                {t('common.edit')}
              </Button>
              <Button
                variant="danger"
                size="sm"
                onClick={() => onDelete(index)}
                disabled={actionsDisabled}
              >
                {deleteLabel || t('common.delete')}
              </Button>
              {renderExtraActions ? renderExtraActions(item, index) : null}
            </div>
          </div>
        );
      })}
    </div>
  );
}
