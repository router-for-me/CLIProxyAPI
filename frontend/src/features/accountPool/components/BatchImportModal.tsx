import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';

interface Props {
  open: boolean;
  onClose: () => void;
  onImport: (text: string) => Promise<void>;
  title: string;
  placeholder: string;
}

export function BatchImportModal({ open, onClose, onImport, title, placeholder }: Props) {
  const { t } = useTranslation();
  const [text, setText] = useState('');
  const [importing, setImporting] = useState(false);

  const handleImport = async () => {
    if (!text.trim()) return;
    setImporting(true);
    try {
      await onImport(text);
      setText('');
      onClose();
    } finally {
      setImporting(false);
    }
  };

  return (
    <Modal open={open} title={title} onClose={onClose}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: '12px' }}>
        <textarea
          value={text}
          onChange={(e) => setText(e.target.value)}
          placeholder={placeholder}
          rows={10}
          wrap="off"
          style={{
            width: '100%',
            padding: '8px 12px',
            borderRadius: '6px',
            border: '1px solid var(--border-color)',
            backgroundColor: 'var(--bg-secondary)',
            color: 'var(--text-primary)',
            fontFamily: 'monospace',
            fontSize: '13px',
            resize: 'vertical',
            whiteSpace: 'pre',
            overflowX: 'auto',
          }}
        />
        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '8px' }}>
          <Button variant="secondary" onClick={onClose}>
            {t('common.cancel', { defaultValue: 'Cancel' })}
          </Button>
          <Button onClick={handleImport} disabled={!text.trim() || importing}>
            {importing ? t('common.importing', { defaultValue: 'Importing...' }) : t('common.import', { defaultValue: 'Import' })}
          </Button>
        </div>
      </div>
    </Modal>
  );
}
