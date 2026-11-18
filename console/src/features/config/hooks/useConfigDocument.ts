// 两阶段保存：预览前读取 → 确认时再读取；服务端变化则重新预览。
// 可视化仅提交 v8 字段差异，源码保留整份 YAML 保存。
import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { parse as parseYaml, parseDocument } from 'yaml';
import { useConfigStore, useNotificationStore } from '@/stores';
import { configFileApi } from '@/services/api/configFile';
import { apiClient } from '@/services/api/client';
import {
  applyConfigPatch,
  buildConfigPatch,
  ConfigDraftConflictError,
  hasConfigPatchChanges,
  rebaseConfigDraft,
  type ConfigPatchPlan,
} from '@/services/api/configPatch';
import type { ConfigEditorMode } from '../constants';

export function readCommercialModeFromYaml(yamlContent: string): boolean {
  try {
    const parsed = parseYaml(yamlContent);
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return false;
    const server: unknown = (parsed as Record<string, unknown>).server;
    if (!server || typeof server !== 'object' || Array.isArray(server)) return false;
    return Boolean((server as Record<string, unknown>)['commercial-mode']);
  } catch {
    return false;
  }
}

export function hasTrustedProxiesChange(beforeYaml: string, afterYaml: string): boolean {
  const read = (yaml: string): string[] => {
    try {
      const parsed: unknown = parseYaml(yaml);
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return [];
      const server: unknown = (parsed as Record<string, unknown>).server;
      if (!server || typeof server !== 'object' || Array.isArray(server)) return [];
      const value: unknown = (server as Record<string, unknown>)['trusted-proxies'];
      return Array.isArray(value)
        ? [...new Set(value.filter((entry): entry is string => typeof entry === 'string'))].sort()
        : [];
    } catch {
      return [];
    }
  };
  return JSON.stringify(read(beforeYaml)) !== JSON.stringify(read(afterYaml));
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
  rebaseVisualValuesFromYaml: (
    serverYaml: string,
    draftYaml: string
  ) => { ok: true } | { ok: false; error: string };
  applyVisualChangesToYaml: (yaml: string) => string;
};

export function buildConfigSaveDraft(
  latestServerYaml: string,
  sourceDraftYaml: string,
  sourceDirty: boolean,
  mode: ConfigEditorMode,
  applyVisualChanges: (yaml: string) => string
): string {
  if (sourceDirty) {
    if (mode === 'source') return sourceDraftYaml;
    throw new Error('Unsaved source edits must be saved or discarded before visual editing');
  }
  return applyVisualChanges(latestServerYaml);
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
  rebaseVisualValuesFromYaml,
  applyVisualChangesToYaml,
}: UseConfigDocumentArgs) {
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);

  const [content, setContent] = useState('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [recoveryRequired, setRecoveryRequired] = useState(false);
  const [error, setError] = useState('');
  // 仅表示用户在源码编辑器中改过草稿；可视化字段同步到 content 不得修改它。
  const [sourceDirty, setSourceDirty] = useState(false);
  const [diffModalOpen, setDiffModalOpen] = useState(false);
  const [serverYaml, setServerYaml] = useState('');
  const [mergedYaml, setMergedYaml] = useState('');
  const [previewServerYaml, setPreviewServerYaml] = useState('');
  const [previewMode, setPreviewMode] = useState<ConfigEditorMode>('visual');
  const [previewPlan, setPreviewPlan] = useState<ConfigPatchPlan | null>(null);
  const previewRevision = useRef<number | null>(null);
  const operationId = useRef(0);

  // Ignore old reads/completions after unmount, another load, or an ABA connection switch.
  const beginOperation = useCallback(() => {
    const id = ++operationId.current;
    const revision = apiClient.getConnectionRevision();
    return {
      revision,
      isCurrent: () => id === operationId.current && revision === apiClient.getConnectionRevision(),
    };
  }, []);

  const isDirty = sourceDirty || visualDirty;

  const loadConfig = useCallback(async () => {
    const operation = beginOperation();
    setLoading(true);
    setError('');
    try {
      const data = await configFileApi.fetchConfigYaml();
      if (!operation.isCurrent()) return;
      setContent(data);
      setRecoveryRequired(false);
      setSourceDirty(false);
      setDiffModalOpen(false);
      setServerYaml(data);
      setMergedYaml(data);
      setPreviewServerYaml(data);
      loadVisualValuesFromYaml(data);
    } catch (err: unknown) {
      if (!operation.isCurrent()) return;
      const message = err instanceof Error ? err.message : t('notification.refresh_failed');
      setError(message);
    } finally {
      if (operation.isCurrent()) setLoading(false);
    }
  }, [beginOperation, loadVisualValuesFromYaml, t]);

  useEffect(() => {
    void loadConfig();
    return () => {
      operationId.current += 1;
    };
  }, [loadConfig]);

  // After a partial mutation the current server, not the original load, must become the
  // dirty baseline. Retain the intended edits while also adopting untouched concurrent values.
  const recoverVisualDraft = useCallback(
    (latestYaml: string) => {
      // The server may already contain the list mutations. Replaying old visual IDs
      // against that new order would apply deletions/reorders twice and lose conditions.
      const draftYaml = rebaseConfigDraft(previewServerYaml, mergedYaml, latestYaml);
      const result = rebaseVisualValuesFromYaml(latestYaml, draftYaml);
      if (!result.ok) throw new Error(result.error);
      setContent(latestYaml);
      setPreviewServerYaml(latestYaml);
      setServerYaml(latestYaml);
      setMergedYaml(draftYaml);
      setRecoveryRequired(false);
      return draftYaml;
    },
    [mergedYaml, previewServerYaml, rebaseVisualValuesFromYaml]
  );

  const handleConfirmSave = useCallback(async () => {
    if (!diffModalOpen || saving) return;
    const operation = beginOperation();
    if (previewRevision.current !== operation.revision) return;
    let writeAttempted = false;
    setSaving(true);
    try {
      const latestServerYaml = await configFileApi.fetchConfigYaml();
      if (!operation.isCurrent()) return;
      if (latestServerYaml !== previewServerYaml) {
        const nextMergedYaml = !sourceDirty
          ? applyVisualChangesToYaml(latestServerYaml)
          : mergedYaml;
        const nextPlan =
          previewMode === 'visual' ? buildConfigPatch(latestServerYaml, nextMergedYaml) : null;
        setPreviewPlan(nextPlan);
        const nextServerYaml = !sourceDirty
          ? normalizeYamlForVisualDiff(latestServerYaml)
          : latestServerYaml;

        setPreviewServerYaml(latestServerYaml);
        setServerYaml(nextServerYaml);
        setMergedYaml(nextMergedYaml);

        if (nextPlan ? !hasConfigPatchChanges(nextPlan) : nextServerYaml === nextMergedYaml) {
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
      const trustedProxiesChanged = hasTrustedProxiesChange(latestServerYaml, mergedYaml);

      if (previewMode === 'visual' && !previewPlan) return;
      writeAttempted = true;
      if (previewMode === 'visual' && previewPlan) {
        await applyConfigPatch(previewPlan, operation.revision);
      } else {
        await configFileApi.saveConfigYaml(mergedYaml);
      }
      if (!operation.isCurrent()) return;
      useConfigStore.getState().clearCache();
      const latestContent = await configFileApi.fetchConfigYaml();
      if (!operation.isCurrent()) return;
      setSourceDirty(false);
      setRecoveryRequired(false);
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
        if (!operation.isCurrent()) return;
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

      if (!operation.isCurrent()) return;
      showNotification(t('config_management.save_success'), 'success');
      if (commercialModeChanged) {
        showNotification(t('notification.commercial_mode_restart_required'), 'warning');
      }
      if (trustedProxiesChanged) {
        showNotification(t('notification.trusted_proxies_restart_required'), 'warning');
      }
    } catch (err: unknown) {
      if (!operation.isCurrent()) return;
      const message =
        err instanceof ConfigDraftConflictError
          ? t('config_management.concurrent_list_conflict', { path: err.path.join('.') })
          : err instanceof Error
            ? err.message
            : '';
      if (writeAttempted && previewMode === 'visual') {
        // v8 field mutations are not a transaction. Never roll back with a full document.
        // Re-read immediately so changing an already-applied value back remains a dirty edit.
        setDiffModalOpen(false);
        setRecoveryRequired(true);
        useConfigStore.getState().clearCache();
        showNotification(`${t('config_management.precise_save_incomplete')}: ${message}`, 'error');
        try {
          const latestYaml = await configFileApi.fetchConfigYaml();
          if (!operation.isCurrent()) return;
          recoverVisualDraft(latestYaml);
          if (
            readCommercialModeFromYaml(previewServerYaml) !== readCommercialModeFromYaml(latestYaml)
          ) {
            showNotification(t('notification.commercial_mode_restart_required'), 'warning');
          }
          if (hasTrustedProxiesChange(previewServerYaml, latestYaml)) {
            showNotification(t('notification.trusted_proxies_restart_required'), 'warning');
          }
        } catch (recoveryError) {
          if (!operation.isCurrent()) return;
          // Until readback succeeds, freeze editing against the stale baseline. Saving retries
          // recovery; reloading can explicitly replace the draft from the server.
          showNotification(
            recoveryError instanceof ConfigDraftConflictError
              ? t('config_management.concurrent_list_conflict', {
                  path: recoveryError.path.join('.'),
                })
              : t('config_management.precise_save_recovery_required'),
            'error'
          );
        }
        if (!operation.isCurrent()) return;
        try {
          await useConfigStore.getState().fetchConfig(true);
        } catch {
          // Keep the primary mutation/readback error; the cache is already invalidated.
        }
      } else {
        showNotification(`${t('notification.save_failed')}: ${message}`, 'error');
      }
    } finally {
      if (operation.isCurrent()) setSaving(false);
    }
  }, [
    applyVisualChangesToYaml,
    beginOperation,
    diffModalOpen,
    saving,
    loadVisualValuesFromYaml,
    mergedYaml,
    previewMode,
    previewPlan,
    recoverVisualDraft,
    sourceDirty,
    previewServerYaml,
    showNotification,
    t,
  ]);

  const handleSave = useCallback(async () => {
    if (saving || diffModalOpen) return;
    if (mode === 'visual' && sourceDirty) {
      showNotification(t('config_management.source_changes_before_visual'), 'warning');
      return;
    }
    if (mode === 'visual' && visualParseError) {
      showNotification(t('config_management.visual_mode_save_blocked'), 'error');
      return;
    }

    const operation = beginOperation();
    setSaving(true);
    try {
      const latestServerYaml = await configFileApi.fetchConfigYaml();
      if (!operation.isCurrent()) return;
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
      }

      // Generated source still merges visual edits onto fresh YAML; a real source draft is
      // preserved verbatim. The visible mode determines the wire protocol, not edit origin.
      const nextMergedYaml = recoveryRequired
        ? recoverVisualDraft(latestServerYaml)
        : buildConfigSaveDraft(
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

      const nextPlan =
        mode === 'visual' ? buildConfigPatch(latestServerYaml, nextMergedYaml) : null;
      if (nextPlan ? !hasConfigPatchChanges(nextPlan) : diffOriginal === nextMergedYaml) {
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
      setPreviewMode(mode);
      setPreviewPlan(nextPlan);
      previewRevision.current = operation.revision;
      setDiffModalOpen(true);
    } catch (err: unknown) {
      if (!operation.isCurrent()) return;
      const message =
        err instanceof ConfigDraftConflictError
          ? t('config_management.concurrent_list_conflict', { path: err.path.join('.') })
          : err instanceof Error
            ? err.message
            : '';
      showNotification(`${t('notification.save_failed')}: ${message}`, 'error');
    } finally {
      if (operation.isCurrent()) setSaving(false);
    }
  }, [
    applyVisualChangesToYaml,
    beginOperation,
    diffModalOpen,
    saving,
    content,
    sourceDirty,
    recoveryRequired,
    recoverVisualDraft,
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
    if (recoveryRequired) {
      handleReload();
      return;
    }

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
  }, [
    handleReload,
    isDirty,
    loadVisualValuesFromYaml,
    previewServerYaml,
    recoveryRequired,
    showConfirmation,
    t,
  ]);

  const closeDiff = useCallback(() => setDiffModalOpen(false), []);

  return {
    content,
    syncContentFromVisual,
    loading,
    saving,
    recoveryRequired,
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
