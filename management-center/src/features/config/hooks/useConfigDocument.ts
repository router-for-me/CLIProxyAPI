// 配置文档的加载 / 保存状态机 —— 从旧 pages/ConfigPage.tsx 逐字提取。
// 正确性核心，勿随手「顺化」：两阶段保存（预览前 re-fetch → diff → 确认时再 re-fetch，
// 服务端变更则重新预览不落盘）、可视化模式的规范化 diff、commercial-mode 重启警告、
// 保存成功后刷新全局 config store。

import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { parse as parseYaml, parseDocument } from 'yaml';
import { useConfigStore, useNotificationStore } from '@/stores';
import { configFileApi } from '@/services/api/configFile';
import type { ConfigEditorMode } from '../constants';

function readCommercialModeFromYaml(yamlContent: string): boolean {
  try {
    const parsed = parseYaml(yamlContent);
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return false;
    return Boolean((parsed as Record<string, unknown>)['commercial-mode']);
  } catch {
    return false;
  }
}

function normalizeYamlForVisualDiff(yamlContent: string): string {
  try {
    const doc = parseDocument(yamlContent);
    return doc.toString({ indent: 2, lineWidth: 120, minContentWidth: 0 });
  } catch {
    return yamlContent;
  }
}

export type UseConfigDocumentArgs = {
  /** 当前编辑模式（旧实现中的 activeTab）。 */
  mode: ConfigEditorMode;
  visualDirty: boolean;
  visualParseError: string | null;
  loadVisualValuesFromYaml: (yaml: string) => { ok: true } | { ok: false; error: string };
  applyVisualChangesToYaml: (yaml: string) => string;
};

/** 可视化保存仅在用户真正编辑过源码时以本地草稿为合并基底。 */
export function selectVisualMergeBase(
  latestServerYaml: string,
  sourceDraftYaml: string,
  sourceDirty: boolean
): string {
  return sourceDirty ? sourceDraftYaml : latestServerYaml;
}

export function buildConfigSaveDraft(
  latestServerYaml: string,
  sourceDraftYaml: string,
  sourceDirty: boolean,
  mode: ConfigEditorMode,
  applyVisualChanges: (yaml: string) => string
): string {
  if (sourceDirty && mode === 'source') return sourceDraftYaml;
  return applyVisualChanges(selectVisualMergeBase(latestServerYaml, sourceDraftYaml, sourceDirty));
}

/**
 * 未编辑源码的模式往返不应重载可视化值，否则会清空字段级 dirty，改变并发合并策略。
 * YAML 曾解析失败时仍须重试解析，避免仅靠切换模式绕过错误。
 */
export function shouldReloadVisualDraft(sourceDirty: boolean, visualParseError: string | null) {
  return sourceDirty || visualParseError !== null;
}

export function useConfigDocument({
  mode,
  visualDirty,
  visualParseError,
  loadVisualValuesFromYaml,
  applyVisualChangesToYaml,
}: UseConfigDocumentArgs) {
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);

  const [content, setContent] = useState('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  // 仅表示用户在源码编辑器中改过草稿；可视化字段同步到 content 不得修改它。
  const [sourceDirty, setSourceDirty] = useState(false);
  const [diffModalOpen, setDiffModalOpen] = useState(false);
  const [serverYaml, setServerYaml] = useState('');
  const [mergedYaml, setMergedYaml] = useState('');
  const [previewServerYaml, setPreviewServerYaml] = useState('');
  const [previewMode, setPreviewMode] = useState<ConfigEditorMode>('visual');

  const isDirty = sourceDirty || visualDirty;

  const loadConfig = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await configFileApi.fetchConfigYaml();
      setContent(data);
      setSourceDirty(false);
      setDiffModalOpen(false);
      setServerYaml(data);
      setMergedYaml(data);
      setPreviewServerYaml(data);
      loadVisualValuesFromYaml(data);
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : t('notification.refresh_failed');
      setError(message);
    } finally {
      setLoading(false);
    }
  }, [loadVisualValuesFromYaml, t]);

  useEffect(() => {
    loadConfig();
  }, [loadConfig]);

  const handleConfirmSave = useCallback(async () => {
    setSaving(true);
    try {
      const latestServerYaml = await configFileApi.fetchConfigYaml();
      if (latestServerYaml !== previewServerYaml) {
        const nextMergedYaml =
          previewMode === 'visual' && !sourceDirty
            ? applyVisualChangesToYaml(latestServerYaml)
            : mergedYaml;
        const nextServerYaml =
          previewMode === 'visual'
            ? normalizeYamlForVisualDiff(latestServerYaml)
            : latestServerYaml;

        setPreviewServerYaml(latestServerYaml);
        setServerYaml(nextServerYaml);
        setMergedYaml(nextMergedYaml);

        if (nextServerYaml === nextMergedYaml) {
          setSourceDirty(false);
          setDiffModalOpen(false);
          setContent(latestServerYaml);
          loadVisualValuesFromYaml(latestServerYaml);
          showNotification(t('config_management.diff.no_changes'), 'info');
        }
        return;
      }

      const previousCommercialMode = readCommercialModeFromYaml(latestServerYaml);
      const nextCommercialMode = readCommercialModeFromYaml(mergedYaml);
      const commercialModeChanged = previousCommercialMode !== nextCommercialMode;

      await configFileApi.saveConfigYaml(mergedYaml);
      const latestContent = await configFileApi.fetchConfigYaml();
      setSourceDirty(false);
      setDiffModalOpen(false);
      setContent(latestContent);
      setServerYaml(latestContent);
      setMergedYaml(latestContent);
      setPreviewServerYaml(latestContent);
      loadVisualValuesFromYaml(latestContent);

      // Keep the global config store in sync so sidebar / other pages reflect YAML changes immediately.
      try {
        useConfigStore.getState().clearCache();
        await useConfigStore.getState().fetchConfig(true);
      } catch (refreshError: unknown) {
        const message =
          refreshError instanceof Error
            ? refreshError.message
            : typeof refreshError === 'string'
              ? refreshError
              : '';
        showNotification(
          `${t('notification.refresh_failed')}${message ? `: ${message}` : ''}`,
          'error'
        );
      }

      showNotification(t('config_management.save_success'), 'success');
      if (commercialModeChanged) {
        showNotification(t('notification.commercial_mode_restart_required'), 'warning');
      }
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : '';
      showNotification(`${t('notification.save_failed')}: ${message}`, 'error');
    } finally {
      setSaving(false);
    }
  }, [
    applyVisualChangesToYaml,
    sourceDirty,
    loadVisualValuesFromYaml,
    mergedYaml,
    previewMode,
    previewServerYaml,
    showNotification,
    t,
  ]);

  const handleSave = useCallback(async () => {
    if (mode === 'visual' && visualParseError) {
      showNotification(t('config_management.visual_mode_save_blocked'), 'error');
      return;
    }

    setSaving(true);
    try {
      const latestServerYaml = await configFileApi.fetchConfigYaml();

      const visualBaseYaml = selectVisualMergeBase(latestServerYaml, content, sourceDirty);
      if (mode === 'visual' || !sourceDirty) {
        const latestDocument = parseDocument(latestServerYaml);
        if (latestDocument.errors.length > 0) {
          showNotification(
            t('config_management.visual_mode_latest_yaml_invalid', {
              message:
                latestDocument.errors[0]?.message ??
                t('config_management.visual_mode_save_blocked'),
            }),
            'error'
          );
          return;
        }

        if (visualBaseYaml !== latestServerYaml) {
          const visualBaseDocument = parseDocument(visualBaseYaml);
          if (visualBaseDocument.errors.length > 0) {
            showNotification(
              t('config_management.visual_mode_latest_yaml_invalid', {
                message:
                  visualBaseDocument.errors[0]?.message ??
                  t('config_management.visual_mode_save_blocked'),
              }),
              'error'
            );
            return;
          }
        }
      }

      // The edit origin, not the currently visible mode, decides the merge policy. A real source
      // edit preserves the complete draft; a visual edit still merges onto the latest server YAML
      // after switching to source merely to inspect the generated document.
      const nextMergedYaml = buildConfigSaveDraft(
        latestServerYaml,
        content,
        sourceDirty,
        mode,
        applyVisualChangesToYaml
      );

      // In visual-origin saves, applyVisualChangesToYaml re-serializes YAML via parseDocument → toString,
      // which may reformat comments/whitespace. Normalize the server YAML through the same pipeline
      // so the diff only shows actual value changes, not cosmetic reformatting.
      let diffOriginal = latestServerYaml;
      if (!sourceDirty) {
        diffOriginal = normalizeYamlForVisualDiff(latestServerYaml);
      }

      if (diffOriginal === nextMergedYaml) {
        setSourceDirty(false);
        setContent(latestServerYaml);
        setServerYaml(latestServerYaml);
        setMergedYaml(nextMergedYaml);
        setPreviewServerYaml(latestServerYaml);
        loadVisualValuesFromYaml(latestServerYaml);
        showNotification(t('config_management.diff.no_changes'), 'info');
        return;
      }

      setServerYaml(diffOriginal);
      setMergedYaml(nextMergedYaml);
      setPreviewServerYaml(latestServerYaml);
      setPreviewMode(sourceDirty ? 'source' : 'visual');
      setDiffModalOpen(true);
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : '';
      showNotification(`${t('notification.save_failed')}: ${message}`, 'error');
    } finally {
      setSaving(false);
    }
  }, [
    applyVisualChangesToYaml,
    content,
    sourceDirty,
    loadVisualValuesFromYaml,
    mode,
    showNotification,
    t,
    visualParseError,
  ]);

  /** 可视化→源码时只物化当前字段值，不把同步动作冒充为用户源码编辑。 */
  const syncContentFromVisual = useCallback((value: string) => {
    setContent(value);
  }, []);

  /** 源码编辑器 onChange：写入内容并记录真正的源码草稿。 */
  const handleChange = useCallback((value: string) => {
    setContent(value);
    setSourceDirty(true);
  }, []);

  const handleReload = useCallback(() => {
    if (!isDirty) {
      void loadConfig();
      return;
    }

    showConfirmation({
      title: t('common.unsaved_changes_title'),
      message: t('config_management.reload_confirm_message'),
      confirmText: t('config_management.reload'),
      cancelText: t('common.cancel'),
      variant: 'danger',
      onConfirm: async () => {
        await loadConfig();
      },
    });
  }, [isDirty, loadConfig, showConfirmation, t]);

  /** 无需联网，直接恢复最近一次成功读取的原始服务端 YAML。 */
  const handleDiscard = useCallback(() => {
    if (!isDirty) return;

    showConfirmation({
      title: t('common.unsaved_changes_title'),
      message: t('config_management.discard_confirm_message'),
      confirmText: t('config_management.actions.discard'),
      cancelText: t('common.cancel'),
      variant: 'danger',
      onConfirm: () => {
        setContent(previewServerYaml);
        setSourceDirty(false);
        setDiffModalOpen(false);
        setServerYaml(previewServerYaml);
        setMergedYaml(previewServerYaml);
        loadVisualValuesFromYaml(previewServerYaml);
      },
    });
  }, [isDirty, loadVisualValuesFromYaml, previewServerYaml, showConfirmation, t]);

  const closeDiff = useCallback(() => setDiffModalOpen(false), []);

  return {
    content,
    syncContentFromVisual,
    loading,
    saving,
    error,
    sourceDirty,
    isDirty,
    diffModalOpen,
    serverYaml,
    mergedYaml,
    loadConfig,
    handleSave,
    handleConfirmSave,
    handleChange,
    handleReload,
    handleDiscard,
    closeDiff,
  };
}
