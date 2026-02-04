import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { modelsApi } from '@/services/api';
import type { ApiKeyEntry } from '@/types';
import type { ModelInfo } from '@/utils/models';
import { buildHeaderObject, type HeaderEntry } from '@/utils/headers';
import { buildOpenAIModelsEndpoint } from '../utils';
import styles from '@/pages/AiProvidersPage.module.scss';

interface OpenAIDiscoveryModalProps {
  isOpen: boolean;
  baseUrl: string;
  headers: HeaderEntry[];
  apiKeyEntries: ApiKeyEntry[];
  onClose: () => void;
  onApply: (selected: ModelInfo[]) => void;
}

export function OpenAIDiscoveryModal({
  isOpen,
  baseUrl,
  headers,
  apiKeyEntries,
  onClose,
  onApply,
}: OpenAIDiscoveryModalProps) {
  const { t } = useTranslation();
  const [endpoint, setEndpoint] = useState('');
  const [models, setModels] = useState<ModelInfo[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [search, setSearch] = useState('');
  const [selected, setSelected] = useState<Set<string>>(new Set());

  const getErrorMessage = (err: unknown) => {
    if (err instanceof Error) return err.message;
    if (typeof err === 'string') return err;
    return '';
  };

  const filteredModels = useMemo(() => {
    const filter = search.trim().toLowerCase();
    if (!filter) return models;
    return models.filter((model) => {
      const name = (model.name || '').toLowerCase();
      const alias = (model.alias || '').toLowerCase();
      const desc = (model.description || '').toLowerCase();
      return name.includes(filter) || alias.includes(filter) || desc.includes(filter);
    });
  }, [models, search]);

  const fetchOpenaiModelDiscovery = useCallback(
    async ({ allowFallback = true }: { allowFallback?: boolean } = {}) => {
      const trimmedBaseUrl = baseUrl.trim();
      if (!trimmedBaseUrl) return;

      setLoading(true);
      setError('');
      try {
        const headerObject = buildHeaderObject(headers);
        const firstKey = apiKeyEntries.find((entry) => entry.apiKey?.trim())?.apiKey?.trim();
        const hasAuthHeader = Boolean(headerObject.Authorization || headerObject['authorization']);
        const list = await modelsApi.fetchModelsViaApiCall(
          trimmedBaseUrl,
          hasAuthHeader ? undefined : firstKey,
          headerObject
        );
        setModels(list);
      } catch (err: unknown) {
        if (allowFallback) {
          try {
            const list = await modelsApi.fetchModelsViaApiCall(trimmedBaseUrl);
            setModels(list);
            return;
          } catch (fallbackErr: unknown) {
            const message = getErrorMessage(fallbackErr) || getErrorMessage(err);
            setModels([]);
            setError(`${t('ai_providers.openai_models_fetch_error')}: ${message}`);
          }
        } else {
          setModels([]);
          setError(`${t('ai_providers.openai_models_fetch_error')}: ${getErrorMessage(err)}`);
        }
      } finally {
        setLoading(false);
      }
    },
    [apiKeyEntries, baseUrl, headers, t]
  );

  useEffect(() => {
    if (!isOpen) return;
    setEndpoint(buildOpenAIModelsEndpoint(baseUrl));
    setModels([]);
    setSearch('');
    setSelected(new Set());
    setError('');
    void fetchOpenaiModelDiscovery();
  }, [baseUrl, fetchOpenaiModelDiscovery, isOpen]);

  const toggleSelection = (name: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(name)) {
        next.delete(name);
      } else {
        next.add(name);
      }
      return next;
    });
  };

  const handleApply = () => {
    const selectedModels = models.filter((model) => selected.has(model.name));
    onApply(selectedModels);
  };

  return (
    <Modal
      open={isOpen}
      onClose={onClose}
      title={t('ai_providers.openai_models_fetch_title')}
      width={720}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={loading}>
            {t('ai_providers.openai_models_fetch_back')}
          </Button>
          <Button onClick={handleApply} disabled={loading}>
            {t('ai_providers.openai_models_fetch_apply')}
          </Button>
        </>
      }
    >
      <div className="hint" style={{ marginBottom: 8 }}>
        {t('ai_providers.openai_models_fetch_hint')}
      </div>
      <div className="form-group">
        <label>{t('ai_providers.openai_models_fetch_url_label')}</label>
        <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
          <input className="input" readOnly value={endpoint} />
          <Button
            variant="secondary"
            size="sm"
            onClick={() => void fetchOpenaiModelDiscovery({ allowFallback: true })}
            loading={loading}
          >
            {t('ai_providers.openai_models_fetch_refresh')}
          </Button>
        </div>
      </div>
      <Input
        label={t('ai_providers.openai_models_search_label')}
        placeholder={t('ai_providers.openai_models_search_placeholder')}
        value={search}
        onChange={(e) => setSearch(e.target.value)}
      />
      {error && <div className="error-box">{error}</div>}
      {loading ? (
        <div className="hint">{t('ai_providers.openai_models_fetch_loading')}</div>
      ) : models.length === 0 ? (
        <div className="hint">{t('ai_providers.openai_models_fetch_empty')}</div>
      ) : filteredModels.length === 0 ? (
        <div className="hint">{t('ai_providers.openai_models_search_empty')}</div>
      ) : (
        <div className={styles.modelDiscoveryList}>
          {filteredModels.map((model) => {
            const checked = selected.has(model.name);
            return (
              <label
                key={model.name}
                className={`${styles.modelDiscoveryRow} ${checked ? styles.modelDiscoveryRowSelected : ''}`}
              >
                <input type="checkbox" checked={checked} onChange={() => toggleSelection(model.name)} />
                <div className={styles.modelDiscoveryMeta}>
                  <div className={styles.modelDiscoveryName}>
                    {model.name}
                    {model.alias && <span className={styles.modelDiscoveryAlias}>{model.alias}</span>}
                  </div>
                  {model.description && (
                    <div className={styles.modelDiscoveryDesc}>{model.description}</div>
                  )}
                </div>
              </label>
            );
          })}
        </div>
      )}
    </Modal>
  );
}
