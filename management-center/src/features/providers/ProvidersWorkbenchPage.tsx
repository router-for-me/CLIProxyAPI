import { useCallback, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { usePageTransitionLayer } from '@/components/common/PageTransitionLayer';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { Skeleton } from '@/components/ui/Skeleton';
import { useAuthStore, useNotificationStore } from '@/stores';
import { useProviderRecentRequests } from '@/components/providers/hooks/useProviderRecentRequests';
import {
  getOpenAIProviderRecentWindowStats,
  getProviderRecentWindowStats,
  getProviderUsageKey,
  type ProviderRecentUsageMap,
} from '@/components/providers/utils';
import type { OpenAIProviderConfig } from '@/types';
import { ProviderHeaderCard } from './components/ProviderHeaderCard';
import { ProviderCategoryList } from './components/ProviderCategoryList';
import { ProviderResourcePanel } from './components/ProviderResourcePanel';
import type { ProviderPanelControls } from './components/ProviderResourcePanel';
import { SponsorQuickStartPanel } from './components/SponsorQuickStartPanel';
import { ProviderSheet, type ProviderSheetHandle } from './sheets/ProviderSheet';
import { APIKEY_FUN_DISPLAY_NAME } from './sponsor';
import { isMultiProtocolSponsorBrand } from './sponsorDefinitions';
import { isSponsorPartialMutationError } from './sponsorMutationRecovery';
import { useProviderWorkbench } from './useProviderWorkbench';
import {
  getProviderFilterState,
  readProvidersWorkbenchUiState,
  writeProvidersWorkbenchUiState,
  type ProviderFilterState,
  type ProvidersWorkbenchUiState,
} from './uiState';
import type { ProviderBrand, ProviderResource, ProviderSortBy, SortDir } from './types';
import styles from './ProvidersWorkbenchPage.module.scss';

type SheetMode = 'detail' | 'create' | 'edit';

interface SheetState {
  open: boolean;
  brand: ProviderBrand;
  mode: SheetMode;
  resource: ProviderResource | null;
}

interface ProvidersWorkbenchPageProps {
  fixedBrand?: ProviderBrand;
}

const formatDateTime = (iso: string, locale?: string) => {
  try {
    const date = new Date(iso);
    if (Number.isNaN(date.getTime())) return iso;
    return new Intl.DateTimeFormat(locale, {
      dateStyle: 'medium',
      timeStyle: 'short',
    }).format(date);
  } catch {
    return iso;
  }
};

const matchesFilter = (r: ProviderResource, normalized: string): boolean => {
  if (!normalized) return true;
  const haystack = [
    r.identifier,
    r.name,
    r.authIndex,
    r.apiKeyPreview,
    r.apiKey,
    r.baseUrl,
    r.proxyUrl,
    r.prefix,
  ]
    .filter(Boolean)
    .map((v) => String(v).toLowerCase());
  return haystack.some((v) => v.includes(normalized));
};

const getResourceSortName = (resource: ProviderResource): string =>
  (resource.name ?? resource.identifier ?? resource.apiKeyPreview ?? '').toLowerCase();

const getResourceRecentSuccess = (
  resource: ProviderResource,
  usageByProvider: ProviderRecentUsageMap
): number => {
  if (isMultiProtocolSponsorBrand(resource.brand)) {
    return 0;
  }
  if (resource.brand === 'openaiCompatibility') {
    return getOpenAIProviderRecentWindowStats(resource.raw as OpenAIProviderConfig, usageByProvider)
      .success;
  }
  return getProviderRecentWindowStats(
    usageByProvider,
    getProviderUsageKey(resource.brand),
    resource.apiKey ?? undefined,
    resource.baseUrl ?? undefined
  ).success;
};

export function ProvidersWorkbenchPage({ fixedBrand }: ProvidersWorkbenchPageProps = {}) {
  const { t, i18n } = useTranslation();
  const connectionStatus = useAuthStore((s) => s.connectionStatus);
  const { showNotification, showConfirmation } = useNotificationStore();

  const pageTransitionLayer = usePageTransitionLayer();
  const isCurrentLayer = pageTransitionLayer ? pageTransitionLayer.status === 'current' : true;

  const workbench = useProviderWorkbench();
  const [uiState, setUiState] = useState<ProvidersWorkbenchUiState>(readProvidersWorkbenchUiState);
  const [sheetState, setSheetState] = useState<SheetState>({
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
    connectionStatus !== 'connected' ||
    workbench.mutating ||
    workbench.isFetching ||
    workbench.isError;

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

  const setActiveBrand = useCallback(
    (brand: ProviderBrand) => {
      persistUiState((prev) =>
        prev.activeBrand === brand ? prev : { ...prev, activeBrand: brand }
      );
    },
    [persistUiState]
  );

  const allGroups = useMemo(() => workbench.snapshot?.groups ?? [], [workbench.snapshot]);
  const groups = useMemo(
    () =>
      fixedBrand
        ? allGroups.filter((group) => group.id === fixedBrand)
        : allGroups.filter((group) => group.id !== 'apikeyFun'),
    [allGroups, fixedBrand]
  );
  const firstVisibleBrand = groups[0]?.id ?? fixedBrand ?? 'gemini';
  const activeBrand =
    fixedBrand ??
    (groups.some((group) => group.id === uiState.activeBrand)
      ? uiState.activeBrand
      : firstVisibleBrand);
  const activeFilterState = getProviderFilterState(uiState, activeBrand);
  const filter = activeFilterState.filter;
  const providerSortBy = activeFilterState.sortBy;
  const providerSortDir = activeFilterState.sortDir;
  const activeGroup = groups.find((g) => g.id === activeBrand) ?? groups[0] ?? null;

  const updateActiveFilterState = useCallback(
    (patch: Partial<ProviderFilterState>) => {
      persistUiState((prev) => {
        const current = getProviderFilterState(prev, activeBrand);
        return {
          ...prev,
          filtersByBrand: {
            ...prev.filtersByBrand,
            [activeBrand]: {
              ...current,
              ...patch,
            },
          },
        };
      });
    },
    [activeBrand, persistUiState]
  );

  const filteredResources = useMemo(() => {
    if (!activeGroup) return [];
    const normalized = filter.trim().toLowerCase();
    return activeGroup.resources.filter((r) => matchesFilter(r, normalized));
  }, [activeGroup, filter]);

  const availableModels = useMemo(() => {
    if (!activeGroup) return [];
    const seen = new Set<string>();
    activeGroup.resources.forEach((r) => {
      r.models.forEach((name) => seen.add(name));
    });
    return Array.from(seen).sort();
  }, [activeGroup]);

  const selectedModels = useMemo(() => {
    if (availableModels.length === 0) return new Set<string>();
    const availableModelSet = new Set(availableModels);
    return new Set(activeFilterState.selectedModels.filter((name) => availableModelSet.has(name)));
  }, [activeFilterState.selectedModels, availableModels]);

  const visibleResources = useMemo(() => {
    let arr = filteredResources;
    if (selectedModels.size > 0) {
      arr = arr.filter((r) => r.models.some((name) => selectedModels.has(name)));
    }

    const sorted = [...arr].sort((a, b) => {
      const sortDiff =
        providerSortBy === 'name'
          ? getResourceSortName(a).localeCompare(getResourceSortName(b))
          : providerSortBy === 'priority'
            ? a.priority - b.priority
            : getResourceRecentSuccess(a, usageByProvider) -
              getResourceRecentSuccess(b, usageByProvider);
      const diff = sortDiff || a.originalIndex - b.originalIndex;
      return providerSortDir === 'asc' ? diff : -diff;
    });

    return sorted;
  }, [filteredResources, providerSortBy, providerSortDir, selectedModels, usageByProvider]);

  const toolbarControls = useMemo<ProviderPanelControls | undefined>(() => {
    if (!activeGroup) return undefined;
    return {
      sortBy: providerSortBy,
      sortDir: providerSortDir,
      onSortBy: (value: ProviderSortBy) => updateActiveFilterState({ sortBy: value }),
      onSortDir: (value: SortDir) => updateActiveFilterState({ sortDir: value }),
      availableModels,
      selectedModels,
      onSelectedModelsChange: (next) =>
        updateActiveFilterState({
          selectedModels: Array.from(next).sort((a, b) => a.localeCompare(b)),
        }),
    };
  }, [
    activeGroup,
    availableModels,
    providerSortBy,
    providerSortDir,
    selectedModels,
    updateActiveFilterState,
  ]);

  const totalResources = useMemo(
    () => groups.reduce((sum, g) => sum + g.resources.length, 0),
    [groups]
  );

  const totalActive = useMemo(
    () => groups.reduce((sum, g) => sum + g.resources.filter((r) => !r.disabled).length, 0),
    [groups]
  );

  const providerFamilies = useMemo(
    () => groups.filter((g) => g.resources.length > 0).length,
    [groups]
  );
  const quickStartResource = useMemo(
    () =>
      fixedBrand === 'apikeyFun' && activeGroup ? (activeGroup.resources[0] ?? null) : null,
    [activeGroup, fixedBrand]
  );

  const updatedAtLabel = workbench.snapshot
    ? formatDateTime(workbench.snapshot.fetchedAt, i18n.language)
    : t('providersPage.modelCatalog.notLoaded');
  const headerTitle =
    fixedBrand === 'apikeyFun'
      ? quickStartResource
        ? APIKEY_FUN_DISPLAY_NAME
        : t('nav.quick_start')
      : undefined;
  const errorBanner = workbench.errorMessage ? (
    <div className="error-box">{workbench.errorMessage}</div>
  ) : null;

  const openCreate = useCallback(() => {
    const brand = activeBrand;
    setSheetState({ open: true, brand, mode: 'create', resource: null });
  }, [activeBrand]);

  const openView = useCallback((resource: ProviderResource) => {
    setSheetState({
      open: true,
      brand: resource.brand,
      mode: 'detail',
      resource,
    });
  }, []);

  const openEdit = useCallback((resource: ProviderResource) => {
    setSheetState({
      open: true,
      brand: resource.brand,
      mode: 'edit',
      resource,
    });
  }, []);

  const closeSheet = useCallback(() => {
    setSheetState((s) => ({ ...s, open: false }));
  }, []);

  const handleDelete = useCallback(
    (resource: ProviderResource) => {
      const name = resource.name ?? resource.apiKeyPreview ?? resource.identifier ?? '';
      showConfirmation({
        title: t('providersPage.delete.title'),
        message: t('providersPage.delete.confirm', { name }),
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
            const msg = err instanceof Error ? err.message : String(err);
            showNotification(`${t('notification.delete_failed')}: ${msg}`, 'error');
          }
        },
      });
    },
    [showConfirmation, showNotification, t, workbench]
  );

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
        const msg = err instanceof Error ? err.message : String(err);
        showNotification(`${t('providersPage.toast.toggleFailed')}: ${msg}`, 'error');
      }
    },
    [showNotification, t, workbench]
  );

  const handleCreated = useCallback(() => {
    showNotification(t('providersPage.toast.created'), 'success');
    closeSheet();
  }, [closeSheet, showNotification, t]);

  const handleUpdated = useCallback(() => {
    showNotification(t('providersPage.toast.updated'), 'success');
    closeSheet();
  }, [closeSheet, showNotification, t]);

  // 加载状态
  if (!workbench.snapshot && workbench.isPending) {
    return (
      <div className={styles.page}>
        <Skeleton height={120} />
        <div className={styles.layout}>
          <Skeleton height={420} />
          <Skeleton height={420} />
        </div>
      </div>
    );
  }

  if (!activeGroup) {
    return (
      <div className={styles.page}>
        <ProviderHeaderCard
          title={headerTitle}
          totalActive={0}
          totalResources={0}
          providerFamilies={0}
          updatedAtLabel={updatedAtLabel}
          isFetching={workbench.isFetching}
          onRefresh={() => void handleRefresh()}
          onNew={() => {}}
          isNewDisabled
          showNewAction={!fixedBrand}
          showSummary={fixedBrand !== 'apikeyFun'}
        />
        {errorBanner}
      </div>
    );
  }

  return (
    <div className={styles.page}>
      <ProviderHeaderCard
        title={headerTitle}
        totalActive={totalActive}
        totalResources={totalResources}
        providerFamilies={providerFamilies}
        updatedAtLabel={updatedAtLabel}
        isFetching={workbench.isFetching}
        isNewDisabled={disableMutations}
        showNewAction={!fixedBrand}
        showSummary={fixedBrand !== 'apikeyFun'}
        newLabel={t('providersPage.actions.new')}
        variant={fixedBrand === 'apikeyFun' ? 'quickStart' : undefined}
        onRefresh={() => void handleRefresh()}
        onNew={openCreate}
      />

      {errorBanner}

      <div className={`${styles.layout} ${fixedBrand ? styles.layoutSingle : ''}`.trim()}>
        {!fixedBrand ? (
          <ProviderCategoryList
            groups={groups}
            activeBrand={activeGroup.id}
            onSelect={(brand) => {
              const isSwitching = sheetState.open && sheetState.brand !== brand;
              const proceed =
                isSwitching && sheetRef.current
                  ? sheetRef.current.confirmDiscardIfDirty()
                  : Promise.resolve(true);
              void proceed.then((ok) => {
                if (!ok) return;
                setActiveBrand(brand);
                if (isSwitching) {
                  closeSheet();
                }
              });
            }}
          />
        ) : null}
        {fixedBrand === 'apikeyFun' ? (
          <SponsorQuickStartPanel
            resource={quickStartResource}
            workbench={workbench}
            mutationDisabled={disableMutations}
          />
        ) : (
          <ProviderResourcePanel
            group={activeGroup}
            filter={filter}
            onFilterChange={(value) => updateActiveFilterState({ filter: value })}
            filteredResources={visibleResources}
            selectedId={sheetState.open ? (sheetState.resource?.id ?? null) : null}
            disableMutations={disableMutations}
            usageByProvider={usageByProvider}
            toolbarControls={toolbarControls}
            onView={openView}
            onEdit={openEdit}
            onDelete={handleDelete}
            onToggleDisabled={handleToggleDisabled}
            onCreate={openCreate}
          />
        )}
      </div>

      {!fixedBrand ? (
        <ProviderSheet
          ref={sheetRef}
          state={sheetState}
          onClose={closeSheet}
          onSwitchToEdit={() => {
            setSheetState((s) => (s.resource ? { ...s, mode: 'edit' } : s));
          }}
          workbench={workbench}
          onCreated={handleCreated}
          onUpdated={handleUpdated}
          mutationDisabled={disableMutations}
          usageByProvider={usageByProvider}
        />
      ) : null}
    </div>
  );
}
