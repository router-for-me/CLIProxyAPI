import { useEffect, useId, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  ExcludedModelsPicker,
  formatExcludedRulesText,
  parseExcludedRulesText,
  type ExcludedModelsCatalogState,
} from '@/components/excludedModels';
import { authFilesApi } from '@/services/api';
import type { AuthFileModelItem } from '@/features/authFiles/constants';

interface AuthFileExcludedModelsFieldProps {
  fileName: string;
  /** 换行分隔的规则文本——凭证编辑器的 dirty diff 依赖这个形状，不要改成数组。 */
  value: string;
  disabled: boolean;
  onChange: (value: string) => void;
}

export function AuthFileExcludedModelsField({
  fileName,
  value,
  disabled,
  onChange,
}: AuthFileExcludedModelsFieldProps) {
  const { t } = useTranslation();
  // 凭证文件名可能含点/斜杠等字符，不适合直接当 HTML id。
  const labelId = `${useId()}-excluded-models-label`;
  const latestValueRef = useRef(value);
  const [models, setModels] = useState<AuthFileModelItem[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadFailed, setLoadFailed] = useState(false);

  useEffect(() => {
    latestValueRef.current = value;
  }, [value]);

  useEffect(() => {
    let cancelled = false;
    setModels([]);
    setLoading(true);
    setLoadFailed(false);

    void authFilesApi
      .getModelsForAuthFile(fileName)
      .then((items) => {
        if (cancelled) return;
        const byId = new Map<string, AuthFileModelItem>();
        items.forEach((item) => {
          const id = item.id?.trim();
          if (id) byId.set(id.toLowerCase(), { ...item, id });
        });
        // 已配置但目录里没有的精确规则也塞进候选，否则它们会在列表里凭空消失。
        parseExcludedRulesText(latestValueRef.current).forEach((rule) => {
          if (!rule.includes('*') && !byId.has(rule.toLowerCase())) {
            byId.set(rule.toLowerCase(), { id: rule });
          }
        });
        setModels(
          [...byId.values()].sort((left, right) =>
            left.id.localeCompare(right.id, undefined, { sensitivity: 'base' })
          )
        );
      })
      .catch(() => {
        if (!cancelled) setLoadFailed(true);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, [fileName]);

  const rules = useMemo(() => parseExcludedRulesText(value), [value]);
  const candidates = useMemo(
    () => models.map((model) => ({ id: model.id, displayName: model.display_name })),
    [models]
  );
  const catalogState: ExcludedModelsCatalogState = loading
    ? 'loading'
    : loadFailed
      ? 'error'
      : 'ready';

  return (
    <div className="form-group">
      <label id={labelId}>{t('auth_files.excluded_models_label')}</label>
      <ExcludedModelsPicker
        value={rules}
        onChange={(next) => onChange(formatExcludedRulesText(next))}
        candidates={candidates}
        catalogState={catalogState}
        disabled={disabled}
        labelledBy={labelId}
      />
    </div>
  );
}
