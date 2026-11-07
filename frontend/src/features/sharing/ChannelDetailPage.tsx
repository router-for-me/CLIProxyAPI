import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Modal } from '@/components/ui/Modal';
import { EmptyState } from '@/components/ui/EmptyState';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { useAuthStore, useThemeStore } from '@/stores';
import { sharingApi, sharingPath } from '@/services/api/sharing';
import { apiClient } from '@/services/api/client';
import { normalizeAuthFilesResponse } from '@/services/api/authFiles';
import { sharingCredentials, type SharedCredential } from '@/services/api/sharingCredentials';
import { downloadBlob } from '@/utils/download';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { AuthFileCard } from '@/features/authFiles/components/AuthFileCard';
import { getAuthFileRefreshKey } from '@/features/authFiles/manualRefresh';
import { BaseProviderForm } from '@/features/providers/sheets/forms/BaseProviderForm';
import type { ProviderEntryFormInput, ProviderResource } from '@/features/providers/types';
import type { AuthFilesResponse } from '@/types/authFile';
import type { BoardMessage, Channel } from './types';
import { DailyTrend, LoadState, PageHeader } from './shared';
import { date, number, useSharingActions, useSharingData } from './hooks';
import { useChannelStore } from './useChannelStore';
import { CredentialModelsModal } from './CredentialModelsModal';
import { buildCredentialUsagePath } from './usageFilters';
import styles from './Sharing.module.scss';

const EMPTY_STATUS_BAR_CACHE = new Map();

/** Edit-mode placeholder for the stored key: the hub never echoes api_key, so a
 * sentinel keeps the entry card in "configured" state without leaking it. The
 * discovery fetcher maps it back to an empty key. */
const STORED_KEY_SENTINEL = 'hub-stored-key';

const PROVIDER_NAME_PATTERN = /^[a-z0-9][a-z0-9-]{0,39}$/;

function ChannelCredentialCard({
  file,
  refresh,
}: {
  file: SharedCredential;
  refresh: () => Promise<void>;
}) {
  const { t } = useTranslation();
  const account = useAuthStore((state) => state.account);
  const navigate = useNavigate();
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const { run, confirm } = useSharingActions(refresh);
  const [statusUpdating, setStatusUpdating] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [modelsOpen, setModelsOpen] = useState(false);

  const refreshKey = getAuthFileRefreshKey(file);
  const manageable = file.manageable === true;

  return (
    <div className={styles.credentialItem}>
      <div className={styles.credentialMeta}>
        <span className={styles.mono}>{file.channel_prefix ?? file.channel}</span>
        <span>{t('sharing.mounted_by', { name: file.created_by || file.owner })}</span>
      </div>
      <AuthFileCard
        file={file}
        compact={false}
        selected={false}
        resolvedTheme={resolvedTheme}
        disableControls={false}
        deleting={deleting ? file.name : null}
        statusUpdating={statusUpdating ? { [refreshKey]: true } : {}}
        manualRefreshing={refreshing ? { [refreshKey]: true } : {}}
        cooldownResetting={{}}
        quotaFilterType="all"
        statusBarCache={EMPTY_STATUS_BAR_CACHE}
        onShowModels={() => setModelsOpen(true)}
        onShowUsage={() => {
          const path = buildCredentialUsagePath(
            file,
            account?.username || '',
            account?.role === 'admin'
          );
          if (path) navigate(path);
        }}
        onDownload={
          manageable
            ? (name) =>
                void run(async () => {
                  const blob = await sharingCredentials.download(name);
                  downloadBlob({ filename: name, blob });
                })
            : undefined
        }
        onManualRefresh={
          manageable
            ? (target) =>
                void run(async () => {
                  setRefreshing(true);
                  try {
                    await sharingCredentials.refresh(target.name);
                  } finally {
                    setRefreshing(false);
                  }
                })
            : undefined
        }
        onDelete={
          manageable
            ? (name) =>
                confirm(t('sharing.delete_credential_confirm'), async () => {
                  setDeleting(true);
                  try {
                    await sharingCredentials.remove([name]);
                  } finally {
                    setDeleting(false);
                  }
                })
            : undefined
        }
        onToggleStatus={
          manageable
            ? (target, enabled) =>
                void run(async () => {
                  setStatusUpdating(true);
                  try {
                    await sharingCredentials.status(target.name, !enabled);
                  } finally {
                    setStatusUpdating(false);
                  }
                })
            : undefined
        }
      />
      {modelsOpen && (
        <CredentialModelsModal file={file} onClose={() => setModelsOpen(false)} refresh={refresh} />
      )}
    </div>
  );
}

function synthesizeProviderResource(provider: SharedCredential): ProviderResource {
  return {
    id: provider.route_id,
    brand: 'openaiCompatibility',
    originalIndex: 0,
    name: provider.provider_name ?? null,
    identifier: provider.label || provider.provider_name || provider.name,
    apiKeyPreview: null,
    apiKey: null,
    authIndex: null,
    baseUrl: provider.base_url ?? null,
    proxyUrl: provider.proxy_url ?? null,
    prefix: null,
    modelCount: provider.provider_models?.length ?? 0,
    models: (provider.provider_models ?? []).map((model) => model.name),
    priority: 0,
    headerCount: 0,
    excludedModelCount: 0,
    apiKeyEntryCount: 1,
    disabled: provider.disabled === true,
    flags: {},
    selector: { brand: 'openaiCompatibility', name: provider.provider_name ?? '', index: 0 },
    raw: {
      // In edit mode the name field carries the display label; the routing
      // slug (provider_name) is immutable because model IDs depend on it.
      name: provider.label || provider.provider_name || '',
      baseUrl: provider.base_url ?? '',
      disabled: provider.disabled === true,
      models: (provider.provider_models ?? []).map((model) => ({
        name: model.name,
        alias: model.alias,
      })),
      apiKeyEntries: [{ apiKey: STORED_KEY_SENTINEL, proxyUrl: provider.proxy_url ?? '' }],
    },
  };
}

function ChannelProviderFormModal({
  channel,
  provider,
  onClose,
  onSaved,
}: {
  channel: Channel;
  provider: SharedCredential | null;
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const { t } = useTranslation();
  const [saving, setSaving] = useState(false);
  const formId = useMemo(
    () => `channel-provider-${provider ? provider.route_id : 'create'}`,
    [provider]
  );
  const resource = useMemo(
    () => (provider ? synthesizeProviderResource(provider) : null),
    [provider]
  );

  const handleSubmit = async (input: ProviderEntryFormInput) => {
    const entry = (input.apiKeyEntries ?? [])[0];
    const apiKey = (entry?.apiKey ?? '').trim();
    const proxyUrl = (entry?.proxyUrl ?? '').trim();
    const models = input.models
      .map((model) => ({
        name: model.name.trim(),
        alias: model.alias?.trim() || undefined,
      }))
      .filter((model) => model.name);

    if (!provider && !PROVIDER_NAME_PATTERN.test(input.name.trim())) {
      throw new Error(t('sharing.provider_name_invalid'));
    }
    if (!provider && !apiKey) {
      throw new Error(t('sharing.provider_api_key_required'));
    }
    if (models.length === 0) {
      throw new Error(t('sharing.provider_models_required'));
    }

    setSaving(true);
    try {
      if (provider) {
        await sharingApi.updateProvider(provider.route_id, {
          label: input.name.trim(),
          base_url: input.baseUrl.trim(),
          // Empty key keeps the stored one (mirrors the global workbench contract).
          api_key: apiKey,
          proxy_url: proxyUrl,
          models,
        });
      } else {
        await sharingApi.createProvider(channel.id, {
          name: input.name.trim(),
          base_url: input.baseUrl.trim(),
          api_key: apiKey,
          proxy_url: proxyUrl || undefined,
          models,
        });
      }
      await onSaved();
      onClose();
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      title={t(provider ? 'sharing.edit_provider' : 'sharing.add_provider')}
      width={640}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" form={formId} loading={saving}>
            {t('common.save')}
          </Button>
        </>
      }
    >
      <BaseProviderForm
        brand="openaiCompatibility"
        resource={resource}
        mode={provider ? 'edit' : 'create'}
        mutating={saving}
        formId={formId}
        onSubmit={handleSubmit}
        descriptorOverride={{
          supportsPrefix: false,
          supportsPriority: false,
          supportsHeaders: false,
          supportsTestModel: false,
          supportsRequestScopedErrors: false,
          supportsDisabled: false,
        }}
        discoveryFetcher={async ({ baseUrl, apiKey, proxyUrl }) => {
          const result = await sharingApi.discoverProviderModels(
            baseUrl,
            apiKey === STORED_KEY_SENTINEL ? '' : apiKey,
            proxyUrl
          );
          return result.models.map((name) => ({ name }));
        }}
        apiKeyEntriesLimit={1}
        disableKeyTest
      />
    </Modal>
  );
}

function ChannelProviderCard({
  provider,
  channel,
  refresh,
  onEdit,
}: {
  provider: SharedCredential;
  channel: Channel;
  refresh: () => Promise<void>;
  onEdit: () => void;
}) {
  const { t } = useTranslation();
  const account = useAuthStore((state) => state.account);
  const { run, confirm } = useSharingActions(refresh);
  const [statusUpdating, setStatusUpdating] = useState(false);
  const manageable = provider.manageable === true;
  const usagePath = buildCredentialUsagePath(
    provider,
    account?.username || '',
    account?.role === 'admin'
  );

  return (
    <Card
      className={styles.providerCard}
      title={
        <span className={styles.providerTitle}>
          <span className={styles.badge}>{t('sharing.kind_provider')}</span>
          <span className={styles.resourceName}>{provider.label || provider.provider_name}</span>
        </span>
      }
      extra={
        <span className={styles.actions}>
          {usagePath && (
            <Link to={usagePath} className="btn btn-secondary btn-sm">
              {t('sharing.view_usage')}
            </Link>
          )}
          {manageable && (
            <>
              <Button variant="secondary" size="sm" onClick={onEdit}>
                {t('common.edit')}
              </Button>
              <Button
                variant="danger"
                size="sm"
                onClick={() =>
                  confirm(t('sharing.delete_provider_confirm'), () =>
                    sharingCredentials.remove([provider.name])
                  )
                }
              >
                {t('common.delete')}
              </Button>
            </>
          )}
        </span>
      }
    >
      <dl className={styles.fieldGrid}>
        <div className={styles.field}>
          <dt>{t('sharing.call_prefix')}</dt>
          <dd className={styles.mono}>{provider.channel_prefix ?? provider.channel}</dd>
        </div>
        <div className={styles.field}>
          <dt>{t('sharing.provider_base_url')}</dt>
          <dd className={styles.mono}>{provider.base_url || '—'}</dd>
        </div>
        <div className={styles.field}>
          <dt>{t('sharing.provider_models_label')}</dt>
          <dd>{t('sharing.resource_models', { count: provider.models.length })}</dd>
        </div>
        <div className={styles.field}>
          <dt>{t('sharing.mounted_by_label')}</dt>
          <dd>{provider.created_by || provider.owner}</dd>
        </div>
        {provider.proxy_url && (
          <div className={styles.field}>
            <dt>{t('sharing.provider_proxy_url')}</dt>
            <dd className={styles.mono}>{provider.proxy_url}</dd>
          </div>
        )}
        <div className={styles.field}>
          <dt>{t('sharing.status_label')}</dt>
          <dd>
            <ToggleSwitch
              ariaLabel={t('sharing.provider_status_toggle', {
                name: provider.label || provider.provider_name || provider.name,
              })}
              checked={!provider.disabled}
              disabled={!manageable || statusUpdating}
              onChange={(value) =>
                void run(async () => {
                  setStatusUpdating(true);
                  try {
                    await sharingCredentials.status(provider.name, !value);
                  } finally {
                    setStatusUpdating(false);
                  }
                })
              }
            />
          </dd>
        </div>
      </dl>
      <span className={styles.meta}>
        {t('sharing.provider_channel_hint', { channel: channel.slug })}
      </span>
    </Card>
  );
}

function AnnouncementCard({
  channel,
  canManage,
  refresh,
}: {
  channel: Channel;
  canManage: boolean;
  refresh: () => Promise<void>;
}) {
  const { t } = useTranslation();
  const { run } = useSharingActions(refresh);
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState('');
  const [saving, setSaving] = useState(false);
  const save = async () => {
    setSaving(true);
    await run(async () => {
      await sharingApi.updateChannel(channel.id, { announcement: text });
      setEditing(false);
    });
    setSaving(false);
  };
  return (
    <Card
      title={t('sharing.announcement')}
      extra={
        canManage && (
          <Button
            variant="secondary"
            size="sm"
            onClick={() => {
              setText(channel.announcement ?? '');
              setEditing(true);
            }}
          >
            {t('common.edit')}
          </Button>
        )
      }
    >
      {channel.announcement ? (
        <p className={styles.announcement}>{channel.announcement}</p>
      ) : (
        <p className={styles.meta}>{t('sharing.no_announcement')}</p>
      )}
      {channel.announcement_updated_at && channel.announcement && (
        <p className={styles.meta}>{date(channel.announcement_updated_at)}</p>
      )}
      <Modal
        open={editing}
        onClose={() => setEditing(false)}
        title={t('sharing.edit_announcement')}
        footer={
          <>
            <Button variant="secondary" onClick={() => setEditing(false)}>
              {t('common.cancel')}
            </Button>
            <Button onClick={() => void save()} loading={saving}>
              {t('common.save')}
            </Button>
          </>
        }
      >
        <textarea
          className="input"
          rows={6}
          value={text}
          onChange={(event) => setText(event.target.value)}
          maxLength={5000}
        />
      </Modal>
    </Card>
  );
}

function BoardPanel({ channel, canModerate }: { channel: Channel; canModerate: boolean }) {
  const { t } = useTranslation();
  const account = useAuthStore((state) => state.account);
  const [messages, setMessages] = useState<BoardMessage[]>([]);
  const [content, setContent] = useState('');
  const [sending, setSending] = useState(false);
  const [hasMore, setHasMore] = useState(false);
  const [loadingEarlier, setLoadingEarlier] = useState(false);
  const [earlierError, setEarlierError] = useState('');
  const earlierPending = useRef(false);
  const lifetime = useRef(0);
  const revision = useRef(0);
  const pageSize = 50;

  // Polling merges genuinely new messages instead of replacing the list, so
  // messages loaded via "load earlier" are never wiped by a background tick.
  const loadLatest = useCallback(
    async (merge: boolean) => {
      const id = ++revision.current;
      try {
        const connection = apiClient.getConnectionRevision();
        const result = await sharingApi.boardMessages(channel.id, undefined, pageSize);
        if (id !== revision.current || connection !== apiClient.getConnectionRevision()) return;
        if (!merge) {
          setMessages(result.messages);
          setHasMore(result.messages.length >= pageSize);
          return;
        }
        setMessages((current) => {
          const known = new Set(current.map((message) => message.id));
          const fresh = result.messages.filter((message) => !known.has(message.id));
          return fresh.length ? [...fresh, ...current] : current;
        });
      } catch {
        // Polling failures are silent; the next tick retries.
      }
    },
    [channel.id]
  );

  const { confirm } = useSharingActions(() => loadLatest(true));

  useEffect(() => {
    const scope = revision;
    const lifecycle = lifetime;
    setMessages([]);
    setHasMore(false);
    setLoadingEarlier(false);
    setEarlierError('');
    earlierPending.current = false;
    void loadLatest(false);
    const timer = setInterval(() => {
      if (document.visibilityState === 'visible') void loadLatest(true);
    }, 15000);
    return () => {
      scope.current++;
      lifecycle.current++;
      clearInterval(timer);
    };
  }, [loadLatest]);

  const loadEarlier = async () => {
    const oldest = messages[messages.length - 1];
    if (!oldest || !hasMore || earlierPending.current) return;
    const generation = lifetime.current;
    const connection = apiClient.getConnectionRevision();
    const isCurrent = () =>
      generation === lifetime.current && connection === apiClient.getConnectionRevision();
    earlierPending.current = true;
    setLoadingEarlier(true);
    setEarlierError('');
    try {
      const result = await sharingApi.boardMessages(channel.id, oldest.id, pageSize);
      if (!isCurrent()) return;
      setMessages((current) => {
        const known = new Set(current.map((message) => message.id));
        return [...current, ...result.messages.filter((message) => !known.has(message.id))];
      });
      setHasMore(result.messages.length >= pageSize);
    } catch (error) {
      if (isCurrent()) setEarlierError(error instanceof Error ? error.message : String(error));
    } finally {
      if (isCurrent()) {
        earlierPending.current = false;
        setLoadingEarlier(false);
      }
    }
  };

  const send = async () => {
    const text = content.trim();
    if (!text) return;
    setSending(true);
    try {
      await sharingApi.postBoardMessage(channel.id, text);
      setContent('');
      await loadLatest(true);
    } finally {
      setSending(false);
    }
  };

  const canDelete = (message: BoardMessage) => canModerate || message.author === account?.username;

  return (
    <Card className={styles.boardCard} title={t('sharing.board')}>
      <div className={styles.boardList}>
        {hasMore && (
          <Button
            variant="ghost"
            size="sm"
            loading={loadingEarlier}
            onClick={() => void loadEarlier()}
          >
            {t('sharing.load_earlier')}
          </Button>
        )}
        <LoadState loading={false} error={earlierError} />
        {messages.length === 0 && <p className={styles.meta}>{t('sharing.no_messages')}</p>}
        {[...messages].reverse().map((message) => (
          <div key={message.id} className={styles.boardRow}>
            <div className={styles.boardMeta}>
              <strong>{message.author}</strong>
              <span>{date(message.created_at)}</span>
              {canDelete(message) && (
                <button
                  type="button"
                  className={styles.boardDelete}
                  onClick={() =>
                    confirm(t('sharing.delete_message_confirm'), async () => {
                      await sharingApi.deleteBoardMessage(channel.id, message.id);
                      setMessages((current) => current.filter((item) => item.id !== message.id));
                    })
                  }
                >
                  {t('common.delete')}
                </button>
              )}
            </div>
            <p className={styles.boardContent}>{message.content}</p>
          </div>
        ))}
      </div>
      <div className={styles.boardComposer}>
        <textarea
          className="input"
          rows={2}
          value={content}
          maxLength={2000}
          placeholder={t('sharing.board_placeholder')}
          onChange={(event) => setContent(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) void send();
          }}
        />
        <Button onClick={() => void send()} loading={sending} disabled={!content.trim()}>
          {t('sharing.send')}
        </Button>
      </div>
    </Card>
  );
}

type DetailTab = 'overview' | 'resources' | 'board' | 'manage';

function resolveTab(value: string | null, canManage: boolean): DetailTab {
  if (value === 'resources' || value === 'board') return value;
  if (value === 'manage' && canManage) return 'manage';
  return 'overview';
}

function ManagePanel({
  channel,
  isOwner,
  mountedCount,
  refresh,
}: {
  channel: Channel;
  isOwner: boolean;
  mountedCount: number;
  refresh: () => Promise<void>;
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { run, confirm } = useSharingActions(refresh);
  const [saving, setSaving] = useState(false);

  const togglePublic = (value: boolean) =>
    void run(async () => {
      setSaving(true);
      try {
        await sharingApi.updateChannel(channel.id, { public: value });
        await useChannelStore.getState().load();
      } finally {
        setSaving(false);
      }
    });

  const remove = () =>
    confirm(t('sharing.delete_channel_confirm', { name: channel.slug }), async () => {
      await sharingApi.deleteChannel(channel.id);
      await useChannelStore.getState().load();
      navigate('/channels');
    });

  return (
    <>
      <Card title={t('sharing.edit_channel')}>
        <p className={styles.meta}>{t('sharing.manage_hint')}</p>
        <dl className={styles.fieldGrid}>
          <div className={styles.field}>
            <dt>{t('sharing.channel_slug')}</dt>
            <dd className={styles.mono}>{channel.slug}</dd>
          </div>
          <div className={styles.field}>
            <dt>{t('sharing.visibility_label')}</dt>
            <dd>
              <ToggleSwitch
                ariaLabel={t('sharing.public_channel')}
                checked={channel.public}
                disabled={!isOwner || saving}
                onChange={togglePublic}
              />
            </dd>
          </div>
        </dl>
        <p className={styles.meta}>{t('sharing.public_channel_hint')}</p>
      </Card>
      <Card title={t('sharing.publish_status')}>
        <p className={styles.meta}>{t('sharing.publish_hint')}</p>
        <div className={styles.actions}>
          <span
            className={`${styles.status} ${channel.published ? styles.active : styles.inactive}`}
          >
            <span className={styles.statusDot} aria-hidden="true" />
            {t(channel.published ? 'sharing.published' : 'sharing.withdrawn')}
          </span>
          <Button
            variant="secondary"
            size="sm"
            onClick={() =>
              confirm(t('sharing.publish_confirm'), () =>
                sharingApi.updateChannel(channel.id, { published: !channel.published })
              )
            }
          >
            {t(channel.published ? 'sharing.withdraw' : 'sharing.publish')}
          </Button>
        </div>
      </Card>
      {isOwner && (
        <Card title={t('sharing.danger_zone')}>
          <p className={styles.meta}>{t('sharing.delete_channel_hint')}</p>
          <Button
            variant="danger"
            size="sm"
            onClick={remove}
            disabled={mountedCount > 0}
            title={mountedCount > 0 ? t('sharing.delete_channel_blocked') : undefined}
          >
            {t('common.delete')}
          </Button>
        </Card>
      )}
    </>
  );
}

export function ChannelDetailPage() {
  const { t } = useTranslation();
  const { id } = useParams<{ id: string }>();
  const account = useAuthStore((state) => state.account);
  const [params, setParams] = useSearchParams();
  const [providerForm, setProviderForm] = useState<{
    open: boolean;
    provider: SharedCredential | null;
  }>({ open: false, provider: null });

  // Channel/credential data loads separately from the log-derived summary so
  // a slow or cold-building usage index never blocks the page or fakes zeros.
  const { data, loading, error, refresh: refreshCore } = useSharingData(
    useCallback(async (signal: AbortSignal) => {
      const [channels, rawCredentials] = await Promise.all([
        sharingApi.channels(signal),
        apiClient.get<{ credentials: SharedCredential[] }>(sharingPath('/credentials'), {
          params: { channel_id: id },
          signal,
        }),
      ]);
      // AuthFileCard consumes normalized items (cooldownSnapshot, authIndex,
      // recentRequests); reuse the auth-files normalizer on the raw hub payload.
      const normalized = normalizeAuthFilesResponse({
        files: rawCredentials.credentials,
      } as AuthFilesResponse);
      return {
        channels: channels.channels,
        credentials: normalized.files as unknown as SharedCredential[],
      };
    }, [id]),
    { keepPreviousData: false, headerRefresh: false, dataKey: id }
  );
  const {
    data: stats,
    loading: statsLoading,
    error: statsError,
    refresh: refreshStats,
  } = useSharingData(
    useCallback((signal: AbortSignal) => sharingApi.summary({ channel_id: id ?? '' }, signal), [id]),
    { keepPreviousData: false, headerRefresh: false, dataKey: id }
  );
  const refresh = useCallback(async () => {
    await Promise.all([refreshCore(), refreshStats()]);
  }, [refreshCore, refreshStats]);
  useHeaderRefresh(refresh);

  const channel = data?.channels.find((item) => item.id === id);
  const credentials = (data?.credentials ?? []).filter((file) => file.kind !== 'provider');
  const providers = (data?.credentials ?? []).filter((file) => file.kind === 'provider');
  const isOwner = !!channel && (channel.owner === account?.username || account?.role === 'admin');
  const canManage = isOwner || !!channel?.public;

  const tab = resolveTab(params.get('tab'), canManage);
  const setTab = (next: DetailTab) => {
    const updated = new URLSearchParams(params);
    if (next === 'overview') updated.delete('tab');
    else updated.set('tab', next);
    setParams(updated, { replace: true });
  };

  const callableModels = useMemo(() => {
    const names = new Set<string>();
    (data?.credentials ?? []).forEach((file) =>
      (file.models ?? []).forEach((model) => names.add(model))
    );
    return names.size;
  }, [data]);

  const statsTotal = stats?.total;
  const failureRate =
    statsTotal && statsTotal.executions > 0
      ? ((statsTotal.failures / statsTotal.executions) * 100).toFixed(1)
      : null;

  if (!loading && !channel) {
    return (
      <div className={styles.page}>
        <PageHeader title={t('sharing.channel_detail')} />
        <LoadState loading={loading} error={error} />
        <EmptyState title={t('sharing.channel_not_found')} />
      </div>
    );
  }
  const tabs: { key: DetailTab; label: string }[] = [
    { key: 'overview', label: t('sharing.tab_overview') },
    { key: 'resources', label: t('sharing.tab_resources') },
    { key: 'board', label: t('sharing.board') },
    ...(canManage ? [{ key: 'manage' as const, label: t('sharing.manage_tab') }] : []),
  ];
  return (
    <div className={styles.page}>
      <PageHeader
        title={channel ? channel.slug : t('sharing.channel_detail')}
        hint={channel ? t('sharing.created_by', { name: channel.owner }) : undefined}
        actions={
          channel && (
            <>
              <span className={channel.published ? styles.active : styles.inactive}>
                {t(channel.published ? 'sharing.published' : 'sharing.withdrawn')}
              </span>
              {channel.public && (
                <span className={styles.badge}>{t('sharing.public_channel')}</span>
              )}
            </>
          )
        }
      />
      <LoadState loading={loading} error={error} />
      {channel && (
        <>
          <div className={styles.segmented} role="tablist" aria-label={channel.slug}>
            {tabs.map((item) => (
              <button
                key={item.key}
                type="button"
                role="tab"
                aria-selected={tab === item.key}
                className={`${styles.segmentedItem} ${tab === item.key ? styles.active : ''}`}
                onClick={() => setTab(item.key)}
              >
                {item.label}
              </button>
            ))}
          </div>
          <div className={styles.tabContent} key={tab}>
            {tab === 'overview' && (
              <>
                <AnnouncementCard channel={channel} canManage={canManage} refresh={refresh} />
                <Card
                  title={t('sharing.channel_stats')}
                  extra={
                    <Link
                      to={'/usage?channel_id=' + channel.id}
                      className="btn btn-secondary btn-sm"
                    >
                      {t('sharing.view_usage')}
                    </Link>
                  }
                >
                  <div className={styles.statsStack}>
                    {statsLoading || statsError ? (
                      // Reuse the shared loading/error surface instead of
                      // showing fake zeros while the index warms up or fails.
                      <LoadState loading={statsLoading} error={statsError} />
                    ) : (
                      <>
                        <div className={styles.statCards}>
                      <div className={styles.statCard}>
                        <span className={styles.statLabel}>{t('sharing.routed_calls')}</span>
                        <strong className={styles.statValue}>
                          {number(statsTotal?.calls ?? 0)}
                        </strong>
                        <span className={styles.statHint}>
                          {failureRate !== null
                            ? t('sharing.hint_failures', { rate: failureRate })
                            : t('sharing.hint_requests')}
                        </span>
                      </div>
                      <div className={styles.statCard}>
                        <span className={styles.statLabel}>{t('sharing.total_tokens')}</span>
                        <strong className={styles.statValue}>
                          {number(statsTotal?.total_tokens ?? 0)}
                        </strong>
                        <span className={styles.statHint}>
                          {t('sharing.hint_tokens_io', {
                            input: number(statsTotal?.input_tokens ?? 0),
                            output: number(statsTotal?.output_tokens ?? 0),
                          })}
                        </span>
                      </div>
                      <div className={styles.statCard}>
                        <span className={styles.statLabel}>{t('sharing.callable_models')}</span>
                        <strong className={styles.statValue}>{number(callableModels)}</strong>
                        <span className={styles.statHint}>
                          {t('sharing.resources_summary', {
                            credentials: credentials.length,
                            providers: providers.length,
                          })}
                        </span>
                      </div>
                    </div>
                        <DailyTrend groups={stats?.groups ?? []} maxDays={14} />
                      </>
                    )}
                  </div>
                </Card>
                <Card title={t('sharing.channel_info')}>
                  <dl className={styles.fieldGrid}>
                    <div className={styles.field}>
                      <dt>{t('sharing.owner_label')}</dt>
                      <dd>{channel.owner}</dd>
                    </div>
                    <div className={styles.field}>
                      <dt>{t('sharing.prefix_label')}</dt>
                      <dd className={styles.mono}>{channel.prefix}</dd>
                    </div>
                    <div className={styles.field}>
                      <dt>{t('sharing.visibility_label')}</dt>
                      <dd>
                        {t(channel.public ? 'sharing.public_channel' : 'sharing.private_channel')}
                      </dd>
                    </div>
                    <div className={styles.field}>
                      <dt>{t('sharing.created_at_label')}</dt>
                      <dd>{date(channel.created_at)}</dd>
                    </div>
                  </dl>
                </Card>
              </>
            )}
            {tab === 'resources' && (
              <>
                <section className={styles.detailSection}>
                  <div className={styles.sectionHead}>
                    <h2>{t('sharing.credentials_section')}</h2>
                    {canManage && (
                      <Link
                        to="/oauth"
                        className="btn btn-primary btn-sm"
                        onClick={() => useChannelStore.getState().select(channel.id)}
                      >
                        {t('nav.oauth')}
                      </Link>
                    )}
                  </div>
                  {credentials.length === 0 ? (
                    <EmptyState title={t('sharing.no_credentials')} />
                  ) : (
                    <div className={styles.grid}>
                      {credentials.map((file) => (
                        <ChannelCredentialCard key={file.id} file={file} refresh={refresh} />
                      ))}
                    </div>
                  )}
                </section>
                <section className={styles.detailSection}>
                  <div className={styles.sectionHead}>
                    <h2>{t('sharing.providers_section')}</h2>
                    {canManage && (
                      <Button
                        size="sm"
                        onClick={() => setProviderForm({ open: true, provider: null })}
                      >
                        {t('sharing.add_provider')}
                      </Button>
                    )}
                  </div>
                  {providers.length === 0 ? (
                    <EmptyState title={t('sharing.no_providers')} />
                  ) : (
                    <div className={styles.grid}>
                      {providers.map((provider) => (
                        <ChannelProviderCard
                          key={provider.id}
                          provider={provider}
                          channel={channel}
                          refresh={refresh}
                          onEdit={() => setProviderForm({ open: true, provider })}
                        />
                      ))}
                    </div>
                  )}
                </section>
              </>
            )}
            {tab === 'board' && (
              <BoardPanel key={channel.id} channel={channel} canModerate={canManage} />
            )}
            {tab === 'manage' && (
              <ManagePanel
                channel={channel}
                isOwner={isOwner}
                mountedCount={data?.credentials.length ?? 0}
                refresh={refresh}
              />
            )}
          </div>
          {providerForm.open && (
            <ChannelProviderFormModal
              channel={channel}
              provider={providerForm.provider}
              onClose={() => setProviderForm({ open: false, provider: null })}
              onSaved={refresh}
            />
          )}
        </>
      )}
    </div>
  );
}
