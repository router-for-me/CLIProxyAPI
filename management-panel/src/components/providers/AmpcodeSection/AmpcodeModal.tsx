import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { ModelInputList } from '@/components/ui/ModelInputList';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { useConfigStore, useNotificationStore } from '@/stores';
import { ampcodeApi } from '@/services/api';
import type { AmpcodeConfig } from '@/types';
import { maskApiKey } from '@/utils/format';
import { buildAmpcodeFormState, entriesToAmpcodeMappings } from '../utils';
import type { AmpcodeFormState } from '../types';

interface AmpcodeModalProps {
  isOpen: boolean;
  disableControls: boolean;
  onClose: () => void;
  onBusyChange?: (busy: boolean) => void;
}

export function AmpcodeModal({ isOpen, disableControls, onClose, onBusyChange }: AmpcodeModalProps) {
  const { t } = useTranslation();
  const { showNotification, showConfirmation } = useNotificationStore();
  const config = useConfigStore((state) => state.config);
  const updateConfigValue = useConfigStore((state) => state.updateConfigValue);
  const clearCache = useConfigStore((state) => state.clearCache);

  const [form, setForm] = useState<AmpcodeFormState>(() => buildAmpcodeFormState(null));
  const [loading, setLoading] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [mappingsDirty, setMappingsDirty] = useState(false);
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const initializedRef = useRef(false);

  const getErrorMessage = (err: unknown) => {
    if (err instanceof Error) return err.message;
    if (typeof err === 'string') return err;
    return '';
  };

  useEffect(() => {
    onBusyChange?.(loading || saving);
  }, [loading, saving, onBusyChange]);

  useEffect(() => {
    if (!isOpen) {
      initializedRef.current = false;
      setLoading(false);
      setSaving(false);
      setError('');
      setLoaded(false);
      setMappingsDirty(false);
      setForm(buildAmpcodeFormState(null));
      onBusyChange?.(false);
      return;
    }
    if (initializedRef.current) return;
    initializedRef.current = true;

    setLoading(true);
    setLoaded(false);
    setMappingsDirty(false);
    setError('');
    setForm(buildAmpcodeFormState(config?.ampcode ?? null));

    void (async () => {
      try {
        const ampcode = await ampcodeApi.getAmpcode();
        setLoaded(true);
        updateConfigValue('ampcode', ampcode);
        clearCache('ampcode');
        setForm(buildAmpcodeFormState(ampcode));
      } catch (err: unknown) {
        setError(getErrorMessage(err) || t('notification.refresh_failed'));
      } finally {
        setLoading(false);
      }
    })();
  }, [clearCache, config?.ampcode, isOpen, onBusyChange, t, updateConfigValue]);

  const clearAmpcodeUpstreamApiKey = async () => {
    showConfirmation({
      title: t('ai_providers.ampcode_clear_upstream_api_key_title', { defaultValue: 'Clear Upstream API Key' }),
      message: t('ai_providers.ampcode_clear_upstream_api_key_confirm'),
      variant: 'danger',
      confirmText: t('common.confirm'),
      onConfirm: async () => {
        setSaving(true);
        setError('');
        try {
          await ampcodeApi.clearUpstreamApiKey();
          const previous = config?.ampcode ?? {};
          const next: AmpcodeConfig = { ...previous };
          delete next.upstreamApiKey;
          updateConfigValue('ampcode', next);
          clearCache('ampcode');
          showNotification(t('notification.ampcode_upstream_api_key_cleared'), 'success');
        } catch (err: unknown) {
          const message = getErrorMessage(err);
          setError(message);
          showNotification(`${t('notification.update_failed')}: ${message}`, 'error');
        } finally {
          setSaving(false);
        }
      },
    });
  };

  const performSaveAmpcode = async () => {
    setSaving(true);
    setError('');
    try {
      const upstreamUrl = form.upstreamUrl.trim();
      const overrideKey = form.upstreamApiKey.trim();
      const modelMappings = entriesToAmpcodeMappings(form.mappingEntries);

      if (upstreamUrl) {
        await ampcodeApi.updateUpstreamUrl(upstreamUrl);
      } else {
        await ampcodeApi.clearUpstreamUrl();
      }

      await ampcodeApi.updateForceModelMappings(form.forceModelMappings);

      if (loaded || mappingsDirty) {
        if (modelMappings.length) {
          await ampcodeApi.saveModelMappings(modelMappings);
        } else {
          await ampcodeApi.clearModelMappings();
        }
      }

      if (overrideKey) {
        await ampcodeApi.updateUpstreamApiKey(overrideKey);
      }

      const previous = config?.ampcode ?? {};
      const next: AmpcodeConfig = {
        upstreamUrl: upstreamUrl || undefined,
        forceModelMappings: form.forceModelMappings,
      };

      if (previous.upstreamApiKey) {
        next.upstreamApiKey = previous.upstreamApiKey;
      }

      if (Array.isArray(previous.modelMappings)) {
        next.modelMappings = previous.modelMappings;
      }

      if (overrideKey) {
        next.upstreamApiKey = overrideKey;
      }

      if (loaded || mappingsDirty) {
        if (modelMappings.length) {
          next.modelMappings = modelMappings;
        } else {
          delete next.modelMappings;
        }
      }

      updateConfigValue('ampcode', next);
      clearCache('ampcode');
      showNotification(t('notification.ampcode_updated'), 'success');
      onClose();
    } catch (err: unknown) {
      const message = getErrorMessage(err);
      setError(message);
      showNotification(`${t('notification.update_failed')}: ${message}`, 'error');
    } finally {
      setSaving(false);
    }
  };

  const saveAmpcode = async () => {
    if (!loaded && mappingsDirty) {
      showConfirmation({
        title: t('ai_providers.ampcode_mappings_overwrite_title', { defaultValue: 'Overwrite Mappings' }),
        message: t('ai_providers.ampcode_mappings_overwrite_confirm'),
        variant: 'secondary', // Not dangerous, just a warning
        confirmText: t('common.confirm'),
        onConfirm: performSaveAmpcode,
      });
      return;
    }

    await performSaveAmpcode();
  };

  return (
    <Modal
      open={isOpen}
      onClose={onClose}
      title={t('ai_providers.ampcode_modal_title')}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={saving}>
            {t('common.cancel')}
          </Button>
          <Button onClick={saveAmpcode} loading={saving} disabled={disableControls || loading}>
            {t('common.save')}
          </Button>
        </>
      }
    >
      {error && <div className="error-box">{error}</div>}
      <Input
        label={t('ai_providers.ampcode_upstream_url_label')}
        placeholder={t('ai_providers.ampcode_upstream_url_placeholder')}
        value={form.upstreamUrl}
        onChange={(e) => setForm((prev) => ({ ...prev, upstreamUrl: e.target.value }))}
        disabled={loading || saving}
        hint={t('ai_providers.ampcode_upstream_url_hint')}
      />
      <Input
        label={t('ai_providers.ampcode_upstream_api_key_label')}
        placeholder={t('ai_providers.ampcode_upstream_api_key_placeholder')}
        type="password"
        value={form.upstreamApiKey}
        onChange={(e) => setForm((prev) => ({ ...prev, upstreamApiKey: e.target.value }))}
        disabled={loading || saving}
        hint={t('ai_providers.ampcode_upstream_api_key_hint')}
      />
      <div
        style={{
          display: 'flex',
          gap: 8,
          alignItems: 'center',
          marginTop: -8,
          marginBottom: 12,
          flexWrap: 'wrap',
        }}
      >
        <div className="hint" style={{ margin: 0 }}>
          {t('ai_providers.ampcode_upstream_api_key_current', {
            key: config?.ampcode?.upstreamApiKey
              ? maskApiKey(config.ampcode.upstreamApiKey)
              : t('common.not_set'),
          })}
        </div>
        <Button
          variant="danger"
          size="sm"
          onClick={clearAmpcodeUpstreamApiKey}
          disabled={loading || saving || !config?.ampcode?.upstreamApiKey}
        >
          {t('ai_providers.ampcode_clear_upstream_api_key')}
        </Button>
      </div>

      <div className="form-group">
        <ToggleSwitch
          label={t('ai_providers.ampcode_force_model_mappings_label')}
          checked={form.forceModelMappings}
          onChange={(value) => setForm((prev) => ({ ...prev, forceModelMappings: value }))}
          disabled={loading || saving}
        />
        <div className="hint">{t('ai_providers.ampcode_force_model_mappings_hint')}</div>
      </div>

      <div className="form-group">
        <label>{t('ai_providers.ampcode_model_mappings_label')}</label>
        <ModelInputList
          entries={form.mappingEntries}
          onChange={(entries) => {
            setMappingsDirty(true);
            setForm((prev) => ({ ...prev, mappingEntries: entries }));
          }}
          addLabel={t('ai_providers.ampcode_model_mappings_add_btn')}
          namePlaceholder={t('ai_providers.ampcode_model_mappings_from_placeholder')}
          aliasPlaceholder={t('ai_providers.ampcode_model_mappings_to_placeholder')}
          disabled={loading || saving}
        />
        <div className="hint">{t('ai_providers.ampcode_model_mappings_hint')}</div>
      </div>
    </Modal>
  );
}
