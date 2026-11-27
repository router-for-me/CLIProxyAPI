import { useCallback, useId, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { usePageTransitionLayer } from '@/components/common/PageTransitionLayer';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { Skeleton } from '@/components/ui/Skeleton';
import { useAuthStore, useNotificationStore } from '@/stores';
import { useProviderRecentRequests } from '@/components/providers/hooks/useProviderRecentRequests';
import { ProvidersHeader, type ProvidersHeaderSummary } from './components/ProvidersHeader';
import { BrandTabs } from './components/BrandTabs';
import { BrandPanel, type BrandPanelControls } from './components/BrandPanel';
import { ProviderPicker } from './components/ProviderPicker';
import { SponsorQuickStartPanel } from './components/SponsorQuickStartPanel';
import {
  ProviderSheet,
  type ProviderSheetHandle,
  type ProviderSheetState,
} from './sheets/ProviderSheet';
import {
  collectGroupModels,
  connectionCounts,
  getConnectionFormTarget,
  getResourceDisplayName,
  partitionProviderGroups,
  selectVisibleResources,
} from './resourceView';
import { APIKEY_FUN_DISPLAY_NAME } from './sponsor';
import { isSponsorPartialMutationError } from './sponsorMutationRecovery';
import { useProviderWorkbench } from './useProviderWorkbench';
import {
  getProviderFilterState,
  readProvidersWorkbenchUiState,
  writeProvidersWorkbenchUiState,
  type ProviderFilterState,
  type ProvidersWorkbenchUiState,
} from './uiState';
import { isNativeFamily, selectVisibleSections } from './nativeSections';
import type {
  NativeSection,
  ProviderBrand,
  ProviderResource,
  ProviderSortBy,
  SortDir,
} from './types';
import styles from './ProvidersWorkbenchPage.module.scss';

interface ProvidersWorkbenchPageProps {
  fixedBrand?: ProviderBrand;
}

const formatDateTime = (iso: string, locale?: string) => {
  try {
    const date = new Date(iso);
    if (Number.isNaN(date.getTime())) return iso;
    return new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(
      date
    );
  } catch {
    return iso;
  }
};

const errorMessageOf = (err: unknown): string => (err instanceof Error ? err.message : String(err));

export function ProvidersWorkbenchPage({ fixedBrand }: ProvidersWorkbenchPageProps = {}) {
  const { t, i18n } = useTranslation();
  const connectionStatus = useAuthStore((s) => s.connectionStatus);
  const { showNotification, showConfirmation } = useNotificationStore();
  const domId = useId();

  const pageTransitionLayer = usePageTransitionLayer();
  const isCurrentLayer = pageTransitionLayer ? pageTransitionLayer.status === 'current' : true;

  const workbench = useProviderWorkbench();
  const [uiState, setUiState] = useState<ProvidersWorkbenchUiState>(readProvidersWorkbenchUiState);
  const [sheetState, setSheetState] = useState<ProviderSheetState>({
    open: false,
    brand: 'gemini',
    mode: 'detail',
    resource: null,
  });
  const sheetRef = useRef<ProviderSheetHandle>(null);

  const connected = connectionStatus === 'connected';
  const { usageByProvider, refreshRecentRequests } = useProviderRecentRequests({
    enabled: connected,
  });

  const handleRefresh = useCallback(async () => {
    await Promise.allSettled([workbench.refetch(), refreshRecentRequests().catch(() => undefined)]);
  }, [refreshRecentRequests, workbench]);

  useHeaderRefresh(handleRefresh, isCurrentLayer);

  const disableMutations =
    !connected || workbench.mutating || workbench.isFetching || workbench.isError;

  const persistUiState = useCallback(
    (updater: (prev: ProvidersWorkbenchUiState) => ProvidersWorkbenchUiState) => {
      setUiState((prev) => {
        const next = updater(prev);
        writeProvidersWorkbenchUiState(next);
        return next;
      });
    },
    []
  );

  const allGroups = useMemo(() => workbench.snapshot?.groups ?? [], [workbench.snapshot]);
  const groups = useMemo(
    () =>
      fixedBrand
        ? allGroups.filter((group) => group.id === fixedBrand)
        : allGroups.filter((group) => group.id !== 'apikeyFun'),
    [allGroups, fixedBrand]
  );
  // Every brand stays in the strip (regular providers first, quick-connect partners after).
  const stripGroups = useMemo(() => {
    const { providers, quickFill } = partitionProviderGroups(groups);
    return [...providers, ...quickFill];
  }, [groups]);
  const connectedGroups = useMemo(
    () => groups.filter((group) => group.resources.length > 0 || group.nativeSections?.length),
    [groups]
  );
  const activeBrand: ProviderBrand =
    fixedBrand ??
    (groups.some((group) => group.id === uiState.activeBrand)
      ? uiState.activeBrand
      : (connectedGroups[0]?.id ?? stripGroups[0]?.id ?? 'gemini'));
  const activeGroup = groups.find((group) => group.id === activeBrand) ?? groups[0] ?? null;
  const partnerBrands = useMemo(
    () =>
      partitionProviderGroups(groups)
        .quickFill.filter((group) => group.resources.length === 0)
        .map((group) => group.id),
    [groups]
  );
  const activeFilterState = getProviderFilterState(uiState, activeBrand);

  const updateActiveFilterState = useCallback(
    (patch: Partial<ProviderFilterState>) => {
      persistUiState((prev) => ({
        ...prev,
        filtersByBrand: {
          ...prev.filtersByBrand,
          [activeBrand]: { ...getProviderFilterState(prev, activeBrand), ...patch },
        },
      }));
    },
    [activeBrand, persistUiState]
  );

  const availableModels = useMemo(() => collectGroupModels(activeGroup), [activeGroup]);

  const selectedModels = useMemo(() => {
    const available = new Set(availableModels);
    return new Set(activeFilterState.selectedModels.filter((name) => available.has(name)));
  }, [activeFilterState.selectedModels, availableModels]);

  const { visibleResources, nativeSections } = useMemo(() => {
    const query = { ...activeFilterState, selectedModels };
    if (activeGroup?.nativeSections) {
      const sections = selectVisibleSections(activeGroup.nativeSections, query, usageByProvider);
      return {
        nativeSections: sections,
        visibleResources: sections.flatMap((section) => section.members),
      };
    }
    return {
      visibleResources: selectVisibleResources(
        activeGroup?.resources ?? [],
        query,
        usageByProvider
      ),
    };
  }, [activeGroup, activeFilterState, selectedModels, usageByProvider]);

  const panelControls = useMemo<BrandPanelControls>(
    () => ({
      filter: activeFilterState.filter,
      onFilterChange: (value: string) => updateActiveFilterState({ filter: value }),
      sortBy: activeFilterState.sortBy,
      sortDir: activeFilterState.sortDir,
      onSortBy: (value: ProviderSortBy) => updateActiveFilterState({ sortBy: value }),
      onSortDir: (value: SortDir) => updateActiveFilterState({ sortDir: value }),
      availableModels,
      selectedModels,
      onSelectedModelsChange: (next: Set<string>) =>
        updateActiveFilterState({
          selectedModels: Array.from(next).sort((a, b) => a.localeCompare(b)),
        }),
      onClearFilters: () => updateActiveFilterState({ filter: '', selectedModels: [] }),
    }),
    [activeFilterState, availableModels, selectedModels, updateActiveFilterState]
  );

  const summary = useMemo<ProvidersHeaderSummary>(
    () => ({
      totalResources: groups.reduce((sum, group) => sum + connectionCounts(group).total, 0),
      totalActive: groups.reduce((sum, group) => sum + connectionCounts(group).active, 0),
      providerFamilies: groups.filter(
        (group) => group.resources.length > 0 || group.nativeSections?.length
      ).length,
      updatedAtLabel: workbench.snapshot
        ? formatDateTime(workbench.snapshot.fetchedAt, i18n.language)
        : t('providersPage.modelCatalog.notLoaded'),
    }),
    [groups, i18n.language, t, workbench.snapshot]
  );

  const isQuickStart = fixedBrand === 'apikeyFun';
  const quickStartResource =
    isQuickStart && activeGroup ? (activeGroup.resources[0] ?? null) : null;
  const headerTitle = isQuickStart
    ? quickStartResource
      ? APIKEY_FUN_DISPLAY_NAME
      : t('nav.quick_start')
    : t('providersPage.header.title');
  const openConnectionForm = useCallback(
    (brand: ProviderBrand, fromPicker: boolean) => {
      const group = groups.find((item) => item.id === brand);
      if (!group) return;
      setSheetState({ open: true, brand, fromPicker, ...getConnectionFormTarget(group) });
    },
    [groups]
  );
  const openCreate = () => openConnectionForm(activeBrand, false);

  const openPicker = useCallback(() => {
    setSheetState((state) => ({
      open: true,
      brand: state.brand,
      mode: 'pick',
      resource: null,
      fromPicker: false,
    }));
  }, []);

  const backToPicker = useCallback(() => {
    setSheetState((state) => ({ ...state, mode: 'pick', fromPicker: false }));
  }, []);

  const openView = useCallback((resource: ProviderResource) => {
    setSheetState({ open: true, brand: resource.brand, mode: 'detail', resource });
  }, []);

  const openEdit = useCallback((resource: ProviderResource) => {
    setSheetState({ open: true, brand: resource.brand, mode: 'edit', resource });
  }, []);

  const closeSheet = useCallback(() => {
    setSheetState((state) => ({ ...state, open: false }));
  }, []);

  const selectBrand = useCallback(
    (brand: ProviderBrand) => {
      const isSwitching = sheetState.open && sheetState.brand !== brand;
      const proceed =
        isSwitching && sheetRef.current
          ? sheetRef.current.confirmDiscardIfDirty()
          : Promise.resolve(true);
      void proceed.then((ok) => {
        if (!ok) return;
        persistUiState((prev) =>
          prev.activeBrand === brand ? prev : { ...prev, activeBrand: brand }
        );
        if (isSwitching) closeSheet();
      });
    },
    [closeSheet, persistUiState, sheetState.brand, sheetState.open]
  );

  const handleDelete = useCallback(
    (resource: ProviderResource) => {
      showConfirmation({
        title: t('providersPage.delete.title'),
        message: t('providersPage.delete.confirm', {
          name: getResourceDisplayName(resource),
        }),
        variant: 'danger',
        confirmText: t('providersPage.actions.delete'),
        onConfirm: async () => {
          try {
            await workbench.deleteProvider(resource);
            showNotification(t('providersPage.toast.deleted'), 'success');
          } catch (err) {
            if (isSponsorPartialMutationError(err)) {
              showNotification(t('providersPage.sponsor.partialMutationWarning'), 'warning');
              return;
            }
            showNotification(`${t('notification.delete_failed')}: ${errorMessageOf(err)}`, 'error');
          }
        },
      });
    },
    [showConfirmation, showNotification, t, workbench]
  );

  const deleteGroup = (section: NativeSection) => {
    const brand = sheetState.brand;
    if (!isNativeFamily(brand)) return;
    showConfirmation({
      title: t('providersPage.groupActions.delete'),
      message: t('providersPage.groupActions.confirm', {
        name: section.name,
        count: section.keyCount,
      }),
      variant: 'danger',
      confirmText: t('providersPage.actions.delete'),
      onConfirm: async () => {
        try {
          await workbench.deleteNativeConnection(brand, section.source);
          finishSave();
          showNotification(t('providersPage.toast.deleted'), 'success');
        } catch (err) {
          showNotification(`${t('notification.delete_failed')}: ${errorMessageOf(err)}`, 'error');
        }
      },
    });
  };

  const toggleGroup = async (section: NativeSection, disabled: boolean) => {
    if (!isNativeFamily(activeBrand)) return;
    try {
      await workbench.setConnectionDisabled(activeBrand, section, disabled);
      showNotification(
        t(disabled ? 'providersPage.toast.disabled' : 'providersPage.toast.enabled'),
        'success'
      );
    } catch (err) {
      showNotification(`${t('providersPage.toast.toggleFailed')}: ${errorMessageOf(err)}`, 'error');
    }
  };

  const finishSave = useCallback(() => {
    setSheetState((current) => (current === sheetState ? { ...current, open: false } : current));
  }, [sheetState]);

  const handleToggleDisabled = useCallback(
    async (resource: ProviderResource, disabled: boolean) => {
      try {
        await workbench.toggleDisabled(resource, disabled);
        showNotification(
          disabled ? t('providersPage.toast.disabled') : t('providersPage.toast.enabled'),
          'success'
        );
      } catch (err) {
        if (isSponsorPartialMutationError(err)) {
          showNotification(t('providersPage.sponsor.partialMutationWarning'), 'warning');
          return;
        }
        showNotification(
          `${t('providersPage.toast.toggleFailed')}: ${errorMessageOf(err)}`,
          'error'
        );
      }
    },
    [showNotification, t, workbench]
  );

  const handleCreated = useCallback(() => {
    showNotification(t('providersPage.toast.created'), 'success');
    // Land on the brand that was just connected so the new card is in view.
    const createdBrand = sheetState.brand;
    persistUiState((prev) =>
      prev.activeBrand === createdBrand ? prev : { ...prev, activeBrand: createdBrand }
    );
    finishSave();
  }, [finishSave, persistUiState, sheetState.brand, showNotification, t]);

  const handleUpdated = useCallback(() => {
    showNotification(t('providersPage.toast.updated'), 'success');
    finishSave();
  }, [finishSave, showNotification, t]);

  const errorBanner = workbench.errorMessage ? (
    <div className={styles.errorBanner} role="alert">
      {workbench.errorMessage}
    </div>
  ) : null;

  if (!workbench.snapshot && workbench.isPending) {
    return (
      <div className={styles.page} aria-busy="true">
        <div className={styles.skeletonHeader}>
          <Skeleton width={220} height={34} />
          <Skeleton width={320} height={16} />
        </div>
        <Skeleton height={40} />
        <div className={styles.skeletonGrid}>
          {[0, 1, 2].map((key) => (
            <Skeleton key={key} height={236} />
          ))}
        </div>
      </div>
    );
  }

  const header = (
    <ProvidersHeader
      title={headerTitle}
      summary={isQuickStart ? undefined : summary}
      isFetching={workbench.isFetching}
      onRefresh={() => void handleRefresh()}
      newLabel={fixedBrand ? undefined : t('providersPage.actions.addProvider')}
      newDisabled={disableMutations}
      onNew={openPicker}
    />
  );

  const sheet = (
    <ProviderSheet
      ref={sheetRef}
      state={sheetState}
      onClose={closeSheet}
      onSwitchToEdit={() => {
        setSheetState((state) => (state.resource ? { ...state, mode: 'edit' } : state));
      }}
      workbench={workbench}
      onDeleteConnection={deleteGroup}
      onCreated={handleCreated}
      onUpdated={handleUpdated}
      mutationDisabled={disableMutations}
      usageByProvider={usageByProvider}
      pickerGroups={groups}
      onPick={(brand) => openConnectionForm(brand, true)}
      onBackToPicker={backToPicker}
    />
  );

  if (!fixedBrand && groups.length > 0 && connectedGroups.length === 0) {
    // First run: nothing connected yet, so the catalogue itself is the page.
    return (
      <div className={styles.page}>
        {header}
        {errorBanner}
        <section className={styles.onboarding} aria-labelledby={`${domId}-onboarding`}>
          <h2 id={`${domId}-onboarding`} className={styles.onboardingTitle}>
            {t('providersPage.picker.onboardingTitle')}
          </h2>
          <ProviderPicker
            groups={groups}
            variant="page"
            onPick={(brand) => openConnectionForm(brand, false)}
          />
        </section>
        {sheet}
      </div>
    );
  }

  if (!activeGroup) {
    return (
      <div className={styles.page}>
        {header}
        {errorBanner}
      </div>
    );
  }

  if (isQuickStart) {
    return (
      <div className={styles.page}>
        {header}
        {errorBanner}
        <div className={styles.quickStartStack}>
          <SponsorQuickStartPanel
            resource={quickStartResource}
            workbench={workbench}
            mutationDisabled={disableMutations}
          />
        </div>
      </div>
    );
  }

  const tabId = (brand: ProviderBrand) => `${domId}-tab-${brand}`;
  const panelId = `${domId}-panel`;

  return (
    <div className={styles.page}>
      {header}
      {errorBanner}

      <div className={styles.workbench}>
        {!fixedBrand ? (
          <BrandTabs
            groups={stripGroups}
            activeBrand={activeGroup.id}
            onSelect={selectBrand}
            tabId={tabId}
            panelId={panelId}
            onAdd={openPicker}
            partnerBrands={partnerBrands}
            addDisabled={disableMutations}
          />
        ) : null}
        <BrandPanel
          id={panelId}
          labelledBy={tabId(activeGroup.id)}
          group={activeGroup}
          resources={visibleResources}
          nativeSections={nativeSections}
          selectedGroupIndex={
            sheetState.open && sheetState.brand === activeBrand
              ? (sheetState.section?.source.groupIndex ?? null)
              : null
          }
          onToggleGroup={toggleGroup}
          onEditGroup={(section) =>
            setSheetState({
              open: true,
              brand: activeBrand,
              mode: 'edit',
              section,
              resource: null,
            })
          }
          controls={panelControls}
          selectedId={sheetState.open ? (sheetState.resource?.id ?? null) : null}
          disableMutations={disableMutations}
          usageByProvider={usageByProvider}
          onView={openView}
          onEdit={openEdit}
          onDelete={handleDelete}
          onToggleDisabled={(resource, disabled) => void handleToggleDisabled(resource, disabled)}
          onCreate={openCreate}
        />
      </div>

      {sheet}
    </div>
  );
}
