import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { HeaderInputList } from '@/components/ui/HeaderInputList';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { ModelInputList } from '@/components/ui/ModelInputList';
import { modelsToEntries } from '@/components/ui/modelInputListUtils';
import type { ProviderKeyConfig } from '@/types';
import { headersToEntries } from '@/utils/headers';
import { excludedModelsToText } from '../utils';
import type { ProviderFormState, ProviderModalProps } from '../types';

interface ClaudeModalProps extends ProviderModalProps<ProviderKeyConfig, ProviderFormState> {
  isSaving: boolean;
}

const buildEmptyForm = (): ProviderFormState => ({
  apiKey: '',
  prefix: '',
  baseUrl: '',
  proxyUrl: '',
  headers: [],
  models: [],
  excludedModels: [],
  modelEntries: [{ name: '', alias: '' }],
  excludedText: '',
});

export function ClaudeModal({
  isOpen,
  editIndex,
  initialData,
  onClose,
  onSave,
  isSaving,
}: ClaudeModalProps) {
  const { t } = useTranslation();
  const [form, setForm] = useState<ProviderFormState>(buildEmptyForm);

  useEffect(() => {
    if (!isOpen) return;
    if (initialData) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setForm({
        ...initialData,
        headers: headersToEntries(initialData.headers),
        modelEntries: modelsToEntries(initialData.models),
        excludedText: excludedModelsToText(initialData.excludedModels),
      });
      return;
    }
    setForm(buildEmptyForm());
  }, [initialData, isOpen]);

  return (
    <Modal
      open={isOpen}
      onClose={onClose}
      title={
        editIndex !== null
          ? t('ai_providers.claude_edit_modal_title')
          : t('ai_providers.claude_add_modal_title')
      }
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={isSaving}>
            {t('common.cancel')}
          </Button>
          <Button onClick={() => void onSave(form, editIndex)} loading={isSaving}>
            {t('common.save')}
          </Button>
        </>
      }
    >
      <Input
        label={t('ai_providers.claude_add_modal_key_label')}
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
        label={t('ai_providers.claude_add_modal_url_label')}
        value={form.baseUrl ?? ''}
        onChange={(e) => setForm((prev) => ({ ...prev, baseUrl: e.target.value }))}
      />
      <Input
        label={t('ai_providers.claude_add_modal_proxy_label')}
        value={form.proxyUrl ?? ''}
        onChange={(e) => setForm((prev) => ({ ...prev, proxyUrl: e.target.value }))}
      />
      <HeaderInputList
        entries={form.headers}
        onChange={(entries) => setForm((prev) => ({ ...prev, headers: entries }))}
        addLabel={t('common.custom_headers_add')}
        keyPlaceholder={t('common.custom_headers_key_placeholder')}
        valuePlaceholder={t('common.custom_headers_value_placeholder')}
      />
      <div className="form-group">
        <label>{t('ai_providers.claude_models_label')}</label>
        <ModelInputList
          entries={form.modelEntries}
          onChange={(entries) => setForm((prev) => ({ ...prev, modelEntries: entries }))}
          addLabel={t('ai_providers.claude_models_add_btn')}
          namePlaceholder={t('common.model_name_placeholder')}
          aliasPlaceholder={t('common.model_alias_placeholder')}
          disabled={isSaving}
        />
      </div>
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
