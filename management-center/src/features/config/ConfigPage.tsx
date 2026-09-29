import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useLocation, useNavigate } from 'react-router-dom';
import { usePageTransitionLayer } from '@/components/common/PageTransitionLayer';
import { useMediaQuery } from '@/hooks/useMediaQuery';
import { useRevealGroup } from '@/hooks/motion';
import { useUnsavedChangesGuard } from '@/hooks/useUnsavedChangesGuard';
import { useVisualConfig } from '@/hooks/useVisualConfig';
import { useAuthStore, useNotificationStore, useThemeStore } from '@/stores';
import {
  CONFIG_MODE_STORAGE_KEY,
  CONFIG_SECTION_STORAGE_KEY,
  LEGACY_EDITOR_MODE_STORAGE_KEY,
  configPanelDomId,
  configTabDomId,
  type ConfigEditorMode,
  type ConfigTabId,
} from './constants';
import {
  CONFIG_FIELD_COUNT,
  buildHeaderMeta,
  countSectionErrors,
  countTotalErrors,
  readSavedMode,
  readSavedSection,
  resolveDirtyTabs,
  resolveStatus,
} from './uiState';
import { findConfigFieldById } from './searchIndex';
import { shouldReloadVisualDraft, useConfigDocument } from './hooks/useConfigDocument';
import { useFieldJump } from './hooks/useFieldJump';
import { useSourceSearch } from './hooks/useSourceSearch';
import { ConfigHeader } from './components/ConfigHeader';
import { ConfigSearch } from './components/ConfigSearch';
import { ConfigTabs } from './components/ConfigTabs';
import { DiffModal } from './components/DiffModal';
import { FloatingSaveBar } from './components/FloatingSaveBar';
import { ModeSwitch } from './components/ModeSwitch';
import { SourcePanel, SourceSearchBar } from './components/SourcePanel';
import { SectionAdvanced } from './components/sections/SectionAdvanced';
import { SectionCommon } from './components/sections/SectionCommon';
import { SectionConnectivity } from './components/sections/SectionConnectivity';
import { SectionLogging } from './components/sections/SectionLogging';
import { SectionNetwork } from './components/sections/SectionNetwork';
import { SectionPayload } from './components/sections/SectionPayload';
import { SectionQuota } from './components/sections/SectionQuota';
import { SectionStreaming } from './components/sections/SectionStreaming';
import styles from './ConfigPage.module.scss';

/** 首载入场预算：卡片延迟 0.28s + 0.45s 动画，之后关闭 animateIn，切 tab 不再重播。 */
const ENTRANCE_BUDGET_MS = 800;

export function ConfigPage() {
  const { t } = useTranslation();
  const location = useLocation();
  const navigate = useNavigate();
  const requestedFieldEntry = useMemo(() => {
    const fieldId = new URLSearchParams(location.search).get('field');
    return findConfigFieldById(fieldId);
  }, [location.search]);
  const pageTransitionLayer = usePageTransitionLayer();
  const isCurrentLayer = pageTransitionLayer ? pageTransitionLayer.isCurrentLayer : true;
  const showNotification = useNotificationStore((state) => state.showNotification);
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const isMobile = useMediaQuery('(max-width: 768px)');
  const revealRef = useRevealGroup<HTMLDivElement>();

  const {
    visualValues,
    visualDirty,
    visualDirtyFields,
    visualParseError,
    visualValidationErrors,
    visualHasPayloadValidationErrors,
    loadVisualValuesFromYaml,
    applyVisualChangesToYaml,
    setVisualValues,
  } = useVisualConfig();

  const [mode, setMode] = useState<ConfigEditorMode>(() =>
    requestedFieldEntry ? 'visual' : readSavedMode(localStorage.getItem(CONFIG_MODE_STORAGE_KEY))
  );
  const [activeSection, setActiveSection] = useState<ConfigTabId>(
    () =>
      requestedFieldEntry?.sectionId ??
      readSavedSection(localStorage.getItem(CONFIG_SECTION_STORAGE_KEY))
  );
  const handledRequestedFieldRef = useRef<string | null>(null);
  // 首载入场：挂载后一个预算周期内为 true；此后切 tab 新挂载的卡片不再播入场。
  const [animateCards, setAnimateCards] = useState(true);
  useEffect(() => {
    const timer = window.setTimeout(() => setAnimateCards(false), ENTRANCE_BUDGET_MS);
    return () => window.clearTimeout(timer);
  }, []);

  // 旧「简单/完整」双模式已退役，清掉遗留的持久化键。
  useEffect(() => {
    localStorage.removeItem(LEGACY_EDITOR_MODE_STORAGE_KEY);
  }, []);

  const doc = useConfigDocument({
    mode,
    visualDirty,
    visualParseError,
    loadVisualValuesFromYaml,
    applyVisualChangesToYaml,
  });
  const sourceSearch = useSourceSearch();

  const disableControls = connectionStatus !== 'connected';
  const hasVisualModeError = !!visualParseError;
  const hasVisualValidationErrors =
    mode === 'visual' &&
    (Object.values(visualValidationErrors).some(Boolean) || visualHasPayloadValidationErrors);

  const unsavedChangesDialog = useMemo(
    () => ({
      title: t('common.unsaved_changes_title'),
      message: t('common.unsaved_changes_message'),
      confirmText: t('common.confirm'),
      cancelText: t('common.cancel'),
    }),
    [t]
  );

  useUnsavedChangesGuard({
    enabled: isCurrentLayer,
    shouldBlock: doc.isDirty,
    dialog: unsavedChangesDialog,
  });

  // YAML 解析失败：切换到源码模式；修复后仍可重试进入可视化模式。
  useEffect(() => {
    if (mode !== 'visual' || !visualParseError) return;

    setMode('source');
    localStorage.setItem(CONFIG_MODE_STORAGE_KEY, 'source');
    showNotification(
      t('config_management.visual_mode_unavailable_detail', { message: visualParseError }),
      'error'
    );
  }, [mode, showNotification, t, visualParseError]);

  // 可视化 ↔ 源码切换的 dirty 交接：
  // → 源码：物化可视化脏字段供查看，但不把同步动作记作用户源码编辑；
  // → 可视化：真正的源码草稿需重新解析；纯模式往返保留字段级 dirty 和并发合并策略。
  const handleModeChange = useCallback(
    (nextMode: ConfigEditorMode) => {
      if (nextMode === mode) return;

      if (nextMode === 'source') {
        if (visualDirty) {
          const nextContent = applyVisualChangesToYaml(doc.content);
          if (nextContent !== doc.content) {
            doc.syncContentFromVisual(nextContent);
          }
        }
      } else if (shouldReloadVisualDraft(doc.sourceDirty, visualParseError)) {
        const result = loadVisualValuesFromYaml(doc.content);
        if (!result.ok) {
          showNotification(
            t('config_management.visual_mode_unavailable_detail', { message: result.error }),
            'error'
          );
          return;
        }
      }

      setMode(nextMode);
      localStorage.setItem(CONFIG_MODE_STORAGE_KEY, nextMode);
    },
    [
      applyVisualChangesToYaml,
      doc,
      loadVisualValuesFromYaml,
      mode,
      showNotification,
      t,
      visualDirty,
      visualParseError,
    ]
  );

  const handleSectionChange = useCallback((sectionId: ConfigTabId) => {
    setActiveSection(sectionId);
    localStorage.setItem(CONFIG_SECTION_STORAGE_KEY, sectionId);
  }, []);

  const { jumpToField } = useFieldJump({
    values: visualValues,
    setActiveSection: handleSectionChange,
  });

  useEffect(() => {
    if (!requestedFieldEntry || handledRequestedFieldRef.current === requestedFieldEntry.fieldId) {
      return;
    }

    handledRequestedFieldRef.current = requestedFieldEntry.fieldId;
    localStorage.setItem(CONFIG_MODE_STORAGE_KEY, 'visual');
    jumpToField(requestedFieldEntry);

    const nextSearchParams = new URLSearchParams(location.search);
    nextSearchParams.delete('field');
    const nextSearch = nextSearchParams.toString();
    void navigate(
      {
        pathname: location.pathname,
        search: nextSearch ? `?${nextSearch}` : '',
        hash: location.hash,
      },
      { replace: true }
    );
  }, [
    jumpToField,
    location.hash,
    location.pathname,
    location.search,
    navigate,
    requestedFieldEntry,
  ]);

  const errorCounts = useMemo(
    () => countSectionErrors(visualValidationErrors, visualHasPayloadValidationErrors),
    [visualHasPayloadValidationErrors, visualValidationErrors]
  );
  const dirtyTabs = useMemo(() => resolveDirtyTabs(visualDirtyFields), [visualDirtyFields]);
  const totalErrors = useMemo(
    () => countTotalErrors(visualValidationErrors, visualHasPayloadValidationErrors),
    [visualHasPayloadValidationErrors, visualValidationErrors]
  );

  const status = resolveStatus({
    disconnected: disableControls,
    loading: doc.loading,
    loadFailed: Boolean(doc.error),
    yamlError: hasVisualModeError,
    validationBlocked: hasVisualValidationErrors,
    saving: doc.saving,
    dirty: doc.isDirty,
  });
  const headerMeta = buildHeaderMeta({
    fieldCount: CONFIG_FIELD_COUNT,
    status,
    dirtyCount: visualDirtyFields.size,
    sourceDirty: doc.sourceDirty,
    errorCount: mode === 'visual' ? totalErrors : 0,
  });

  const saveDisabled =
    disableControls ||
    doc.loading ||
    doc.saving ||
    !doc.isDirty ||
    doc.diffModalOpen ||
    hasVisualModeError ||
    hasVisualValidationErrors;

  const sectionProps = {
    values: visualValues,
    validationErrors: visualValidationErrors,
    disabled: disableControls || doc.loading,
    animateIn: animateCards,
    onChange: setVisualValues,
  };

  const renderActiveSection = () => {
    switch (activeSection) {
      case 'common':
        return <SectionCommon {...sectionProps} />;
      case 'connectivity':
        return <SectionConnectivity {...sectionProps} />;
      case 'network':
        return <SectionNetwork {...sectionProps} />;
      case 'logging':
        return <SectionLogging {...sectionProps} />;
      case 'quota':
        return <SectionQuota {...sectionProps} />;
      case 'streaming':
        return <SectionStreaming {...sectionProps} />;
      case 'advanced':
        return <SectionAdvanced {...sectionProps} />;
      case 'payload':
        return (
          <SectionPayload
            {...sectionProps}
            hasPayloadValidationErrors={visualHasPayloadValidationErrors}
          />
        );
    }
  };

  return (
    <div className={styles.page} ref={revealRef}>
      <ConfigHeader
        meta={headerMeta}
        reloadDisabled={doc.loading || doc.saving}
        reloading={doc.loading}
        onReload={doc.handleReload}
      />

      {doc.error && (
        <div className="error-box" role="alert">
          {doc.error}
        </div>
      )}
      {!doc.error && visualParseError && (
        <div className="error-box" role="alert">
          {t('config_management.visual_mode_unavailable_detail', { message: visualParseError })}
        </div>
      )}

      <div className={styles.toolbar} data-reveal>
        {mode === 'visual' ? (
          <ConfigSearch disabled={disableControls || doc.loading} onJump={jumpToField} />
        ) : (
          <SourceSearchBar search={sourceSearch} disabled={disableControls || doc.loading} />
        )}
        <ModeSwitch mode={mode} disabled={doc.saving || doc.loading} onChange={handleModeChange} />
      </div>

      {mode === 'visual' ? (
        <>
          <div className={styles.tabsRow} data-reveal>
            <ConfigTabs
              active={activeSection}
              errorCounts={errorCounts}
              dirtyTabs={dirtyTabs}
              disabled={doc.saving || doc.loading}
              onChange={handleSectionChange}
            />
          </div>
          <div
            className={styles.panel}
            role="tabpanel"
            id={configPanelDomId(activeSection)}
            aria-labelledby={configTabDomId(activeSection)}
          >
            {renderActiveSection()}
          </div>
        </>
      ) : (
        <SourcePanel
          search={sourceSearch}
          value={doc.content}
          onChange={doc.handleChange}
          theme={resolvedTheme}
          editable={!disableControls && !doc.loading}
        />
      )}

      <FloatingSaveBar
        visible={isCurrentLayer && doc.isDirty}
        statusText={t(isMobile ? status.shortLabelKey : status.labelKey)}
        statusTone={status.tone}
        saving={doc.saving}
        saveDisabled={saveDisabled}
        discardDisabled={doc.loading || doc.saving}
        onSave={doc.handleSave}
        onDiscard={doc.handleDiscard}
      />

      <DiffModal
        open={doc.diffModalOpen}
        original={doc.serverYaml}
        modified={doc.mergedYaml}
        onConfirm={doc.handleConfirmSave}
        onCancel={doc.closeDiff}
        loading={doc.saving}
      />
    </div>
  );
}
