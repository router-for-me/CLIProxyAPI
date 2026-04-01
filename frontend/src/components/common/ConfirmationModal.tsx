import { useTranslation } from 'react-i18next';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import { useNotificationStore } from '@/stores';

export function ConfirmationModal() {
  const { t } = useTranslation();
  const confirmation = useNotificationStore((state) => state.confirmation);
  const hideConfirmation = useNotificationStore((state) => state.hideConfirmation);
  const setConfirmationLoading = useNotificationStore((state) => state.setConfirmationLoading);

  const { isOpen, isLoading, options } = confirmation;

  if (!isOpen || !options) {
    return null;
  }

  const { title, message, onConfirm, onCancel, confirmText, cancelText, variant = 'primary' } = options;

  const handleConfirm = async () => {
    try {
      setConfirmationLoading(true);
      await onConfirm();
      hideConfirmation();
    } catch (error) {
      console.error('Confirmation action failed:', error);
      // Optional: show error notification here if needed, 
      // but usually the calling component handles specific errors.
    } finally {
      setConfirmationLoading(false);
    }
  };

  const handleCancel = () => {
    if (isLoading) {
      return;
    }
    if (onCancel) {
      onCancel();
    }
    hideConfirmation();
  };

  return (
    <Modal open={isOpen} onClose={handleCancel} title={title} closeDisabled={isLoading}>
      {typeof message === 'string' ? (
        <p style={{ margin: '1rem 0' }}>{message}</p>
      ) : (
        <div style={{ margin: '1rem 0' }}>{message}</div>
      )}
      <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '1rem', marginTop: '2rem' }}>
        <Button variant="ghost" onClick={handleCancel} disabled={isLoading}>
          {cancelText || t('common.cancel')}
        </Button>
        <Button 
          variant={variant} 
          onClick={handleConfirm} 
          loading={isLoading}
        >
          {confirmText || t('common.confirm')}
        </Button>
      </div>
    </Modal>
  );
}
