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
import type { ProviderModalProps, VertexFormState } from '../types';

interface VertexModalProps extends ProviderModalProps<ProviderKeyConfig, VertexFormState> {
  isSaving: boolean;
}

const buildEmptyForm = (): VertexFormState => ({
  apiKey: '',
  prefix: '',
  baseUrl: '',
  proxyUrl: '',
  headers: [],
  models: [],
  modelEntries: [{ name: '', alias: '' }],
});

export function VertexModal({
  isOpen,
  editIndex,
  initialData,
  onClose,
  onSave,
  isSaving,
}: VertexModalProps) {
  const { t } = useTranslation();
  const [form, setForm] = useState<VertexFormState>(buildEmptyForm);

  useEffect(() => {
    if (!isOpen) return;
    if (initialData) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setForm({
        ...initialData,
        headers: headersToEntries(initialData.headers),
        modelEntries: modelsToEntries(initialData.models),
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
          ? t('ai_providers.vertex_edit_modal_title')
          : t('ai_providers.vertex_add_modal_title')
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
        label={t('ai_providers.vertex_add_modal_key_label')}
        placeholder={t('ai_providers.vertex_add_modal_key_placeholder')}
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
        label={t('ai_providers.vertex_add_modal_url_label')}
        placeholder={t('ai_providers.vertex_add_modal_url_placeholder')}
        value={form.baseUrl ?? ''}
        onChange={(e) => setForm((prev) => ({ ...prev, baseUrl: e.target.value }))}
      />
      <Input
        label={t('ai_providers.vertex_add_modal_proxy_label')}
        placeholder={t('ai_providers.vertex_add_modal_proxy_placeholder')}
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
        <label>{t('ai_providers.vertex_models_label')}</label>
        <ModelInputList
          entries={form.modelEntries}
          onChange={(entries) => setForm((prev) => ({ ...prev, modelEntries: entries }))}
          addLabel={t('ai_providers.vertex_models_add_btn')}
          namePlaceholder={t('common.model_name_placeholder')}
          aliasPlaceholder={t('common.model_alias_placeholder')}
          disabled={isSaving}
        />
        <div className="hint">{t('ai_providers.vertex_models_hint')}</div>
      </div>
    </Modal>
  );
}
