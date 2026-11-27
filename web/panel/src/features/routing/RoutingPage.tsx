/**
 * Routing board: CPA's priority tiers as one arrangement.
 *
 * Every move, weight and setting is a local draft until it is saved as one plan. Saving writes
 * credentials one by one, then the configuration once, and always reloads the board afterwards
 * so it shows what actually landed, including after a partial failure.
 */

import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react';
import { useTranslation } from 'react-i18next';
import { AutocompleteInput } from '@/components/ui/AutocompleteInput';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { Skeleton } from '@/components/ui/Skeleton';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useRevealGroup } from '@/hooks/motion';
import { useUnsavedChangesGuard } from '@/hooks/useUnsavedChangesGuard';
import {
  RoutingConflictError,
  buildRoutingBoard,
  routingApi,
  type RoutingApplyResult,
} from '@/services/api/routing';
import { useAuthStore, useConfigStore, useNotificationStore, useThemeStore } from '@/stores';
import type { ResolvedTheme } from '@/types';
import type {
  RoutingBoard,
  RoutingDraft,
  RoutingDropTarget,
  RoutingModelCatalog,
  RoutingSettings,
  RoutingTier,
} from '@/types/routing';
import { getErrorMessage } from '@/utils/helpers';
import { createSharedClock } from '@/utils/time/sharedClock';
import {
  assignPriorities,
  RoutingPriorityError,
  buildRoutingPlan,
  collectModelOptions,
  draftFromBoard,
  moveMember,
  neededChannels,
  neededCredentialModels,
  resolveRoute,
  stepMember,
} from './logic';
import { ChangeReviewDialog } from './components/ChangeReviewDialog';
import { PolicyPanel } from './components/PolicyPanel';
import { RoutingSaveBar } from './components/RoutingSaveBar';
import { usePageTransitionLayer } from '@/components/common/PageTransitionLayer';
import { TierLadder } from './components/TierLadder';
import { WeightDialog } from './components/WeightDialog';
import styles from './RoutingPage.module.scss';

// Cooldown chips count down coarsely; one shared timer for the whole board.
const cooldownClock = createSharedClock({ intervalMs: 5_000 });

const EMPTY_CATALOG: RoutingModelCatalog = { channels: {}, credentials: {} };

interface ModelProgress {
  done: number;
  total: number;
}

export function RoutingPage() {
  const { t } = useTranslation();
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const showNotification = useNotificationStore((state) => state.showNotification);
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);
  const resolvedTheme: ResolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const nowMs = useSyncExternalStore(
    cooldownClock.subscribe,
    cooldownClock.getSnapshot,
    cooldownClock.getSnapshot
  );
  const revealRef = useRevealGroup<HTMLDivElement>();
  const layoutRef = useRef<HTMLDivElement>(null);
  const pageTransitionLayer = usePageTransitionLayer();
  // A page sliding out must not leave its portalled save bar behind.
  const isCurrentLayer = pageTransitionLayer ? pageTransitionLayer.isCurrentLayer : true;

  const [board, setBoard] = useState<RoutingBoard | null>(null);
  const [draft, setDraft] = useState<RoutingDraft | null>(null);
  const [catalog, setCatalog] = useState<RoutingModelCatalog>(EMPTY_CATALOG);
  const [modelProgress, setModelProgress] = useState<ModelProgress | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [model, setModel] = useState('');
  const [reviewOpen, setReviewOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const [weightEdit, setWeightEdit] = useState<{ key: string; session: number } | null>(null);
  const [weightOpen, setWeightOpen] = useState(false);
  const requestRef = useRef(0);
  const catalogRef = useRef<RoutingModelCatalog>(EMPTY_CATALOG);

  const connected = connectionStatus === 'connected';

  const loadModels = useCallback((next: RoutingBoard, requestId: number) => {
    const isCurrent = () => requestId === requestRef.current;
    const known = catalogRef.current;
    const channels = neededChannels(next).filter((channel) => !(channel in known.channels));
    const credentials = neededCredentialModels(next).filter((name) => !(name in known.credentials));
    const total = channels.length + credentials.length;
    if (total === 0) {
      setModelProgress(null);
      return;
    }
    let done = 0;
    setModelProgress({ done, total });
    const record = (kind: 'channels' | 'credentials', id: string, models: string[] | null) => {
      if (!isCurrent()) return;
      done += 1;
      catalogRef.current = {
        ...catalogRef.current,
        [kind]: { ...catalogRef.current[kind], [id]: models },
      };
      setCatalog(catalogRef.current);
      setModelProgress(done >= total ? null : { done, total });
    };
    void routingApi.loadChannelModels(channels, (id, models) => record('channels', id, models));
    void routingApi.loadCredentialModels(credentials, (id, models) =>
      record('credentials', id, models)
    );
  }, []);

  const load = useCallback(async () => {
    const requestId = ++requestRef.current;
    const isCurrent = () => requestId === requestRef.current;
    if (!connected) {
      setLoading(false);
      return;
    }
    setLoading(true);
    setError(null);
    try {
      const [config, files] = await Promise.all([fetchConfig(true), routingApi.listCredentials()]);
      if (!isCurrent()) return;
      const next = buildRoutingBoard(config.raw ?? {}, files);
      setBoard(next);
      setDraft(draftFromBoard(next));
      loadModels(next, requestId);
    } catch (err) {
      if (!isCurrent()) return;
      setError(getErrorMessage(err));
    } finally {
      if (isCurrent()) setLoading(false);
    }
  }, [connected, fetchConfig, loadModels]);

  useEffect(() => {
    void load();
    return () => {
      // Late responses from this mount must not land on a later one.
      requestRef.current += 1;
    };
  }, [load]);

  useEffect(() => {
    // A new connection is a new backend: model lists are not shared across it.
    catalogRef.current = EMPTY_CATALOG;
    setCatalog(EMPTY_CATALOG);
  }, [connectionStatus]);

  const plan = useMemo(
    () => (board && draft ? buildRoutingPlan(board, draft) : null),
    [board, draft]
  );
  const dirty = Boolean(plan && plan.changes.length > 0);

  // The global header refresh reloads the board; with a draft it asks first.
  const refresh = useCallback(() => {
    if (!dirty) return load();
    showConfirmation({
      title: t('routing.unsaved_title'),
      message: t('routing.refresh_discards'),
      confirmText: t('routing.refresh'),
      cancelText: t('routing.unsaved_cancel'),
      variant: 'danger',
      onConfirm: () => load(),
    });
  }, [dirty, load, showConfirmation, t]);

  useHeaderRefresh(refresh);

  const { allowNextNavigation } = useUnsavedChangesGuard({
    shouldBlock: dirty,
    dialog: {
      title: t('routing.unsaved_title'),
      message: t('routing.unsaved_message'),
      confirmText: t('routing.unsaved_confirm'),
      cancelText: t('routing.unsaved_cancel'),
      variant: 'danger',
    },
  });

  const members = useMemo(
    () => new Map((board?.members ?? []).map((member) => [member.key, member])),
    [board]
  );
  const priorities = useMemo(
    () => (board && draft ? assignPriorities(draft.tiers, board.members) : {}),
    [board, draft]
  );
  const selectedModel = model.trim() || null;
  const preview = useMemo(
    () => (board && draft ? resolveRoute(board, draft, catalog, selectedModel, nowMs) : null),
    [board, draft, catalog, selectedModel, nowMs]
  );
  const modelOptions = useMemo(
    () => (board ? collectModelOptions(board, catalog) : []),
    [board, catalog]
  );

  const applyTiers = useCallback(
    (tiers: RoutingTier[]) => {
      if (!board || !draft) return;
      try {
        assignPriorities(tiers, board.members);
        setDraft({ ...draft, tiers });
      } catch (err) {
        if (!(err instanceof RoutingPriorityError)) throw err;
        showNotification(t('routing.locked_priority_conflict'), 'warning');
      }
    },
    [board, draft, showNotification, t]
  );

  const handleMove = useCallback(
    (key: string, target: RoutingDropTarget) => {
      if (draft) applyTiers(moveMember(draft.tiers, key, target));
    },
    [draft, applyTiers]
  );

  const handleStep = useCallback(
    (key: string, direction: 'up' | 'down') => {
      if (draft) applyTiers(stepMember(draft.tiers, key, direction));
      // Keep keyboard focus on the moved card after it re-renders in its new tier.
      requestAnimationFrame(() => {
        const card = document.querySelector<HTMLElement>(
          `[data-member-key="${CSS.escape(key)}"] button`
        );
        card?.focus();
      });
    },
    [draft, applyTiers]
  );

  const handleEditWeights = useCallback((key: string) => {
    setWeightEdit((current) => ({ key, session: (current?.session ?? 0) + 1 }));
    setWeightOpen(true);
  }, []);

  const handleApplyWeights = useCallback((key: string, weights: (number | null)[]) => {
    setDraft((current) =>
      current ? { ...current, weights: { ...current.weights, [key]: weights } } : current
    );
  }, []);

  const handleSettings = useCallback((settings: RoutingSettings) => {
    setDraft((current) => (current ? { ...current, settings } : current));
  }, []);

  const discard = () => {
    if (board) setDraft(draftFromBoard(board));
  };

  const reportResult = (result: RoutingApplyResult) => {
    if (!result.failed) {
      showNotification(t('routing.save_success', { count: plan?.changes.length ?? 0 }), 'success');
      return;
    }
    if (result.failed.error instanceof RoutingConflictError) {
      showNotification(t('routing.save_conflict'), 'error');
      return;
    }
    const failedKey = result.failed.key;
    const name =
      failedKey === 'config'
        ? t('routing.config_target')
        : (members.get(failedKey)?.name ?? failedKey);
    showNotification(
      t('routing.save_partial', {
        name,
        message: getErrorMessage(result.failed.error, t('common.unknown_error')),
        applied: result.applied.length,
        skipped: result.notAttempted.length,
      }),
      'error',
      10_000
    );
  };

  const save = async () => {
    if (!board || !plan || saving) return;
    setSaving(true);
    try {
      const result = await routingApi.applyPlan(board, plan);
      reportResult(result);
    } catch (err) {
      showNotification(getErrorMessage(err, t('common.unknown_error')), 'error');
    } finally {
      setSaving(false);
      setReviewOpen(false);
      allowNextNavigation();
      await load();
    }
  };

  const entryControl = (
    <div className={styles.modelPicker}>
      <AutocompleteInput
        value={model}
        onChange={setModel}
        options={modelOptions}
        placeholder={t('routing.model_placeholder')}
        wrapperClassName={styles.modelInput}
        className={styles.modelField}
        id="routing-model-preview"
      />
      {modelProgress ? (
        <span className={styles.modelProgress} role="status">
          {t('routing.models_loading', { done: modelProgress.done, total: modelProgress.total })}
        </span>
      ) : null}
    </div>
  );

  const renderBody = () => {
    if (loading && !board) {
      return (
        <div className={styles.skeletons} aria-busy="true" aria-label={t('routing.loading')}>
          {[0, 1, 2].map((index) => (
            <Skeleton key={index} height={96} />
          ))}
        </div>
      );
    }
    if (error !== null && !board) {
      return (
        <EmptyState
          title={t('routing.load_failed', { message: error || t('common.unknown_error') })}
          action={
            <Button variant="secondary" onClick={() => void load()}>
              {t('routing.retry')}
            </Button>
          }
        />
      );
    }
    if (!board || !draft || !preview) return null;
    if (board.members.length === 0) {
      return <EmptyState title={t('routing.empty_title')} description={t('routing.empty_desc')} />;
    }
    return (
      <div className={styles.layout} ref={layoutRef}>
        <section className={styles.board} aria-label={t('routing.title')}>
          <TierLadder
            members={members}
            draft={draft}
            preview={preview}
            priorities={priorities}
            model={selectedModel}
            nowMs={nowMs}
            theme={resolvedTheme}
            entryControl={entryControl}
            onMove={handleMove}
            onStep={handleStep}
            onEditWeights={handleEditWeights}
          />
        </section>
        <PolicyPanel settings={draft.settings} disabled={saving} onChange={handleSettings} />
      </div>
    );
  };

  const editingMember = weightEdit ? (members.get(weightEdit.key) ?? null) : null;

  return (
    <div className={styles.page} ref={revealRef}>
      <header className={styles.header}>
        <h1 className={styles.title} data-reveal>
          {t('routing.title')}
        </h1>
      </header>

      {renderBody()}

      <RoutingSaveBar
        visible={isCurrentLayer && dirty}
        count={plan?.changes.length ?? 0}
        saving={saving}
        saveDisabled={!connected}
        onSave={() => setReviewOpen(true)}
        onDiscard={discard}
        layoutRef={layoutRef}
      />

      {plan ? (
        <ChangeReviewDialog
          open={reviewOpen}
          changes={plan.changes}
          members={members}
          saving={saving}
          onConfirm={() => void save()}
          onClose={() => setReviewOpen(false)}
        />
      ) : null}

      {weightEdit && draft ? (
        <WeightDialog
          key={weightEdit.session}
          open={weightOpen}
          member={editingMember}
          weights={draft.weights[weightEdit.key] ?? []}
          strategy={draft.settings.strategy}
          onApply={handleApplyWeights}
          onClose={() => setWeightOpen(false)}
        />
      ) : null}
    </div>
  );
}
