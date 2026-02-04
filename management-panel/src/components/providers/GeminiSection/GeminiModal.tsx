import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { HeaderInputList } from '@/components/ui/HeaderInputList';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import type { GeminiKeyConfig } from '@/types';
import { headersToEntries } from '@/utils/headers';
import { excludedModelsToText } from '../utils';
import type { GeminiFormState, ProviderModalProps } from '../types';

interface GeminiModalProps extends ProviderModalProps<GeminiKeyConfig, GeminiFormState> {
  isSaving: boolean;
}

const buildEmptyForm = (): GeminiFormState => ({
  apiKey: '',
  prefix: '',
  baseUrl: '',
  headers: [],
  excludedModels: [],
  excludedText: '',
});

export function GeminiModal({
  isOpen,
  editIndex,
  initialData,
  onClose,
  onSave,
  isSaving,
}: GeminiModalProps) {
  const { t } = useTranslation();
  const [form, setForm] = useState<GeminiFormState>(buildEmptyForm);

  useEffect(() => {
    if (!isOpen) return;
    if (initialData) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setForm({
        ...initialData,
        headers: headersToEntries(initialData.headers),
        excludedText: excludedModelsToText(initialData.excludedModels),
      });
      return;
    }
    setForm(buildEmptyForm());
  }, [initialData, isOpen]);

  const handleSave = () => {
    void onSave(form, editIndex);
  };

  return (
    <Modal
      open={isOpen}
      onClose={onClose}
      title={
        editIndex !== null
          ? t('ai_providers.gemini_edit_modal_title')
          : t('ai_providers.gemini_add_modal_title')
      }
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={isSaving}>
            {t('common.cancel')}
          </Button>
          <Button onClick={handleSave} loading={isSaving}>
            {t('common.save')}
          </Button>
        </>
      }
    >
      <Input
        label={t('ai_providers.gemini_add_modal_key_label')}
        placeholder={t('ai_providers.gemini_add_modal_key_placeholder')}
        value={form.apiKey}
        onChange={(e) => setForm((prev) => ({ ...prev, apiKey: e.target.value }))}
      />
      <Input
        label={t('ai_providers.prefix_label')}
        placeholder={t('ai_providers.prefix_placeholder')}
        value={form.prefix ?? ''}
        onChange={(e) => setForm((prev) => ({ ...prev, prefix: e.target.value }))}
        hint={t('ai_providers.prefix_hint')}
      />
      <Input
        label={t('ai_providers.gemini_base_url_label')}
        placeholder={t('ai_providers.gemini_base_url_placeholder')}
        value={form.baseUrl ?? ''}
        onChange={(e) => setForm((prev) => ({ ...prev, baseUrl: e.target.value }))}
      />
      <HeaderInputList
        entries={form.headers}
        onChange={(entries) => setForm((prev) => ({ ...prev, headers: entries }))}
        addLabel={t('common.custom_headers_add')}
        keyPlaceholder={t('common.custom_headers_key_placeholder')}
        valuePlaceholder={t('common.custom_headers_value_placeholder')}
      />
      <div className="form-group">
        <label>{t('ai_providers.excluded_models_label')}</label>
        <textarea
          className="input"
          placeholder={t('ai_providers.excluded_models_placeholder')}
          value={form.excludedText}
          onChange={(e) => setForm((prev) => ({ ...prev, excludedText: e.target.value }))}
          rows={4}
        />
        <div className="hint">{t('ai_providers.excluded_models_hint')}</div>
      </div>
    </Modal>
  );
}
