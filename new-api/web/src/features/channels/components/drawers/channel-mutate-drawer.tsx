/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { zodResolver } from '@hookform/resolvers/zod'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  ArrowRight,
  ArrowLeft,
  ArrowRightLeft,
  PanelLeftOpen,
  AlertCircle,
  ChevronDown,
  ClipboardPaste,
  Loader2,
  Server,
  Sparkles,
  Trash2,
  Copy,
  FileText,
  Eraser,
  Eye,
  RefreshCw,
  Code,
  Route,
  Settings,
  SlidersHorizontal,
  Wand2,
} from 'lucide-react'
import {
  type ComponentProps,
  type ReactNode,
  useEffect,
  useState,
  useMemo,
  useCallback,
  useRef,
} from 'react'
import { type SubmitErrorHandler, useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  sideDrawerContentClassName,
  sideDrawerFooterClassName,
  sideDrawerFormClassName,
  sideDrawerHeaderClassName,
  sideDrawerSwitchItemClassName,
} from '@/components/drawer-layout'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { JsonCodeEditor } from '@/components/json-code-editor'
import { JsonEditor } from '@/components/json-editor'
import { LearnMore } from '@/components/learn-more'
import { LoadingState } from '@/components/loading-state'
import { MultiSelect } from '@/components/multi-select'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { IconBadge, type IconBadgeTone } from '@/components/ui/icon-badge'
import { Input } from '@/components/ui/input'
import { PopoverDescription, PopoverTitle } from '@/components/ui/popover'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Separator } from '@/components/ui/separator'
import {
  Sheet,
  SheetClose,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { SecureVerificationDialog } from '@/features/auth/secure-verification'
import { PluginIcon } from '@/features/task-plugins/components/plugin-icon'
import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'
import { useHiddenClickUnlock } from '@/hooks/use-hidden-click-unlock'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import {
  parseChannelConnectionInfo,
  type ChannelConnectionInfo,
} from '@/lib/channel-connection-info'
import { handleServerError } from '@/lib/handle-server-error'
import { ROLE } from '@/lib/roles'
import {
  requireServerSuccess,
  createServerError,
  getServerErrorMessage,
} from '@/lib/server-error-message'
import { cn } from '@/lib/utils'
import { useAuthStore } from '@/stores/auth-store'

import {
  getAllModels,
  getChannel,
  getChannelDefaultBaseURLs,
  getGroups,
  getPrefillGroups,
  getTaskPluginOptions,
  refreshCodexCredential,
} from '../../api'
import {
  ADD_MODE_OPTIONS,
  CLAUDE_FIELD_PASSTHROUGH_TYPES,
  CHANNEL_STATUS_LABELS,
  CHANNEL_TYPE_NEW_API,
  CHANNEL_TYPE_OLLAMA,
  CHANNEL_TYPE_OPTIONS,
  CHANNEL_TYPE_TASK_PLUGIN,
  CHANNEL_TYPE_VLLM,
  CHANNEL_TYPE_SGLANG,
  CHANNEL_TYPE_WARNINGS,
  ERROR_MESSAGES,
  FIELD_PASSTHROUGH_TYPES,
  FIELD_DESCRIPTIONS,
  FIELD_PLACEHOLDERS,
  MODEL_FETCHABLE_TYPES,
  OPENAI_FIELD_PASSTHROUGH_TYPES,
} from '../../constants'
import { useChannelKeyDisclosure } from '../../hooks/use-channel-key-disclosure'
import {
  useChannelModelDiscovery,
  type ChannelModelDiscoveryRequest,
} from '../../hooks/use-channel-model-discovery'
import { useChannelMutateForm } from '../../hooks/use-channel-mutate-form'
import {
  REDIRECT_PANEL_MIN_VIEWPORT,
  useRedirectPanelPlacement,
} from '../../hooks/use-redirect-panel-placement'
import {
  CHANNEL_FORM_DEFAULT_VALUES,
  CHANNEL_TYPE_ADVANCED_CUSTOM,
  channelFormSchema,
  channelsQueryKeys,
  getAdvancedCustomStats,
  transformChannelToFormDefaults,
  type ChannelFormValues,
  deduplicateKeys,
  getKeyPromptForType,
  parseModelsString,
  formatModelsArray,
  mergeModelMappingPairs,
  type ModelMappingPair,
  extractRedirectModels,
  extractMappingSourceModels,
  hasModelConfigChanged,
  findMissingModelsInMapping,
  validateModelMappingJson,
} from '../../lib'
import {
  getChannelConfigurationSection,
  getChannelConfigurationState,
  type ChannelConfigurationStatus,
  type ChannelConfigurationSection,
  type ChannelProviderTarget,
} from '../../lib/channel-configuration'
import {
  getChannelPluginExtensions,
  supportsChannelPluginExtensions,
  supportsNewAPIUpstream,
} from '../../lib/channel-plugin-extensions'
import { getChannelTypeConfig } from '../../lib/channel-type-config'
import {
  collectInvalidStatusCodeEntries,
  collectNewDisallowedStatusCodeRedirects,
} from '../../lib/status-code-risk-guard'
import {
  assessBaseUrlTrust,
  nextTaskPluginBaseUrl,
} from '../../lib/task-plugin-base-url'
import type { Channel } from '../../types'
import { ChannelPluginExtensions } from '../channel-plugin-extensions'
import { ChannelQuickOptions } from '../channel-quick-options'
import { ChannelTypeLogo } from '../channel-type-badge'
import { useChannels } from '../channels-provider'
import { AdvancedCustomEditorDialog } from '../dialogs/advanced-custom-editor-dialog'
import { ConfigureModelsDialog } from '../dialogs/configure-models-dialog'
import {
  MissingModelsConfirmationDialog,
  type MissingModelsAction,
} from '../dialogs/missing-models-confirmation-dialog'
import { ParamOverrideEditorDialog } from '../dialogs/param-override-editor-dialog'
import {
  PassthroughWarningDialog,
  type PassthroughKind,
} from '../dialogs/passthrough-warning-dialog'
import { StatusCodeRiskDialog } from '../dialogs/status-code-risk-dialog'
import {
  ModelMappingBatchDialog,
  type ModelMappingBatchResult,
  type ModelMappingBatchSource,
} from '../model-mapping-batch-dialog'
import {
  ModelMappingEditor,
  type ModelMappingDraftRequest,
} from '../model-mapping-editor'
import { ModelRedirectPanel } from '../model-redirect-panel'
import { ResponsesWebSocketSetting } from '../responses-websocket-setting'
import { UpstreamModelSelection } from '../upstream-model-selection'
import {
  ChannelConfiguration,
  ChannelConfigurationStatusIndicator,
} from './channel-configuration'
import { ChannelProviderPicker } from './channel-provider-picker'
import {
  ChannelApiAccessSection,
  ChannelAuthSection,
  ChannelBasicSection,
  ChannelEditorLoadingState,
  ChannelModelsSection,
} from './sections'

type ChannelMutateDrawerProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  currentRow?: Channel | null
}

type ModelMappingGuardrail = {
  invalidJson: boolean
  entries: Array<{ source: string; target: string }>
  missingSourceModels: string[]
  exposedTargetModels: string[]
}

// Helper functions
const createEmptyModelMappingGuardrail = (): ModelMappingGuardrail => ({
  invalidJson: false,
  entries: [],
  missingSourceModels: [],
  exposedTargetModels: [],
})

const formatModelNames = (models: string[]): string =>
  models.map((model) => `"${model}"`).join(', ')

const MODEL_MAPPING_PREVIEW_FALLBACK: Array<{
  source: string
  target: string
}> = [{ source: 'client-model', target: 'upstream-model' }]

const ADVANCED_CUSTOM_ROUTE_TYPE_PREVIEW_LIMIT = 3
const UPSTREAM_DETECTED_MODEL_PREVIEW_LIMIT = 8
const SENSITIVE_FORM_FIELDS = [
  'type',
  'base_url',
  'key',
  'openai_organization',
  'other',
  'key_mode',
  'param_override',
  'header_override',
  'settings',
  'setting',
  'advanced_custom',
  'is_enterprise_account',
  'vertex_key_type',
  'aws_key_type',
  'azure_responses_version',
  'force_format',
  'thinking_to_content',
  'proxy',
  'http_protocol',
  'http2_connection_shards',
  'pass_through_body_enabled',
  'responses_websocket_enabled',
  'system_prompt',
  'system_prompt_override',
  'allow_service_tier',
  'disable_store',
  'allow_safety_identifier',
  'allow_include_obfuscation',
  'allow_inference_geo',
  'allow_speed',
  'claude_beta_query',
  'ollama_openai_chat',
  'disable_task_polling_sleep',
  'upstream_model_update_check_enabled',
  'upstream_model_update_auto_sync_enabled',
  'upstream_model_update_ignored_models',
] satisfies (keyof ChannelFormValues)[]

function parseSettingsRecord(
  settings: string | undefined
): Record<string, unknown> {
  if (!settings?.trim()) return {}
  try {
    const parsed = JSON.parse(settings)
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
      return parsed as Record<string, unknown>
    }
  } catch {
    return {}
  }
  return {}
}

function formatUnixTime(timestamp: unknown): string {
  const seconds = Number(timestamp)
  if (!Number.isFinite(seconds) || seconds <= 0) return '-'
  return new Date(seconds * 1000).toLocaleString()
}

function channelConfigurationBlockClassName(
  status: ChannelConfigurationStatus,
  className?: string
) {
  return cn(
    'border-border/60 rounded-lg border p-4 transition-colors',
    status === 'configured' && 'border-primary/35 ring-primary/20 ring-1',
    status === 'error' && 'border-destructive/50 ring-destructive/20 ring-1',
    className
  )
}

function CardHeading(props: {
  title: string
  icon?: ReactNode
  iconTone?: IconBadgeTone
  status?: ChannelConfigurationStatus
}) {
  return (
    <div className='flex items-center gap-3'>
      {props.icon && (
        <IconBadge tone={props.iconTone} size='md'>
          {props.icon}
        </IconBadge>
      )}
      <h3 className='min-w-0 flex-1 text-sm font-semibold tracking-tight'>
        {props.title}
      </h3>
      {props.status && (
        <ChannelConfigurationStatusIndicator status={props.status} />
      )}
    </div>
  )
}

function SubHeading(props: {
  title: string
  icon?: ReactNode
  iconTone?: IconBadgeTone
  status?: ChannelConfigurationStatus
}) {
  return (
    <div className='flex items-center gap-2'>
      {props.icon && (
        <IconBadge tone={props.iconTone} size='xs'>
          {props.icon}
        </IconBadge>
      )}
      <h4 className='text-muted-foreground text-xs font-medium tracking-wide uppercase'>
        {props.title}
      </h4>
      {props.status && (
        <ChannelConfigurationStatusIndicator status={props.status} />
      )}
    </div>
  )
}

/** Stable empty list so memoized consumers do not re-run on every render. */
const NO_UPSTREAM_MODELS: string[] = []

export function ChannelMutateDrawer({
  open,
  onOpenChange,
  currentRow,
}: ChannelMutateDrawerProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const { setOpen } = useChannels()
  const currentUser = useAuthStore((s) => s.auth.user)
  const canEditSensitive = hasPermission(
    currentUser,
    ADMIN_PERMISSION_RESOURCES.CHANNEL,
    ADMIN_PERMISSION_ACTIONS.SENSITIVE_WRITE
  )
  const canOperateChannel = hasPermission(
    currentUser,
    ADMIN_PERMISSION_RESOURCES.CHANNEL,
    ADMIN_PERMISSION_ACTIONS.OPERATE
  )
  const canBindTaskPlugin = hasPermission(
    currentUser,
    ADMIN_PERMISSION_RESOURCES.TASK_PLUGIN,
    ADMIN_PERMISSION_ACTIONS.BIND
  )
  const canRevealChannelKey = currentUser?.role === ROLE.SUPER_ADMIN
  const [isCodexCredentialRefreshing, setIsCodexCredentialRefreshing] =
    useState(false)
  const initialModelsRef = useRef<string[]>([])
  const initialModelMappingRef = useRef<string>('')
  const initialStatusCodeMappingRef = useRef<string>('')
  const [statusCodeRiskOpen, setStatusCodeRiskOpen] = useState(false)
  const [statusCodeRiskDetailItems, setStatusCodeRiskDetailItems] = useState<
    string[]
  >([])
  const statusCodeRiskResolveRef = useRef<
    ((confirmed: boolean) => void) | null
  >(null)
  const [passthroughWarningKind, setPassthroughWarningKind] =
    useState<PassthroughKind | null>(null)
  const passthroughWarningResolveRef = useRef<
    ((confirmed: boolean) => void) | null
  >(null)
  const [missingModelsDialogOpen, setMissingModelsDialogOpen] = useState(false)
  const [missingModelsList, setMissingModelsList] = useState<string[]>([])
  const missingModelsResolveRef = useRef<
    ((action: MissingModelsAction) => void) | null
  >(null)
  const channelFormRef = useRef<HTMLFormElement>(null)
  const [modelConfiguration, setModelConfiguration] = useState<{
    pluginKey?: string
  } | null>(null)
  const [paramOverrideEditorOpen, setParamOverrideEditorOpen] = useState(false)
  const [advancedCustomEditorOpen, setAdvancedCustomEditorOpen] =
    useState(false)
  const [clipboardConnectionInfo, setClipboardConnectionInfo] =
    useState<ChannelConnectionInfo | null>(null)

  const isEditing = Boolean(currentRow)
  const requestedSide = isEditing ? 'left' : 'right'
  const [drawerSide, setDrawerSide] = useState<'left' | 'right'>(requestedSide)
  // The parent clears currentRow as soon as closing starts. Keep the last
  // open direction until the next opening, including the entire exit animation.
  if (open && drawerSide !== requestedSide) {
    setDrawerSide(requestedSide)
  }
  const channelId = currentRow?.id ?? null
  const sensitiveLocked = isEditing && !canEditSensitive
  const [providerTarget, setProviderTarget] =
    useState<ChannelProviderTarget | null>(null)
  const [choosingProvider, setChoosingProvider] = useState(true)
  const [configurationSection, setConfigurationSection] =
    useState<ChannelConfigurationSection>('connection')
  const [pendingErrorFocus, setPendingErrorFocus] = useState<string | null>(
    null
  )
  const [mappingDraftRequest, setMappingDraftRequest] =
    useState<ModelMappingDraftRequest | null>(null)
  const mappingDraftTokenRef = useRef(0)
  const [batchMapping, setBatchMapping] = useState<{
    source: ModelMappingBatchSource
  } | null>(null)
  const [redirectPanelOpen, setRedirectPanelOpen] = useState(false)
  const [redirectSyncModels, setRedirectSyncModels] = useState(true)
  const syncedAliasesRef = useRef(new Set<string>())
  const syncedTargetsRef = useRef(new Set<string>())
  const [viewportWide, setViewportWide] = useState(
    () => window.innerWidth >= REDIRECT_PANEL_MIN_VIEWPORT
  )
  useEffect(() => {
    const update = () =>
      setViewportWide(window.innerWidth >= REDIRECT_PANEL_MIN_VIEWPORT)
    update()
    window.addEventListener('resize', update)
    return () => window.removeEventListener('resize', update)
  }, [])
  const redirectPanelPlacement = useRedirectPanelPlacement(
    channelFormRef,
    redirectPanelOpen && viewportWide,
    drawerSide
  )
  const redirectPanelActive =
    redirectPanelOpen && viewportWide && redirectPanelPlacement !== null
  const showProviderPicker =
    choosingProvider && (!isEditing || Boolean(providerTarget))
  const providerControlRef = useRef<HTMLButtonElement>(null)
  const previousProviderPicker = useRef(false)
  useEffect(() => {
    if (open && previousProviderPicker.current && !showProviderPicker) {
      providerControlRef.current?.focus({ preventScroll: true })
    }
    previousProviderPicker.current = showProviderPicker
  }, [open, showProviderPicker])
  const loadedForm = useRef<{ channelId: number; snapshot: string } | null>(
    null
  )

  const { data: defaultBaseURLs } = useQuery({
    queryKey: channelsQueryKeys.defaultBaseURLs(),
    // Optional hints must not trigger the global error-page redirect.
    queryFn: () => getChannelDefaultBaseURLs().catch(() => null),
    enabled: open,
    staleTime: 5 * 60 * 1000,
  })

  // Fetch channel details if editing
  const {
    data: channelData,
    isLoading: isChannelLoading,
    isError: isChannelError,
    error: channelError,
    refetch: refetchChannel,
  } = useQuery({
    queryKey: channelsQueryKeys.detail(channelId || 0),
    queryFn: async () => requireServerSuccess(await getChannel(channelId || 0)),
    enabled: open && isEditing && Boolean(channelId),
    meta: { errorToast: false },
  })

  // Fetch available groups
  const { data: groupsData, isLoading: isLoadingGroups } = useQuery({
    queryKey: ['groups'],
    queryFn: async () => requireServerSuccess(await getGroups()),
    enabled: open && !showProviderPicker,
  })

  // Fetch all available models
  const { data: allModelsData } = useQuery({
    queryKey: ['channel_models'],
    queryFn: async () => requireServerSuccess(await getAllModels()),
    enabled: open && !showProviderPicker,
  })

  // Fetch prefill model groups
  const { data: prefillGroupsData } = useQuery({
    queryKey: ['prefill_groups', 'model'],
    queryFn: async () => requireServerSuccess(await getPrefillGroups('model')),
    enabled: open && !showProviderPicker,
  })

  const { copyToClipboard } = useCopyToClipboard()

  const { channelKey, isChannelKeyLoading, handleRevealKey, verification } =
    useChannelKeyDisclosure(open, channelId)

  // Check if this is a multi-key channel
  const isMultiKeyChannel =
    isEditing && channelData?.data?.channel_info?.is_multi_key === true

  // Form setup
  const form = useForm<ChannelFormValues>({
    resolver: zodResolver(channelFormSchema),
    defaultValues: CHANNEL_FORM_DEFAULT_VALUES,
  })

  // Watch values once for conditional fields and configuration indicators.
  const formValues = form.watch()
  const multiKeyMode = formValues.multi_key_mode
  const multiKeyType = formValues.multi_key_type
  const keyMode = formValues.key_mode
  const currentGroups = formValues.group
  const currentType = formValues.type
  const baseUrlPlaceholder = [CHANNEL_TYPE_VLLM, CHANNEL_TYPE_SGLANG].includes(
    currentType
  )
    ? t(
        getChannelTypeConfig(currentType).hints?.baseUrl ||
          FIELD_PLACEHOLDERS.BASE_URL
      )
    : defaultBaseURLs?.[currentType] || t(FIELD_PLACEHOLDERS.BASE_URL)
  const currentStatus = formValues.status
  const currentBaseUrl = formValues.base_url
  const currentTaskPluginKey = formValues.task_plugin_key
  const currentTaskExtendPluginKeys = formValues.task_extend_plugin_keys
  const currentKey = formValues.key
  const currentModels = formValues.models
  const currentModelMapping = formValues.model_mapping
  const awsKeyType = formValues.aws_key_type
  const vertexKeyType = formValues.vertex_key_type
  const upstreamModelUpdateCheckEnabled =
    formValues.upstream_model_update_check_enabled
  const currentSettings = formValues.settings
  const currentAdvancedCustom = formValues.advanced_custom
  const currentHeaderOverride = formValues.header_override
  const currentProxy = formValues.proxy
  const currentHttpProtocol = formValues.http_protocol
  const {
    unlocked: doubaoApiEditUnlocked,
    handleClick: handleApiConfigSecretClick,
    reset: resetDoubaoApiUnlock,
  } = useHiddenClickUnlock({
    requiredClicks: 10,
    disabled: currentType !== 45 || sensitiveLocked,
    onUnlock: () => {
      toast.info(t('Doubao custom API address editing unlocked'))
    },
  })

  useEffect(() => {
    if (!open) {
      resetDoubaoApiUnlock()
    }
  }, [open, resetDoubaoApiUnlock])

  const applyConnectionInfo = useCallback(
    (connectionInfo: ChannelConnectionInfo) => {
      form.setValue('key', connectionInfo.key, {
        shouldDirty: true,
        shouldValidate: true,
      })
      form.setValue('base_url', connectionInfo.url, {
        shouldDirty: true,
        shouldValidate: true,
      })
      setClipboardConnectionInfo(null)
      toast.success(t('Connection info filled in'))
    },
    [form, t]
  )

  const pasteConnectionInfoFromClipboard = useCallback(async () => {
    if (typeof navigator === 'undefined' || !navigator.clipboard?.readText) {
      toast.error(t('Unable to read clipboard'))
      return
    }

    try {
      const text = await navigator.clipboard.readText()
      const parsed = parseChannelConnectionInfo(text)
      if (parsed) {
        applyConnectionInfo(parsed)
        return
      }
      toast.info(t('No connection info found in clipboard'))
    } catch {
      toast.error(t('Unable to read clipboard'))
    }
  }, [applyConnectionInfo, t])

  useEffect(() => {
    if (!open || isEditing || showProviderPicker) {
      setClipboardConnectionInfo(null)
      return
    }

    if (typeof navigator === 'undefined' || !navigator.clipboard?.readText) {
      return
    }

    let cancelled = false
    void navigator.clipboard
      .readText()
      .then((text) => {
        if (cancelled) return
        setClipboardConnectionInfo(parseChannelConnectionInfo(text))
      })
      .catch(() => {
        /* Clipboard detection is best-effort on drawer open. */
      })

    return () => {
      cancelled = true
    }
  }, [isEditing, open, showProviderPicker])

  // Helper computed values
  const isBatchMode =
    multiKeyMode === 'batch' || multiKeyMode === 'multi_to_single'
  const isChannelDetailLoading = isEditing && isChannelLoading
  const supportsMultiKeyAddMode =
    currentType !== 57 && !(currentType === 41 && vertexKeyType === 'api_key')
  const addModeOptions = useMemo(
    () =>
      supportsMultiKeyAddMode
        ? ADD_MODE_OPTIONS
        : ADD_MODE_OPTIONS.filter((option) => option.value === 'single'),
    [supportsMultiKeyAddMode]
  )

  const advancedCustomStats = useMemo(
    () => getAdvancedCustomStats(currentAdvancedCustom),
    [currentAdvancedCustom]
  )
  const advancedCustomRouteTypeLabels =
    advancedCustomStats.routeTypeLabels.slice(
      0,
      ADVANCED_CUSTOM_ROUTE_TYPE_PREVIEW_LIMIT
    )
  const hiddenAdvancedCustomRouteTypeCount =
    advancedCustomStats.routeTypeLabels.length -
    advancedCustomRouteTypeLabels.length
  const advancedCustomRouteTypeTitle =
    hiddenAdvancedCustomRouteTypeCount > 0
      ? advancedCustomStats.routeTypeLabels.join(', ')
      : undefined

  // Get all models list
  const allModelsList = useMemo(
    () => allModelsData?.data?.map((model) => model.id).filter(Boolean) || [],
    [allModelsData]
  )

  // Get basic models for the current channel type
  const basicModels = useMemo(() => {
    if (!allModelsList.length) return []
    // Filter models based on common patterns for specific types
    if (currentType === 1) {
      return allModelsList.filter(
        (model) => model.startsWith('gpt-') || model.startsWith('text-')
      )
    }
    return allModelsList
  }, [allModelsList, currentType])

  // Get prefill groups
  const prefillGroups = useMemo(
    () => prefillGroupsData?.data || [],
    [prefillGroupsData]
  )

  // Transform groups to multi-select options
  const groupOptions = useMemo(() => {
    if (!groupsData?.data) return []
    const allGroups = new Set([...groupsData.data, ...(currentGroups || [])])
    return [...allGroups].map((group) => ({
      value: group,
      label: group,
    }))
  }, [groupsData, currentGroups])

  // Parse current models as array
  const currentModelsArray = useMemo(
    () => parseModelsString(currentModels),
    [currentModels]
  )

  const currentTypeLabel = useMemo(
    () =>
      CHANNEL_TYPE_OPTIONS.find((option) => option.value === currentType)
        ?.label || `#${currentType}`,
    [currentType]
  )
  const taskPluginOptionsQuery = useQuery({
    queryKey: ['task-plugin-options'],
    queryFn: async () => requireServerSuccess(await getTaskPluginOptions()),
    enabled: open && canBindTaskPlugin,
    meta: { errorToast: false },
  })
  const canHavePluginExtensions = supportsChannelPluginExtensions(currentType)
  const pluginExtensions = useMemo(() => {
    if (!canBindTaskPlugin || !taskPluginOptionsQuery.isSuccess) return []
    return getChannelPluginExtensions(
      currentType,
      taskPluginOptionsQuery.data,
      currentTaskExtendPluginKeys
    )
  }, [
    canBindTaskPlugin,
    currentType,
    currentTaskExtendPluginKeys,
    taskPluginOptionsQuery.isSuccess,
    taskPluginOptionsQuery.data,
  ])
  const boundTaskPlugin =
    currentType === CHANNEL_TYPE_TASK_PLUGIN
      ? taskPluginOptionsQuery.data?.find(
          (item) => item.key === currentTaskPluginKey
        )
      : undefined
  const providerLabel =
    boundTaskPlugin?.name ||
    (currentType === CHANNEL_TYPE_TASK_PLUGIN && currentTaskPluginKey) ||
    t(currentTypeLabel)

  const selectProvider = useCallback(
    (target: ChannelProviderTarget) => {
      if (!canEditSensitive) return
      if (
        (target.kind === 'builtin' &&
          providerTarget?.kind === 'builtin' &&
          target.type === providerTarget.type) ||
        (target.kind === 'plugin' &&
          providerTarget?.kind === 'plugin' &&
          target.key === providerTarget.key)
      ) {
        setChoosingProvider(false)
        return
      }
      if (target.kind === 'plugin') {
        if (!canBindTaskPlugin) return
        const plugin = taskPluginOptionsQuery.data?.find(
          (item) => item.key === target.key
        )
        if (!plugin) return
        const previousPlugin = taskPluginOptionsQuery.data?.find(
          (item) => item.key === form.getValues('task_plugin_key')
        )
        form.setValue('type', CHANNEL_TYPE_TASK_PLUGIN, { shouldDirty: true })
        form.setValue('task_plugin_key', plugin.key, { shouldDirty: true })
        if (!isEditing && !providerTarget && !form.getValues('name').trim()) {
          form.setValue('name', plugin.name)
        }
        if (plugin.models.length) {
          form.setValue('models', formatModelsArray(plugin.models), {
            shouldDirty: true,
          })
        }
        const baseUrl = nextTaskPluginBaseUrl(
          form.getValues('base_url'),
          previousPlugin?.baseUrl,
          plugin.baseUrl
        )
        if (baseUrl !== null) {
          form.setValue('base_url', baseUrl, {
            shouldDirty: true,
            shouldValidate: true,
          })
        }
      } else {
        if (
          !Number.isSafeInteger(target.type) ||
          target.type <= 0 ||
          target.type === CHANNEL_TYPE_TASK_PLUGIN
        ) {
          return
        }
        form.setValue('type', target.type, { shouldDirty: true })
        if (!isEditing && !providerTarget && !form.getValues('name').trim()) {
          const label = CHANNEL_TYPE_OPTIONS.find(
            (option) => option.value === target.type
          )?.label
          form.setValue('name', label ? t(label) : `#${target.type}`)
        }
      }
      setProviderTarget(target)
      setChoosingProvider(false)
    },
    [
      canBindTaskPlugin,
      canEditSensitive,
      providerTarget,
      isEditing,
      form,
      t,
      taskPluginOptionsQuery.data,
    ]
  )
  // The plugin author proposes the destination host once a default is
  // prefilled, so the admin is told when the key would travel over plain HTTP
  // or to a private network before the channel is saved.
  const taskPluginBaseUrlTrust =
    currentType === CHANNEL_TYPE_TASK_PLUGIN
      ? assessBaseUrlTrust(currentBaseUrl)
      : null

  const formErrors = form.formState.errors
  const configuration = getChannelConfigurationState(
    formValues,
    formErrors,
    isEditing
  )

  // Extract redirect models from model_mapping (target values)
  const redirectModelList = useMemo(
    () => extractRedirectModels(currentModelMapping || ''),
    [currentModelMapping]
  )

  // Extract source keys from model_mapping (models being remapped FROM)
  const redirectModelKeyList = useMemo(
    () => extractMappingSourceModels(currentModelMapping || ''),
    [currentModelMapping]
  )

  const modelMappingGuardrail = useMemo<ModelMappingGuardrail>(() => {
    if (!currentModelMapping?.trim()) {
      return createEmptyModelMappingGuardrail()
    }

    try {
      const parsed = JSON.parse(currentModelMapping)
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
        return { ...createEmptyModelMappingGuardrail(), invalidJson: true }
      }

      const entries = Object.entries(parsed).reduce<
        Array<{ source: string; target: string }>
      >((acc, [rawSource, rawTarget]) => {
        const source = String(rawSource).trim()
        const target = String(rawTarget ?? '').trim()

        if (!source || !target) {
          return acc
        }

        acc.push({ source, target })
        return acc
      }, [])

      const missingSourceModels = [
        ...new Set(
          entries
            .filter(
              (entry) =>
                Boolean(entry.source) &&
                !currentModelsArray.includes(entry.source)
            )
            .map((entry) => entry.source)
        ),
      ]

      const exposedTargetModels = [
        ...new Set(
          entries
            .filter(
              (entry) =>
                Boolean(entry.target) &&
                currentModelsArray.includes(entry.target)
            )
            .map((entry) => entry.target)
        ),
      ]

      return {
        invalidJson: false,
        entries,
        missingSourceModels,
        exposedTargetModels,
      }
    } catch {
      return { ...createEmptyModelMappingGuardrail(), invalidJson: true }
    }
  }, [currentModelMapping, currentModelsArray])

  const mappingPreviewPairs =
    modelMappingGuardrail.entries.length > 0
      ? modelMappingGuardrail.entries.slice(0, 3)
      : MODEL_MAPPING_PREVIEW_FALLBACK
  const remainingMappingCount =
    modelMappingGuardrail.entries.length > 3
      ? modelMappingGuardrail.entries.length - 3
      : 0
  const mappingCount = modelMappingGuardrail.entries.length
  const upstreamAliases = useMemo(() => {
    const aliases: Record<string, string> = {}
    for (const entry of modelMappingGuardrail.entries) {
      aliases[entry.target] ??= entry.source
    }
    return aliases
  }, [modelMappingGuardrail.entries])

  // Transform models to multi-select options. Redirected models carry their
  // upstream target as a hint so the picker shows which names are aliases.
  const modelOptions = useMemo(() => {
    const targetBySource = new Map(
      modelMappingGuardrail.entries.map((entry) => [entry.source, entry.target])
    )
    const allModels = new Set([
      ...allModelsList,
      ...currentModelsArray,
      ...pluginExtensions.flatMap((plugin) => plugin.models),
    ])
    return [...allModels].map((model) => {
      const target = targetBySource.get(model)
      return {
        value: model,
        label: model,
        hint: target
          ? t('Redirects to {{model}}', { model: target })
          : undefined,
      }
    })
  }, [
    allModelsList,
    currentModelsArray,
    pluginExtensions,
    modelMappingGuardrail.entries,
    t,
  ])

  const upstreamUpdateMeta = useMemo(() => {
    const settings = parseSettingsRecord(currentSettings)
    const detectedModels = Array.isArray(
      settings.upstream_model_update_last_detected_models
    )
      ? settings.upstream_model_update_last_detected_models
          .map((model) => String(model || '').trim())
          .filter(Boolean)
      : []

    return {
      lastCheckTime: settings.upstream_model_update_last_check_time,
      detectedModels: [...new Set(detectedModels)],
    }
  }, [currentSettings])

  const upstreamDetectedModelsPreview = upstreamUpdateMeta.detectedModels.slice(
    0,
    UPSTREAM_DETECTED_MODEL_PREVIEW_LIMIT
  )
  const upstreamDetectedModelsOmittedCount =
    upstreamUpdateMeta.detectedModels.length -
    upstreamDetectedModelsPreview.length

  // Load channel data into form when editing
  useEffect(() => {
    if (!open) {
      setModelConfiguration(null)
      form.reset(CHANNEL_FORM_DEFAULT_VALUES)
      loadedForm.current = null
      setProviderTarget(null)
      setChoosingProvider(true)
      setConfigurationSection('connection')
      setPendingErrorFocus(null)
      return
    }
    if (isEditing && channelData?.data) {
      const isNewChannel = loadedForm.current?.channelId !== channelId
      // Model selectors also change values without setting RHF's dirty flag.
      // Refresh untouched forms, while retaining every kind of unsaved input.
      if (
        !isNewChannel &&
        loadedForm.current?.snapshot !== JSON.stringify(form.getValues())
      ) {
        return
      }
      const defaults = transformChannelToFormDefaults(channelData.data)
      form.reset(defaults)
      loadedForm.current = {
        channelId: channelData.data.id,
        snapshot: JSON.stringify(form.getValues()),
      }
      setProviderTarget(
        defaults.type === CHANNEL_TYPE_TASK_PLUGIN
          ? { kind: 'plugin', key: defaults.task_plugin_key || '' }
          : { kind: 'builtin', type: defaults.type }
      )
      if (isNewChannel) {
        setModelConfiguration(null)
        setChoosingProvider(false)
        setConfigurationSection('connection')
        setPendingErrorFocus(null)
      }
      // Store initial values for comparison
      initialModelsRef.current = parseModelsString(
        channelData.data.models || ''
      )
      initialModelMappingRef.current = channelData.data.model_mapping || ''
      initialStatusCodeMappingRef.current =
        channelData.data.status_code_mapping || ''
    } else if (!isEditing) {
      form.reset(CHANNEL_FORM_DEFAULT_VALUES)
      initialModelsRef.current = []
      initialModelMappingRef.current = ''
      initialStatusCodeMappingRef.current = ''
    }
  }, [isEditing, channelId, channelData, form, open])

  // Handle type change - set default values for specific types
  useEffect(() => {
    if (isEditing) return // Don't auto-set defaults when editing

    // Type 45 (VolcEngine) - set default base_url
    if (currentType === 45) {
      const currentBaseUrlValue = form.getValues('base_url')
      if (!currentBaseUrlValue || currentBaseUrlValue === '') {
        form.setValue('base_url', 'https://ark.cn-beijing.volces.com')
      }
    }

    // Type 18 (Xunfei) - set default other (version)
    if (currentType === 18) {
      const currentOther = form.getValues('other')
      if (!currentOther || currentOther === '') {
        form.setValue('other', 'v2.1')
      }
    }
  }, [currentType, isEditing, form])

  useEffect(() => {
    if (currentType !== 45 || currentBaseUrl !== 'doubao-coding-plan') return

    form.setValue('base_url', 'https://ark.cn-beijing.volces.com', {
      shouldDirty: false,
      shouldValidate: true,
    })
  }, [currentBaseUrl, currentType, form])

  useEffect(() => {
    if (isEditing || supportsMultiKeyAddMode) return
    if (multiKeyMode && multiKeyMode !== 'single') {
      form.setValue('multi_key_mode', 'single', {
        shouldDirty: true,
        shouldValidate: true,
      })
    }
  }, [form, isEditing, multiKeyMode, supportsMultiKeyAddMode])

  // Validate base_url - warn if it ends with /v1
  useEffect(() => {
    if (!currentBaseUrl || !currentBaseUrl.endsWith('/v1')) return

    // Show warning toast
    const timer = setTimeout(() => {
      toast.warning(
        t(
          'Warning: Base URL should not end with /v1. New API will handle it automatically. This may cause request failures.'
        ),
        { duration: 5000 }
      )
    }, 500)

    return () => clearTimeout(timer)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [currentBaseUrl])

  // Handle key deduplication
  const handleDeduplicateKeys = () => {
    const currentKey = form.getValues('key')
    if (!currentKey || currentKey.trim() === '') {
      toast.info(t('Please enter keys first'))
      return
    }

    const result = deduplicateKeys(currentKey)

    if (result.removedCount === 0) {
      toast.info(t('No duplicate keys found'))
    } else {
      form.setValue('key', result.deduplicatedText)
      toast.success(
        t(
          'Removed {{removed}} duplicate key(s). Before: {{before}}, After: {{after}}',
          {
            removed: result.removedCount,
            before: result.beforeCount,
            after: result.afterCount,
          }
        )
      )
    }
  }

  const handleRefreshCodexCredential = useCallback(async () => {
    if (!channelId) return
    setIsCodexCredentialRefreshing(true)
    try {
      const res = await refreshCodexCredential(channelId)
      if (!res.success) {
        throw createServerError(res, t('Failed to refresh credential'))
      }
      toast.success(t('Credential refreshed'))
      queryClient.invalidateQueries({
        queryKey: channelsQueryKeys.detail(channelId),
      })
    } catch (error) {
      handleServerError(error, t('Refresh failed'))
    } finally {
      setIsCodexCredentialRefreshing(false)
    }
  }, [channelId, queryClient, t])

  // Unified function to update models
  const updateModels = useCallback(
    (newModels: string[], merge: boolean = false) => {
      const finalModels = merge
        ? formatModelsArray([...currentModelsArray, ...newModels])
        : formatModelsArray(newModels)
      form.setValue('models', finalModels)
      return newModels.length
    },
    [currentModelsArray, form]
  )

  // Ordinary edits use the saved channel. Advanced Custom retains its existing
  // preview path, which reuses the saved credential on the server.
  const previewModels =
    !isEditing ||
    (currentType === CHANNEL_TYPE_ADVANCED_CUSTOM && canEditSensitive)
  const canDiscoverModels = previewModels ? canEditSensitive : canOperateChannel
  const previewKey = isEditing ? undefined : currentKey
  const previewRequest = useMemo<ChannelModelDiscoveryRequest>(
    () => ({
      kind: 'preview',
      data: {
        type: currentType,
        key: previewKey,
        channel_id: isEditing ? channelId || undefined : undefined,
        base_url: currentBaseUrl || '',
        advanced_custom: currentAdvancedCustom,
        header_override: currentHeaderOverride,
        proxy: currentProxy,
      },
    }),
    [
      currentType,
      previewKey,
      isEditing,
      channelId,
      currentBaseUrl,
      currentAdvancedCustom,
      currentHeaderOverride,
      currentProxy,
    ]
  )
  const savedRequest = useMemo<ChannelModelDiscoveryRequest>(
    () => ({ kind: 'saved', channelId: channelId || 0 }),
    [channelId]
  )
  const discovery = useChannelModelDiscovery({
    enabled:
      open &&
      canDiscoverModels &&
      MODEL_FETCHABLE_TYPES.has(currentType) &&
      (!isEditing || Boolean(channelData?.data)),
    request: previewModels ? previewRequest : savedRequest,
  })
  const upstreamModelList =
    discovery.status === 'success' ? discovery.models : NO_UPSTREAM_MODELS
  const batchMappingSource: ModelMappingBatchSource =
    upstreamModelList.length > 0 ? 'upstream' : 'channel'
  const canBatchMap =
    currentModelsArray.length > 0 || upstreamModelList.length > 0
  const fetchDiscoveredModels = discovery.fetch
  const handleFetchModels = useCallback(async () => {
    const type = form.getValues('type')
    if (!MODEL_FETCHABLE_TYPES.has(type)) {
      toast.error(t('This channel type does not support fetching models'))
      return
    }
    if (!canDiscoverModels) {
      toast.error(t("You don't have necessary permission"))
      return
    }
    if (
      !isEditing &&
      type !== CHANNEL_TYPE_ADVANCED_CUSTOM &&
      !form.getValues('key')?.trim()
    ) {
      form.setError('key', {
        type: 'manual',
        message: ERROR_MESSAGES.REQUIRED_KEY,
      })
      setConfigurationSection('connection')
      setPendingErrorFocus('key')
      return
    }
    await fetchDiscoveredModels()
  }, [isEditing, canDiscoverModels, form, t, fetchDiscoveredModels])

  // Handle model operations
  const handleFillRelatedModels = useCallback(() => {
    if (!basicModels.length) {
      toast.info(t('No related models available for this channel type'))
      return
    }
    updateModels(basicModels)
    toast.success(
      t('Filled {{count}} related model(s)', { count: basicModels.length })
    )
  }, [basicModels, updateModels, t])

  const handleClearModels = useCallback(() => {
    form.setValue('models', '')
    toast.success(t('Cleared all models'))
  }, [form, t])

  const handleCopyModels = useCallback(async () => {
    const models = form.getValues('models')
    if (!models?.trim()) {
      toast.info(t('No models to copy'))
      return
    }
    await copyToClipboard(models)
  }, [form, copyToClipboard, t])

  // Handle adding prefill group models
  const handleAddPrefillGroup = useCallback(
    (group: { id: number; name: string; items: string | string[] }) => {
      try {
        const items = Array.isArray(group.items)
          ? group.items
          : JSON.parse(group.items)

        if (!Array.isArray(items)) {
          throw new Error('Invalid items format')
        }

        const count = updateModels(items, true)
        toast.success(
          t('Added {{count}} models from "{{name}}"', {
            count,
            name: group.name,
          })
        )
      } catch {
        toast.error(t('Failed to parse group items'))
      }
    },
    [updateModels, t]
  )

  // Handle model selection change from MultiSelect
  const handleModelsChange = useCallback(
    (selected: string[]) => {
      form.setValue('models', selected.join(','))
    },
    [form]
  )

  const taskPluginExtensionOptions = useMemo(
    () =>
      (taskPluginOptionsQuery.data ?? [])
        .filter(supportsNewAPIUpstream)
        .map((plugin) => ({
          value: plugin.key,
          label: plugin.name,
          hint: plugin.key,
          icon: <PluginIcon plugin={plugin} size={16} />,
        })),
    [taskPluginOptionsQuery.data]
  )

  // Binding an upstream plugin publishes its models like the type-61 prefill;
  // unbinding removes the models that no remaining bound plugin declares.
  const handleTaskExtendPluginKeysChange = useCallback(
    (keys: string[]) => {
      const plugins = taskPluginOptionsQuery.data ?? []
      const declaredBy = (bound: readonly string[]) =>
        new Set(
          plugins
            .filter((plugin) => bound.includes(plugin.key))
            .flatMap((plugin) => plugin.models)
        )
      const previous = form.getValues('task_extend_plugin_keys') ?? []
      const added = declaredBy(keys.filter((key) => !previous.includes(key)))
      const kept = declaredBy(keys)
      const dropped = declaredBy(previous.filter((key) => !keys.includes(key)))
      const models = parseModelsString(form.getValues('models') || '').filter(
        (model) => kept.has(model) || !dropped.has(model)
      )
      for (const model of added) {
        if (!models.includes(model)) models.push(model)
      }
      form.setValue('task_extend_plugin_keys', keys, { shouldDirty: true })
      form.setValue('models', models.join(','), { shouldDirty: true })
    },
    [form, taskPluginOptionsQuery.data]
  )

  const raiseMappingDraft = useCallback(
    (from: string, to: string, focus?: 'from' | 'to') => {
      mappingDraftTokenRef.current += 1
      setMappingDraftRequest({
        from,
        to,
        focus,
        token: mappingDraftTokenRef.current,
      })
    },
    []
  )

  // Open a mapping row for a model the user picked elsewhere and hand focus to
  // the editor on the routing tab. Drafts are saved once both names are set.
  const requestMappingDraft = useCallback(
    (from: string, to: string) => {
      setConfigurationSection('routing')
      raiseMappingDraft(from, to)
    },
    [raiseMappingDraft]
  )

  // Keep the model list in step with mappings edited from the redirect panel:
  // complete pairs publish the request name and hide the raw upstream name,
  // deleting a pair reverses that. The refs remember what this sync changed so
  // only its own additions and removals are undone.
  const syncModelsWithMapping = useCallback(
    (mapping: string) => {
      const aliasSet = new Set<string>()
      const targetSet = new Set<string>()
      let parsed: unknown
      try {
        parsed = JSON.parse(mapping.trim() || '{}')
      } catch {
        return
      }
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return
      for (const [from, to] of Object.entries(parsed)) {
        if (typeof to !== 'string' || !from.trim() || !to.trim()) continue
        aliasSet.add(from.trim())
        targetSet.add(to.trim())
      }
      let models = parseModelsString(form.getValues('models') || '')
      models = models.filter(
        (model) =>
          !(syncedAliasesRef.current.has(model) && !aliasSet.has(model))
      )
      for (const target of syncedTargetsRef.current) {
        if (!targetSet.has(target) && !models.includes(target)) {
          models.push(target)
        }
      }
      models = models.filter((model) => !targetSet.has(model))
      for (const alias of aliasSet) {
        if (!models.includes(alias)) models.push(alias)
      }
      syncedAliasesRef.current = aliasSet
      syncedTargetsRef.current = targetSet
      form.setValue('models', formatModelsArray(models))
    },
    [form]
  )

  const applyMappingPairs = useCallback(
    (pairs: ModelMappingPair[], sync: boolean) => {
      const merged = mergeModelMappingPairs(
        form.getValues('model_mapping') || '',
        pairs
      )
      if (merged === null) return
      form.setValue('model_mapping', merged, { shouldDirty: true })
      if (sync) syncModelsWithMapping(merged)
    },
    [form, syncModelsWithMapping]
  )

  const handleBatchMappingApply = useCallback(
    (result: ModelMappingBatchResult) =>
      applyMappingPairs(result.pairs, result.syncModels),
    [applyMappingPairs]
  )

  const handlePanelMappingChange = useCallback(
    (value: string) => {
      form.setValue('model_mapping', value, { shouldDirty: true })
    },
    [form]
  )

  const handlePanelMappingCommit = useCallback(
    (value: string) => {
      if (redirectSyncModels) syncModelsWithMapping(value)
    },
    [redirectSyncModels, syncModelsWithMapping]
  )

  const openRedirectPanel = useCallback(() => {
    // Pre-existing mappings count as already synced, so opening the panel
    // never rewrites the model list on its own.
    const models = new Set(parseModelsString(form.getValues('models') || ''))
    syncedAliasesRef.current = new Set(
      modelMappingGuardrail.entries
        .filter((entry) => models.has(entry.source))
        .map((entry) => entry.source)
    )
    syncedTargetsRef.current = new Set(
      modelMappingGuardrail.entries
        .filter((entry) => !models.has(entry.target))
        .map((entry) => entry.target)
    )
    setRedirectPanelOpen(true)
  }, [form, modelMappingGuardrail.entries])

  const closeRedirectPanel = useCallback(() => {
    setRedirectPanelOpen(false)
  }, [])

  // Open a draft without guessing or publishing a request name.
  const handleFetchedRedirect = useCallback(
    (model: string) => {
      if (window.innerWidth < REDIRECT_PANEL_MIN_VIEWPORT) {
        requestMappingDraft('', model)
        return
      }
      openRedirectPanel()
      raiseMappingDraft('', model, 'from')
    },
    [requestMappingDraft, openRedirectPanel, raiseMappingDraft]
  )

  // Handle successful submission
  const handleSuccess = useCallback(() => {
    queryClient.invalidateQueries({ queryKey: channelsQueryKeys.lists() })
    if (channelId) {
      queryClient.invalidateQueries({
        queryKey: channelsQueryKeys.detail(channelId),
      })
    }
    onOpenChange(false)
    setOpen(null)
  }, [channelId, queryClient, onOpenChange, setOpen])

  // Show missing models confirmation dialog
  const confirmMissingModelMappings = useCallback(
    (missingModels: string[]): Promise<MissingModelsAction> => {
      return new Promise((resolve) => {
        setMissingModelsList(missingModels)
        setMissingModelsDialogOpen(true)
        missingModelsResolveRef.current = resolve
      })
    },
    []
  )

  // Handle missing models dialog action
  const handleMissingModelsAction = useCallback(
    (action: MissingModelsAction) => {
      setMissingModelsDialogOpen(false)
      if (missingModelsResolveRef.current) {
        missingModelsResolveRef.current(action)
        missingModelsResolveRef.current = null
      }
    },
    []
  )

  const confirmStatusCodeRisk = useCallback(
    (detailItems: string[]): Promise<boolean> =>
      new Promise((resolve) => {
        statusCodeRiskResolveRef.current = resolve
        setStatusCodeRiskDetailItems(detailItems)
        setStatusCodeRiskOpen(true)
      }),
    []
  )

  const handleStatusCodeRiskAction = useCallback((confirmed: boolean) => {
    setStatusCodeRiskOpen(false)
    setStatusCodeRiskDetailItems([])
    if (statusCodeRiskResolveRef.current) {
      statusCodeRiskResolveRef.current(confirmed)
      statusCodeRiskResolveRef.current = null
    }
  }, [])

  // Passthrough bypasses model redirect, overrides and conversion, so every
  // control that enables it (quick options, request section, header template)
  // routes through this single confirmation before the value changes.
  const confirmEnablePassthrough = useCallback(
    (kind: PassthroughKind): Promise<boolean> =>
      new Promise((resolve) => {
        passthroughWarningResolveRef.current?.(false)
        passthroughWarningResolveRef.current = resolve
        setPassthroughWarningKind(kind)
      }),
    []
  )

  const handlePassthroughWarningAction = useCallback((confirmed: boolean) => {
    setPassthroughWarningKind(null)
    if (passthroughWarningResolveRef.current) {
      passthroughWarningResolveRef.current(confirmed)
      passthroughWarningResolveRef.current = null
    }
  }, [])

  useEffect(() => {
    return () => {
      passthroughWarningResolveRef.current?.(false)
      passthroughWarningResolveRef.current = null
    }
  }, [])

  useEffect(() => {
    return () => {
      if (statusCodeRiskResolveRef.current) {
        statusCodeRiskResolveRef.current(false)
        statusCodeRiskResolveRef.current = null
      }
    }
  }, [])

  const channelMutation = useChannelMutateForm({
    currentRow,
    isEditing,
    isMultiKeyChannel,
    onSuccess: handleSuccess,
  })

  const isSubmitting = channelMutation.isPending || form.formState.isSubmitting

  // Submit handler
  const onSubmit = useCallback(
    async (data: ChannelFormValues) => {
      if (isEditing && !channelData?.data) return
      if (!isEditing && (!providerTarget || !canEditSensitive)) return
      // Validate key is required when creating
      if (!isEditing && !data.key?.trim()) {
        form.setError('key', {
          type: 'manual',
          message: ERROR_MESSAGES.REQUIRED_KEY,
        })
        setConfigurationSection('connection')
        setPendingErrorFocus('key')
        return
      }

      if (sensitiveLocked) {
        const dirtyFields = form.formState.dirtyFields as Partial<
          Record<keyof ChannelFormValues, unknown>
        >
        const hasSensitiveChanges = SENSITIVE_FORM_FIELDS.some((field) =>
          Boolean(dirtyFields[field])
        )
        if (hasSensitiveChanges) {
          toast.error(
            t('You do not have permission to edit sensitive channel settings.')
          )
          return
        }
      }

      // Validate status_code_mapping entries
      if (data.status_code_mapping?.trim()) {
        const invalidEntries = collectInvalidStatusCodeEntries(
          data.status_code_mapping
        )
        if (invalidEntries.length > 0) {
          const message = t(
            'Invalid status code mapping entries: {{entries}}',
            {
              entries: invalidEntries.join(', '),
            }
          )
          form.setError('status_code_mapping', { type: 'manual', message })
          setConfigurationSection('request')
          setPendingErrorFocus('status_code_mapping')
          toast.error(message)
          return
        }

        const riskyRedirects = collectNewDisallowedStatusCodeRedirects(
          initialStatusCodeMappingRef.current,
          data.status_code_mapping
        )
        if (riskyRedirects.length > 0) {
          const confirmed = await confirmStatusCodeRisk(riskyRedirects)
          if (!confirmed) return
        }
      }

      // Validate model_mapping JSON format
      const hasModelMapping =
        typeof data.model_mapping === 'string' &&
        data.model_mapping.trim() !== ''
      const modelMappingValue = data.model_mapping || ''

      if (hasModelMapping) {
        const validation = validateModelMappingJson(modelMappingValue)
        if (!validation.valid) {
          form.setError('model_mapping', {
            type: 'manual',
            message: t(validation.error || 'Invalid model mapping'),
          })
          setConfigurationSection('routing')
          setPendingErrorFocus('model_mapping')
          handleServerError(
            validation,
            t(validation.error || 'Invalid model mapping')
          )
          return
        }
      }

      // Normalize models array
      const normalizedModels = parseModelsString(data.models || '')

      // Check for missing models in model_mapping
      if (hasModelMapping) {
        const missingModels = findMissingModelsInMapping(
          modelMappingValue,
          normalizedModels
        )

        const shouldPromptMissing =
          missingModels.length > 0 &&
          hasModelConfigChanged(
            normalizedModels,
            data.model_mapping || '',
            initialModelsRef.current,
            initialModelMappingRef.current
          )

        if (shouldPromptMissing) {
          const confirmAction = await confirmMissingModelMappings(missingModels)
          if (confirmAction === 'cancel') {
            return
          }
          if (confirmAction === 'add') {
            const updatedModels = [
              ...new Set([...normalizedModels, ...missingModels]),
            ]
            data.models = formatModelsArray(updatedModels)
            form.setValue('models', data.models)
          }
        }
      }

      try {
        await channelMutation.mutateAsync(data)
      } catch {
        // The mutation reports the server error; keep the draft open for correction.
      }
    },
    [
      isEditing,
      providerTarget,
      canEditSensitive,
      sensitiveLocked,
      channelData,
      form,
      confirmMissingModelMappings,
      confirmStatusCodeRisk,
      channelMutation,
      t,
    ]
  )

  const onInvalid: SubmitErrorHandler<ChannelFormValues> = useCallback(
    (errors) => {
      const field = Object.keys(errors)[0]
      if (field) {
        setConfigurationSection(getChannelConfigurationSection(field))
        setPendingErrorFocus(field)
      }
      toast.error(t('Please fix the highlighted fields before saving'))
    },
    [t]
  )

  useEffect(() => {
    if (!pendingErrorFocus || showProviderPicker) return
    const frame = window.requestAnimationFrame(() => {
      const panel = channelFormRef.current?.querySelector(
        '[role="tabpanel"]:not([hidden])'
      )
      const invalid = panel?.querySelector<HTMLElement>('[aria-invalid="true"]')
      const item = invalid?.closest<HTMLElement>('[data-slot="form-item"]')
      const controls =
        'input:not([type="hidden"]), textarea, button, [contenteditable="true"], [tabindex]:not([tabindex="-1"])'
      const focusTarget = invalid?.matches(controls)
        ? invalid
        : item?.querySelector<HTMLElement>(controls)
      item?.scrollIntoView({ block: 'center', behavior: 'smooth' })
      focusTarget?.focus({ preventScroll: true })
      setPendingErrorFocus(null)
    })
    return () => window.cancelAnimationFrame(frame)
  }, [pendingErrorFocus, configurationSection, showProviderPicker])

  // Handle drawer close
  const handleOpenChange = useCallback<
    NonNullable<ComponentProps<typeof Sheet>['onOpenChange']>
  >(
    (v, details) => {
      if (!v && isSubmitting) return
      if (
        !v &&
        showProviderPicker &&
        providerTarget &&
        details.reason === 'escape-key'
      ) {
        details.cancel()
        setChoosingProvider(false)
        return
      }
      onOpenChange(v)
      if (!v) {
        form.reset(CHANNEL_FORM_DEFAULT_VALUES)
        setClipboardConnectionInfo(null)
      }
    },
    [onOpenChange, form, isSubmitting, showProviderPicker, providerTarget]
  )

  const proxyFields = (
    <FormField
      control={form.control}
      name='proxy'
      render={({ field }) => (
        <FormItem>
          <FormLabel>{t('Proxy Address')}</FormLabel>
          <FormControl>
            <Input placeholder={t('socks5://user:pass@host:port')} {...field} />
          </FormControl>
          <FormDescription>
            {t(
              'Network proxy for this channel (supports HTTP, HTTPS, SOCKS5, and SOCKS5H)'
            )}
          </FormDescription>
          <FormMessage />
        </FormItem>
      )}
    />
  )

  const systemPromptFields = (
    <FormField
      control={form.control}
      name='system_prompt'
      render={({ field }) => (
        <FormItem>
          <FormLabel>{t('System Prompt')}</FormLabel>
          <FormControl>
            <Textarea
              placeholder={t(
                'Enter system prompt (user prompt takes priority)'
              )}
              rows={3}
              {...field}
            />
          </FormControl>
          <FormDescription>
            {t('Default system prompt for this channel')}
          </FormDescription>
          <FormMessage />
        </FormItem>
      )}
    />
  )

  const systemPromptOverrideFields = (
    <FormField
      control={form.control}
      name='system_prompt_override'
      render={({ field }) => (
        <FormItem className='flex items-center justify-between'>
          <div className='space-y-0.5'>
            <FormLabel>{t('System Prompt Concatenation')}</FormLabel>
            <FormDescription>
              {t('Concatenate channel system prompt with user&apos;s prompt')}
            </FormDescription>
          </div>
          <FormControl>
            <Switch
              disabled={sensitiveLocked}
              checked={field.value}
              onCheckedChange={field.onChange}
            />
          </FormControl>
        </FormItem>
      )}
    />
  )

  const passthroughFields = (
    <FormField
      control={form.control}
      name='pass_through_body_enabled'
      render={({ field }) => (
        <FormItem className='flex items-center justify-between px-4 py-3'>
          <div className='space-y-0.5'>
            <FormLabel>{t('Pass Through Body')}</FormLabel>
            <FormDescription>
              {t(
                'Preserve upstream-specific fields when API formats match; bypasses model redirect, parameter override and format conversion'
              )}
            </FormDescription>
          </div>
          <FormControl>
            <Switch
              disabled={sensitiveLocked}
              checked={field.value}
              onCheckedChange={(value) => {
                if (!value) {
                  field.onChange(false)
                  return
                }
                void confirmEnablePassthrough('body').then((confirmed) => {
                  if (confirmed) field.onChange(true)
                })
              }}
            />
          </FormControl>
        </FormItem>
      )}
    />
  )

  const thinkingFields = (
    <FormField
      control={form.control}
      name='thinking_to_content'
      render={({ field }) => (
        <FormItem className='flex items-center justify-between px-4 py-3'>
          <div className='space-y-0.5'>
            <FormLabel>{t('Thinking to Content')}</FormLabel>
            <FormDescription>
              {t('Convert reasoning_content to <think> tag in content')}
            </FormDescription>
          </div>
          <FormControl>
            <Switch
              disabled={sensitiveLocked}
              checked={field.value}
              onCheckedChange={field.onChange}
            />
          </FormControl>
        </FormItem>
      )}
    />
  )

  const taskPollingFields = (
    <FormField
      control={form.control}
      name='disable_task_polling_sleep'
      render={({ field }) => (
        <FormItem className='flex items-center justify-between px-4 py-3'>
          <div className='space-y-0.5'>
            <FormLabel>{t('Skip async task polling delay')}</FormLabel>
            <FormDescription>
              {t(
                'Do not wait one second between polling async tasks for this channel'
              )}
            </FormDescription>
          </div>
          <FormControl>
            <Switch
              disabled={sensitiveLocked}
              checked={field.value}
              onCheckedChange={field.onChange}
            />
          </FormControl>
        </FormItem>
      )}
    />
  )

  const formatFields = currentType === 1 && (
    <FormField
      control={form.control}
      name='force_format'
      render={({ field }) => (
        <FormItem className='flex items-center justify-between px-4 py-3'>
          <div className='space-y-0.5'>
            <FormLabel>{t('Force Format')}</FormLabel>
            <FormDescription>
              {t(
                'Force format response to OpenAI standard (OpenAI channel only)'
              )}
            </FormDescription>
          </div>
          <FormControl>
            <Switch
              disabled={sensitiveLocked}
              checked={field.value}
              onCheckedChange={field.onChange}
            />
          </FormControl>
        </FormItem>
      )}
    />
  )

  const ollamaOpenAIChatFields = currentType === CHANNEL_TYPE_OLLAMA && (
    <FormField
      control={form.control}
      name='ollama_openai_chat'
      render={({ field }) => (
        <FormItem className='flex items-center justify-between px-4 py-3'>
          <div className='space-y-0.5'>
            <FormLabel>{t('Use OpenAI-compatible Ollama chat API')}</FormLabel>
            <FormDescription>
              {t(
                'Send chat completions to the OpenAI-compatible /v1/chat/completions instead of the native Ollama /api/chat'
              )}
            </FormDescription>
          </div>
          <FormControl>
            <Switch
              disabled={sensitiveLocked}
              checked={field.value === true}
              onCheckedChange={field.onChange}
            />
          </FormControl>
        </FormItem>
      )}
    />
  )

  const notesFields = (
    <div
      role='group'
      aria-label={t('Internal Notes')}
      className={channelConfigurationBlockClassName(
        configuration.blocks.internalNotes,
        'flex flex-col gap-4'
      )}
    >
      <SubHeading
        title={t('Internal Notes')}
        status={configuration.blocks.internalNotes}
        icon={<FileText className='h-3.5 w-3.5' />}
        iconTone='chart-3'
      />
      <div className='grid gap-4 sm:grid-cols-2'>
        <FormField
          control={form.control}
          name='tag'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Tag')}</FormLabel>
              <FormControl>
                <Input placeholder={t(FIELD_PLACEHOLDERS.TAG)} {...field} />
              </FormControl>
              <FormDescription>{t(FIELD_DESCRIPTIONS.TAG)}</FormDescription>
              <FormMessage />
            </FormItem>
          )}
        />

        <FormField
          control={form.control}
          name='remark'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Remark')}</FormLabel>
              <FormControl>
                <Textarea
                  placeholder={t(FIELD_PLACEHOLDERS.REMARK)}
                  rows={2}
                  {...field}
                />
              </FormControl>
              <FormDescription>{t(FIELD_DESCRIPTIONS.REMARK)}</FormDescription>
              <FormMessage />
            </FormItem>
          )}
        />
      </div>
    </div>
  )

  const httpProtocolFields = (
    <FormField
      control={form.control}
      name='http_protocol'
      render={({ field }) => (
        <FormItem>
          <FormLabel>{t('HTTP Protocol')}</FormLabel>
          <Select
            disabled={sensitiveLocked}
            items={[
              {
                value: 'auto',
                label: t('Auto'),
              },
              {
                value: 'http1',
                label: t('HTTP/1.1'),
              },
            ]}
            value={field.value || 'auto'}
            onValueChange={(value) => {
              const nextProtocol = value === 'http1' ? 'http1' : 'auto'
              field.onChange(nextProtocol)
              if (nextProtocol === 'http1') {
                form.setValue('http2_connection_shards', 1, {
                  shouldDirty: true,
                  shouldValidate: true,
                })
              }
            }}
          >
            <FormControl>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
            </FormControl>
            <SelectContent alignItemWithTrigger={false}>
              <SelectGroup>
                <SelectItem value='auto'>{t('Auto')}</SelectItem>
                <SelectItem value='http1'>{t('HTTP/1.1')}</SelectItem>
              </SelectGroup>
            </SelectContent>
          </Select>
          <FormDescription>
            {t(
              'Auto negotiates HTTP/2 when available. HTTP/1.1 forces multiple keep-alive connections under concurrency.'
            )}
          </FormDescription>
          <FormMessage />
        </FormItem>
      )}
    />
  )

  const httpShardsFields = (
    <FormField
      control={form.control}
      name='http2_connection_shards'
      render={({ field }) => {
        const http1Selected = currentHttpProtocol === 'http1'
        const shardItems = Array.from({ length: 8 }, (_, index) => {
          const value = String(index + 1)
          return { value, label: value }
        })
        return (
          <FormItem>
            <FormLabel>{t('HTTP/2 Connection Shards')}</FormLabel>
            <Select
              items={shardItems}
              value={String(field.value || 1)}
              disabled={sensitiveLocked || http1Selected}
              onValueChange={(value) => {
                field.onChange(Number(value))
              }}
            >
              <FormControl>
                <SelectTrigger disabled={sensitiveLocked || http1Selected}>
                  <SelectValue />
                </SelectTrigger>
              </FormControl>
              <SelectContent alignItemWithTrigger={false}>
                <SelectGroup>
                  {shardItems.map((item) => (
                    <SelectItem key={item.value} value={item.value}>
                      {item.label}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            <FormDescription>
              {http1Selected
                ? t(
                    'HTTP/2 connection shards are unavailable when HTTP/1.1 is selected.'
                  )
                : t(
                    'Spread HTTP/2 traffic across multiple reusable connections to the same upstream origin (1-8).'
                  )}
            </FormDescription>
            <FormMessage />
          </FormItem>
        )
      }}
    />
  )

  const routingFields = (
    <div
      role='group'
      aria-label={t('Routing Strategy')}
      className={channelConfigurationBlockClassName(
        configuration.blocks.routingStrategy,
        'flex flex-col gap-4'
      )}
    >
      <SubHeading
        title={t('Routing Strategy')}
        status={configuration.blocks.routingStrategy}
        icon={<Route className='h-3.5 w-3.5' />}
        iconTone='info'
      />
      <div className='grid gap-4 sm:grid-cols-2'>
        <FormField
          control={form.control}
          name='priority'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Priority')}</FormLabel>
              <FormControl>
                <Input
                  type='number'
                  placeholder='0'
                  {...field}
                  onChange={(e) => field.onChange(Number(e.target.value))}
                />
              </FormControl>
              <FormDescription>
                {t(FIELD_DESCRIPTIONS.PRIORITY)}
              </FormDescription>
              <FormMessage />
            </FormItem>
          )}
        />

        <FormField
          control={form.control}
          name='weight'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Weight')}</FormLabel>
              <FormControl>
                <Input
                  type='number'
                  placeholder='0'
                  {...field}
                  onChange={(e) => field.onChange(Number(e.target.value))}
                />
              </FormControl>
              <FormDescription>{t(FIELD_DESCRIPTIONS.WEIGHT)}</FormDescription>
              <FormMessage />
            </FormItem>
          )}
        />
      </div>

      <FormField
        control={form.control}
        name='test_model'
        render={({ field }) => (
          <FormItem>
            <FormLabel>{t('Test Model')}</FormLabel>
            <FormControl>
              <Input
                placeholder={t(FIELD_PLACEHOLDERS.TEST_MODEL)}
                {...field}
              />
            </FormControl>
            <FormDescription>
              {t(FIELD_DESCRIPTIONS.TEST_MODEL)}
            </FormDescription>
            <FormMessage />
          </FormItem>
        )}
      />

      <FormField
        control={form.control}
        name='auto_ban'
        render={({ field }) => (
          <FormItem className='flex items-center justify-between'>
            <div className='space-y-0.5'>
              <FormLabel>{t('Auto Ban')}</FormLabel>
              <FormDescription>
                {t(FIELD_DESCRIPTIONS.AUTO_BAN)}
              </FormDescription>
            </div>
            <FormControl>
              <Switch
                checked={field.value === 1}
                onCheckedChange={(checked) => field.onChange(checked ? 1 : 0)}
              />
            </FormControl>
          </FormItem>
        )}
      />
    </div>
  )

  const upstreamModelDetectionFields = MODEL_FETCHABLE_TYPES.has(
    currentType
  ) && (
    <div
      role='group'
      aria-label={t('Upstream Model Detection Settings')}
      className={channelConfigurationBlockClassName(
        configuration.blocks.upstreamModelDetection,
        'flex flex-col gap-4'
      )}
    >
      <CardHeading
        title={t('Upstream Model Detection Settings')}
        status={configuration.blocks.upstreamModelDetection}
        icon={<RefreshCw className='h-4 w-4' />}
        iconTone='info'
      />
      <fieldset
        disabled={sensitiveLocked}
        className='space-y-4 disabled:opacity-60'
      >
        <div className='divide-border space-y-0 divide-y border-y'>
          <FormField
            control={form.control}
            name='upstream_model_update_check_enabled'
            render={({ field }) => (
              <FormItem className='flex items-center justify-between px-4 py-3'>
                <div className='space-y-0.5'>
                  <FormLabel>{t('Upstream Model Update Check')}</FormLabel>
                  <FormDescription>
                    {t('Periodically check for upstream model changes')}
                  </FormDescription>
                  <FormMessage />
                </div>
                <FormControl>
                  <Switch
                    disabled={sensitiveLocked}
                    checked={field.value}
                    onCheckedChange={field.onChange}
                  />
                </FormControl>
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name='upstream_model_update_auto_sync_enabled'
            render={({ field }) => (
              <FormItem className='flex items-center justify-between px-4 py-3'>
                <div className='space-y-0.5'>
                  <FormLabel>{t('Auto Sync Upstream Models')}</FormLabel>
                  <FormDescription>
                    {t(
                      'Automatically sync model list when upstream changes are detected'
                    )}
                  </FormDescription>
                </div>
                <FormControl>
                  <Switch
                    checked={field.value}
                    disabled={
                      sensitiveLocked || !upstreamModelUpdateCheckEnabled
                    }
                    onCheckedChange={field.onChange}
                  />
                </FormControl>
              </FormItem>
            )}
          />
        </div>
        <FormField
          control={form.control}
          name='upstream_model_update_ignored_models'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Ignored upstream models')}</FormLabel>
              <FormControl>
                <Input
                  placeholder={t(
                    'e.g., gpt-4.1-nano,regex:^claude-.*$,regex:^sora-.*$'
                  )}
                  {...field}
                />
              </FormControl>
              <FormDescription>
                {t(
                  'Comma-separated exact model names. Prefix with regex: to ignore by regular expression.'
                )}
              </FormDescription>
              <FormMessage />
            </FormItem>
          )}
        />
        <div className='text-muted-foreground space-y-2 border-t pt-3 text-xs'>
          <div>
            <span className='text-foreground font-medium'>
              {t('Last check time')}:
            </span>{' '}
            {formatUnixTime(upstreamUpdateMeta.lastCheckTime)}
          </div>
          <div>
            <span className='text-foreground font-medium'>
              {t('Last detected addable models')}:
            </span>{' '}
            {upstreamUpdateMeta.detectedModels.length === 0 ? (
              t('None')
            ) : (
              <>
                <span className='break-all'>
                  {upstreamDetectedModelsPreview.join(', ')}
                </span>
                {upstreamDetectedModelsOmittedCount > 0 && (
                  <span className='ml-1'>
                    {t('({{total}} total, {{omit}} omitted)', {
                      total: upstreamUpdateMeta.detectedModels.length,
                      omit: upstreamDetectedModelsOmittedCount,
                    })}
                  </span>
                )}
              </>
            )}
          </div>
        </div>
      </fieldset>
    </div>
  )

  const modelMappingFields = (
    <div
      role='group'
      aria-label={t('Model Mapping')}
      className={channelConfigurationBlockClassName(
        configuration.blocks.modelMapping
      )}
    >
      <FormField
        control={form.control}
        name='model_mapping'
        render={({ field }) => (
          <FormItem className='space-y-3'>
            <div className='flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between'>
              <div className='space-y-1'>
                <div className='flex items-center gap-2'>
                  <FormLabel className='mb-0'>{t('Model Mapping')}</FormLabel>
                  <ChannelConfigurationStatusIndicator
                    status={configuration.blocks.modelMapping}
                  />
                  <LearnMore
                    triggerProps={{
                      type: 'button',
                      'aria-label': t('How model mapping works'),
                      className: 'size-4 rounded-full',
                    }}
                    contentProps={{
                      collisionPadding: 16,
                      className:
                        'w-96 max-w-[calc(100vw-2rem)] gap-3 p-4 text-sm leading-6',
                    }}
                  >
                    <PopoverTitle className='text-foreground'>
                      {t('Request flow')}
                    </PopoverTitle>
                    <div className='space-y-2 font-mono'>
                      {mappingPreviewPairs.map((pair) => (
                        <div
                          key={`${pair.source}-${pair.target}`}
                          className='grid grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)] items-start gap-3'
                        >
                          <span className='min-w-0 wrap-anywhere'>
                            {pair.source}
                          </span>
                          <ArrowRight
                            className='mt-1 size-4 shrink-0'
                            aria-hidden='true'
                          />
                          <span className='min-w-0 wrap-anywhere'>
                            {pair.target}
                          </span>
                        </div>
                      ))}
                      {remainingMappingCount > 0 && (
                        <div>
                          +{remainingMappingCount} {t('more mapping')}
                          {remainingMappingCount > 1 ? 's' : ''}
                        </div>
                      )}
                    </div>
                    <PopoverDescription className='leading-6'>
                      {t(
                        'Users call the model on the left. The platform forwards the request to the upstream model on the right.'
                      )}
                    </PopoverDescription>
                  </LearnMore>
                </div>
                <FormDescription>
                  {t(FIELD_DESCRIPTIONS.MODEL_MAPPING)}
                </FormDescription>
              </div>
            </div>
            <FormControl>
              <ModelMappingEditor
                value={field.value || ''}
                onChange={field.onChange}
                disabled={isSubmitting}
                sourceModelOptions={currentModelsArray}
                targetModelOptions={modelOptions.map((option) => option.value)}
                onBatchAdd={
                  canBatchMap
                    ? () =>
                        setBatchMapping({
                          source: batchMappingSource,
                        })
                    : undefined
                }
                draftRequest={redirectPanelActive ? null : mappingDraftRequest}
                onDraftRequestHandled={() => setMappingDraftRequest(null)}
              />
            </FormControl>
            {modelMappingGuardrail.invalidJson && (
              <Alert variant='destructive'>
                <AlertDescription>
                  {t('Model Mapping must be a JSON object like')}{' '}
                  <code className='font-mono'>{'{"gpt-4":"Azure-GPT4"}'}</code>
                  {t('. Please fix the JSON before saving.')}
                </AlertDescription>
              </Alert>
            )}
            {modelMappingGuardrail.missingSourceModels.length > 0 && (
              <Alert className='border-amber-200 bg-amber-50 text-amber-900 dark:border-amber-500/40 dark:bg-amber-500/10 dark:text-amber-50'>
                <AlertDescription className='flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between'>
                  <span>
                    {t('Add')}{' '}
                    {formatModelNames(
                      modelMappingGuardrail.missingSourceModels
                    )}{' '}
                    {t(
                      'to the Models list so users can use them before the mapping sends traffic upstream.'
                    )}
                  </span>
                  <div className='flex shrink-0 flex-wrap gap-2'>
                    <Button
                      type='button'
                      variant='outline'
                      size='sm'
                      onClick={() => {
                        updateModels([
                          ...currentModelsArray,
                          ...modelMappingGuardrail.missingSourceModels,
                        ])
                      }}
                    >
                      {t('Add missing models')}
                    </Button>
                    <Button
                      type='button'
                      variant='ghost'
                      size='sm'
                      onClick={() => setConfigurationSection('connection')}
                    >
                      {t('View models')}
                    </Button>
                  </div>
                </AlertDescription>
              </Alert>
            )}
            <FormMessage />
          </FormItem>
        )}
      />
    </div>
  )

  const redirectPanelNotice = (
    <Alert>
      <AlertDescription className='flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between'>
        <span>
          {t('Model mappings are being edited in the floating panel.')}
        </span>
        <Button
          type='button'
          variant='outline'
          size='sm'
          onClick={closeRedirectPanel}
        >
          {t('Close the panel to edit here')}
        </Button>
      </AlertDescription>
    </Alert>
  )

  const basicSection = (
    <div className='scroll-mt-4'>
      <ChannelBasicSection>
        <div className='grid gap-4'>
          <FormField
            control={form.control}
            name='name'
            render={({ field }) => (
              <FormItem>
                <FormLabel required>{t('Name')}</FormLabel>
                <FormControl>
                  <Input placeholder={t(FIELD_PLACEHOLDERS.NAME)} {...field} />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
        </div>

        {!isEditing && (
          <FormField
            control={form.control}
            name='status'
            render={({ field }) => (
              <FormItem className={sideDrawerSwitchItemClassName()}>
                <div className='flex flex-col gap-0.5'>
                  <FormLabel>{t('Enabled')}</FormLabel>
                  <FormDescription className='text-xs'>
                    {t('Enable or disable this channel')}
                  </FormDescription>
                </div>
                <FormControl>
                  <Switch
                    checked={field.value === 1}
                    onCheckedChange={(checked) =>
                      field.onChange(checked ? 1 : 2)
                    }
                  />
                </FormControl>
              </FormItem>
            )}
          />
        )}

        {currentType === 1 && (
          <fieldset disabled={sensitiveLocked} className='disabled:opacity-60'>
            <FormField
              control={form.control}
              name='openai_organization'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('OpenAI Organization')}</FormLabel>
                  <FormControl>
                    <Input placeholder={t('org-...')} {...field} />
                  </FormControl>
                  <FormDescription>
                    {sensitiveLocked
                      ? t('No permission to perform this action')
                      : t(FIELD_DESCRIPTIONS.OPENAI_ORG)}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          </fieldset>
        )}
      </ChannelBasicSection>
    </div>
  )

  const overrideFields = (
    <div
      role='group'
      aria-label={t('Override Rules')}
      className={channelConfigurationBlockClassName(
        configuration.blocks.overrideRules,
        'flex flex-col gap-4'
      )}
    >
      <SubHeading
        title={t('Override Rules')}
        status={configuration.blocks.overrideRules}
        icon={<Code className='h-3.5 w-3.5' />}
        iconTone='chart-4'
      />

      <FormField
        control={form.control}
        name='status_code_mapping'
        render={({ field }) => (
          <FormItem className='space-y-3'>
            <div className='space-y-1'>
              <FormLabel>{t('Status Code Mapping')}</FormLabel>
              <FormDescription>
                {t('Map upstream status codes to different codes')}
              </FormDescription>
            </div>
            <FormControl>
              <JsonEditor
                value={field.value || ''}
                onChange={field.onChange}
                disabled={isSubmitting}
                keyPlaceholder='400'
                valuePlaceholder='500'
                keyLabel='Original Code'
                valueLabel='Mapped Code'
                emptyMessage={t('No status code mappings configured.')}
                template={{ '400': '500', '429': '503' }}
                valueType='string'
              />
            </FormControl>
            <FormMessage />
          </FormItem>
        )}
      />

      {sensitiveLocked && (
        <p className='text-muted-foreground text-xs'>
          {t('No permission to perform this action')}
        </p>
      )}
      <fieldset
        disabled={sensitiveLocked}
        className='space-y-4 disabled:opacity-60'
      >
        <FormField
          control={form.control}
          name='param_override'
          render={({ field }) => (
            <FormItem className='space-y-3 border-t pt-4'>
              <div className='flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between'>
                <div className='space-y-1'>
                  <FormLabel>{t('Parameter Override')}</FormLabel>
                  <FormDescription>
                    {t(
                      'Override request parameters. Cannot override stream parameter.'
                    )}
                  </FormDescription>
                </div>
                <div className='flex flex-wrap gap-2'>
                  <Button
                    type='button'
                    variant='outline'
                    size='sm'
                    onClick={() => setParamOverrideEditorOpen(true)}
                  >
                    <Wand2 className='mr-2 h-4 w-4' />
                    {t('Visual edit')}
                  </Button>
                  <Button
                    type='button'
                    variant='outline'
                    size='sm'
                    onClick={() => {
                      field.onChange(
                        JSON.stringify(
                          {
                            operations: [
                              {
                                path: 'temperature',
                                mode: 'set',
                                value: 0.7,
                                conditions: [
                                  {
                                    path: 'model',
                                    mode: 'prefix',
                                    value: 'gpt',
                                  },
                                ],
                                logic: 'AND',
                              },
                            ],
                          },
                          null,
                          2
                        )
                      )
                    }}
                  >
                    <Code className='mr-2 h-4 w-4' />
                    {t('New Format Template')}
                  </Button>
                  <Button
                    type='button'
                    variant='ghost'
                    size='sm'
                    onClick={() => field.onChange('')}
                  >
                    {t('Clear')}
                  </Button>
                </div>
              </div>
              <FormControl>
                <JsonCodeEditor
                  value={field.value || ''}
                  onChange={field.onChange}
                  name={field.name}
                  onBlur={field.onBlur}
                  textareaRef={field.ref}
                  disabled={sensitiveLocked || isSubmitting}
                  placeholder={t(
                    'Override request parameters. Cannot override stream parameter.'
                  )}
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />

        <FormField
          control={form.control}
          name='header_override'
          render={({ field }) => (
            <FormItem className='space-y-3 border-t pt-4'>
              <div className='flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between'>
                <div className='space-y-1'>
                  <FormLabel>{t('Request Header Override')}</FormLabel>
                  <FormDescription>
                    {t('Override request headers')}
                  </FormDescription>
                </div>
                <div className='flex flex-wrap gap-2'>
                  <Button
                    type='button'
                    variant='outline'
                    size='sm'
                    onClick={() =>
                      field.onChange(
                        JSON.stringify(
                          {
                            '*': true,
                            're:^X-Trace-.*$': true,
                            'X-Foo': '{client_header:X-Foo}',
                            Authorization: 'Bearer {api_key}',
                          },
                          null,
                          2
                        )
                      )
                    }
                  >
                    {t('Fill Template')}
                  </Button>
                  <Button
                    type='button'
                    variant='outline'
                    size='sm'
                    onClick={() => {
                      void confirmEnablePassthrough('headers').then(
                        (confirmed) => {
                          if (!confirmed) return
                          field.onChange(JSON.stringify({ '*': true }, null, 2))
                        }
                      )
                    }}
                  >
                    {t('Passthrough Template')}
                  </Button>
                  <Button
                    type='button'
                    variant='ghost'
                    size='sm'
                    onClick={() => field.onChange('')}
                  >
                    {t('Clear')}
                  </Button>
                </div>
              </div>
              <FormControl>
                <JsonCodeEditor
                  value={field.value || ''}
                  onChange={field.onChange}
                  name={field.name}
                  onBlur={field.onBlur}
                  textareaRef={field.ref}
                  disabled={sensitiveLocked || isSubmitting}
                  placeholder={t('Enter JSON to override request headers')}
                  heightClassName='h-40 min-h-40 max-h-40'
                />
              </FormControl>
              <FormDescription className='text-xs'>
                {t('Supported variables')}:{' '}
                <code className='bg-muted rounded px-1 py-0.5'>
                  {'{api_key}'}
                </code>{' '}
                — {t('Channel key')},{' '}
                <code className='bg-muted rounded px-1 py-0.5'>
                  {'{client_header:NAME}'}
                </code>{' '}
                — {t('Client header value')}
              </FormDescription>
              <FormMessage />
            </FormItem>
          )}
        />
      </fieldset>
    </div>
  )

  const fieldPassthroughFields = FIELD_PASSTHROUGH_TYPES.has(currentType) && (
    <div
      role='group'
      aria-label={t('Field passthrough controls')}
      className={channelConfigurationBlockClassName(
        configuration.blocks.fieldPassthrough,
        'flex flex-col gap-4'
      )}
    >
      <CardHeading
        title={t('Field passthrough controls')}
        status={configuration.blocks.fieldPassthrough}
        icon={<SlidersHorizontal className='h-4 w-4' />}
        iconTone='chart-4'
      />
      <fieldset disabled={sensitiveLocked} className='disabled:opacity-60'>
        <div className='divide-border space-y-0 divide-y border-y'>
          <FormField
            control={form.control}
            name='allow_service_tier'
            render={({ field }) => (
              <FormItem className='flex items-center justify-between gap-3 px-4 py-3'>
                <div className='space-y-0.5'>
                  <FormLabel className='text-sm'>
                    {t('Allow service_tier passthrough')}
                  </FormLabel>
                  <FormDescription>
                    {t('Pass through the service_tier field')}
                  </FormDescription>
                </div>
                <FormControl>
                  <Switch
                    disabled={sensitiveLocked}
                    checked={field.value}
                    onCheckedChange={field.onChange}
                  />
                </FormControl>
              </FormItem>
            )}
          />

          {OPENAI_FIELD_PASSTHROUGH_TYPES.has(currentType) && (
            <>
              <FormField
                control={form.control}
                name='disable_store'
                render={({ field }) => (
                  <FormItem className='flex items-center justify-between gap-3 px-4 py-3'>
                    <div className='space-y-0.5'>
                      <FormLabel className='text-sm'>
                        {t('Disable store passthrough')}
                      </FormLabel>
                      <FormDescription>
                        {t('When enabled, the store field will be blocked')}
                      </FormDescription>
                    </div>
                    <FormControl>
                      <Switch
                        disabled={sensitiveLocked}
                        checked={field.value}
                        onCheckedChange={field.onChange}
                      />
                    </FormControl>
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='allow_safety_identifier'
                render={({ field }) => (
                  <FormItem className='flex items-center justify-between gap-3 px-4 py-3'>
                    <div className='space-y-0.5'>
                      <FormLabel className='text-sm'>
                        {t('Allow safety_identifier passthrough')}
                      </FormLabel>
                      <FormDescription>
                        {t('Pass through the safety_identifier field')}
                      </FormDescription>
                    </div>
                    <FormControl>
                      <Switch
                        disabled={sensitiveLocked}
                        checked={field.value}
                        onCheckedChange={field.onChange}
                      />
                    </FormControl>
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='allow_include_obfuscation'
                render={({ field }) => (
                  <FormItem className='flex items-center justify-between gap-3 px-4 py-3'>
                    <div className='space-y-0.5'>
                      <FormLabel className='text-sm'>
                        {t('Allow include usage obfuscation passthrough')}
                      </FormLabel>
                      <FormDescription>
                        {t(
                          'Pass through the include field for usage obfuscation'
                        )}
                      </FormDescription>
                    </div>
                    <FormControl>
                      <Switch
                        disabled={sensitiveLocked}
                        checked={field.value}
                        onCheckedChange={field.onChange}
                      />
                    </FormControl>
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='allow_inference_geo'
                render={({ field }) => (
                  <FormItem className='flex items-center justify-between gap-3 px-4 py-3'>
                    <div className='space-y-0.5'>
                      <FormLabel className='text-sm'>
                        {t('Allow inference geography passthrough')}
                      </FormLabel>
                      <FormDescription>
                        {t(
                          'Pass through the inference_geo field for geographic routing'
                        )}
                      </FormDescription>
                    </div>
                    <FormControl>
                      <Switch
                        disabled={sensitiveLocked}
                        checked={field.value}
                        onCheckedChange={field.onChange}
                      />
                    </FormControl>
                  </FormItem>
                )}
              />
            </>
          )}

          {CLAUDE_FIELD_PASSTHROUGH_TYPES.has(currentType) && (
            <>
              {currentType === 14 && (
                <FormField
                  control={form.control}
                  name='allow_inference_geo'
                  render={({ field }) => (
                    <FormItem className='flex items-center justify-between gap-3 px-4 py-3'>
                      <div className='space-y-0.5'>
                        <FormLabel className='text-sm'>
                          {t('Allow inference_geo passthrough')}
                        </FormLabel>
                        <FormDescription>
                          {t(
                            'Pass through the inference_geo field for Claude data residency region control'
                          )}
                        </FormDescription>
                      </div>
                      <FormControl>
                        <Switch
                          disabled={sensitiveLocked}
                          checked={field.value}
                          onCheckedChange={field.onChange}
                        />
                      </FormControl>
                    </FormItem>
                  )}
                />
              )}

              <FormField
                control={form.control}
                name='allow_speed'
                render={({ field }) => (
                  <FormItem className='flex items-center justify-between gap-3 px-4 py-3'>
                    <div className='space-y-0.5'>
                      <FormLabel className='text-sm'>
                        {t('Allow speed passthrough')}
                      </FormLabel>
                      <FormDescription>
                        {t(
                          'Pass through the speed field for Claude inference speed mode control'
                        )}
                      </FormDescription>
                    </div>
                    <FormControl>
                      <Switch
                        disabled={sensitiveLocked}
                        checked={field.value}
                        onCheckedChange={field.onChange}
                      />
                    </FormControl>
                  </FormItem>
                )}
              />

              {currentType === 14 && (
                <FormField
                  control={form.control}
                  name='claude_beta_query'
                  render={({ field }) => (
                    <FormItem className='flex items-center justify-between gap-3 px-4 py-3'>
                      <div className='space-y-0.5'>
                        <FormLabel className='text-sm'>
                          {t('Allow Claude beta query passthrough')}
                        </FormLabel>
                        <FormDescription>
                          {t(
                            'Pass through the anthropic-beta header for beta features'
                          )}
                        </FormDescription>
                      </div>
                      <FormControl>
                        <Switch
                          disabled={sensitiveLocked}
                          checked={field.value}
                          onCheckedChange={field.onChange}
                        />
                      </FormControl>
                    </FormItem>
                  )}
                />
              )}
            </>
          )}
        </div>
      </fieldset>
    </div>
  )

  const modelsSection = (
    <div className='scroll-mt-4'>
      <ChannelModelsSection>
        <div className='space-y-5'>
          <div className='border-border/60 bg-muted/10 rounded-lg border p-4'>
            {currentType === CHANNEL_TYPE_NEW_API &&
              canBindTaskPlugin &&
              taskPluginOptionsQuery.isSuccess &&
              !showProviderPicker && (
                <FormField
                  control={form.control}
                  name='task_extend_plugin_keys'
                  render={({ field }) => (
                    <FormItem className='mb-4'>
                      <FormLabel>{t('Upstream task plugins')}</FormLabel>
                      <FormControl>
                        <MultiSelect
                          options={taskPluginExtensionOptions}
                          selected={field.value ?? []}
                          onChange={handleTaskExtendPluginKeysChange}
                          placeholder={t(
                            'Select the task plugins installed on the upstream gateway'
                          )}
                          maxVisibleChips={8}
                          disabled={!canEditSensitive}
                        />
                      </FormControl>
                      <FormDescription>
                        {t(
                          'This channel serves the models of every selected plugin. The upstream New API gateway must have the same plugins installed.'
                        )}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              )}
            <FormField
              control={form.control}
              name='models'
              render={() => (
                <FormItem
                  role='group'
                  aria-label={t('Models')}
                  className='space-y-3'
                >
                  <div className='flex items-start justify-between gap-3'>
                    <div className='min-w-0 space-y-1'>
                      <FormLabel required>{t('Models')}</FormLabel>
                      <FormDescription>
                        {t(FIELD_DESCRIPTIONS.MODELS)}
                      </FormDescription>
                    </div>
                    <Button
                      type='button'
                      variant='outline'
                      size='sm'
                      onClick={() => setModelConfiguration({})}
                      disabled={
                        currentModelsArray.length === 0 &&
                        !pluginExtensions.some(
                          (plugin) => plugin.models.length > 0
                        )
                      }
                    >
                      <Settings className='mr-2 h-4 w-4' aria-hidden='true' />
                      {t('Configure Models')}
                    </Button>
                  </div>
                  <FormControl>
                    <MultiSelect
                      options={modelOptions}
                      selected={currentModelsArray}
                      onChange={handleModelsChange}
                      placeholder={t('Select models or add custom ones')}
                      allowCreate
                      createLabel='Add custom model "{{value}}"'
                      maxVisibleChips={8}
                      copyChipOnClick
                    />
                  </FormControl>
                  <div className='text-muted-foreground flex flex-wrap items-center gap-x-2 gap-y-1 text-xs'>
                    <Route className='size-3.5 shrink-0' aria-hidden='true' />
                    {mappingCount > 0 && (
                      <span>
                        {t('{{count}} model(s) redirected', {
                          count: mappingCount,
                        })}
                      </span>
                    )}
                    <Button
                      type='button'
                      variant='link'
                      size='sm'
                      className='h-auto p-0 text-xs'
                      onClick={() => setConfigurationSection('routing')}
                    >
                      {mappingCount > 0
                        ? t('Edit mapping')
                        : t('Set up model redirects')}
                    </Button>
                  </div>
                  {canBindTaskPlugin &&
                    canHavePluginExtensions &&
                    !showProviderPicker && (
                      <>
                        {taskPluginOptionsQuery.isLoading && (
                          <LoadingState
                            inline
                            message={t('Loading plugins...')}
                          />
                        )}
                        {taskPluginOptionsQuery.isError && (
                          <ErrorState
                            className='min-h-0 p-3'
                            title={t('Failed to load plugins')}
                            onRetry={() => {
                              void taskPluginOptionsQuery.refetch()
                            }}
                          />
                        )}
                        <ChannelPluginExtensions
                          plugins={pluginExtensions}
                          selected={currentModelsArray}
                          onConfigure={(pluginKey) =>
                            setModelConfiguration({ pluginKey })
                          }
                        />
                      </>
                    )}
                  {modelMappingGuardrail.exposedTargetModels.length > 0 && (
                    <Alert className='border-amber-200 bg-amber-50 text-amber-900 dark:border-amber-500/40 dark:bg-amber-500/10 dark:text-amber-50'>
                      <AlertDescription className='flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between'>
                        <span>
                          {t('The mapped upstream model(s)')}{' '}
                          {formatModelNames(
                            modelMappingGuardrail.exposedTargetModels
                          )}{' '}
                          {t(
                            'are also listed here. Remove them from Models to keep the `/v1/models` response user-friendly and hide vendor-specific names.'
                          )}
                        </span>
                        <div className='flex shrink-0 flex-wrap gap-2'>
                          <Button
                            type='button'
                            variant='outline'
                            size='sm'
                            onClick={() => {
                              const hiddenTargets = new Set(
                                modelMappingGuardrail.exposedTargetModels
                              )
                              updateModels(
                                currentModelsArray.filter(
                                  (model) => !hiddenTargets.has(model)
                                )
                              )
                            }}
                          >
                            {t('Remove mapped targets')}
                          </Button>
                          <Button
                            type='button'
                            variant='ghost'
                            size='sm'
                            onClick={() => setConfigurationSection('routing')}
                          >
                            {t('View mapping')}
                          </Button>
                        </div>
                      </AlertDescription>
                    </Alert>
                  )}
                  <FormMessage />
                </FormItem>
              )}
            />

            {MODEL_FETCHABLE_TYPES.has(currentType) && (
              <div aria-live='polite' className='mt-4 space-y-3'>
                {discovery.status === 'loading' && (
                  <LoadingState
                    className='min-h-0 py-4'
                    message={t('Fetching models...')}
                  />
                )}
                {discovery.status === 'error' && (
                  <ErrorState
                    className='min-h-0 p-3'
                    title={t('Failed to fetch models')}
                    description={getServerErrorMessage(
                      discovery.error,
                      t('Failed to fetch models')
                    )}
                    onRetry={() => {
                      void handleFetchModels()
                    }}
                  />
                )}
                {discovery.status === 'stale' && (
                  <Alert>
                    <AlertDescription>
                      {t(
                        'Connection settings changed. Fetch models again to refresh the list.'
                      )}
                      <Button
                        type='button'
                        variant='outline'
                        size='sm'
                        onClick={handleFetchModels}
                      >
                        {t('Fetch Models')}
                      </Button>
                    </AlertDescription>
                  </Alert>
                )}
                {discovery.status === 'success' &&
                  discovery.models.length === 0 && (
                    <EmptyState
                      className='min-h-0 p-3'
                      title={t('No models returned by the upstream')}
                      description={t(
                        'You can add models manually or try fetching again.'
                      )}
                      action={
                        <Button
                          type='button'
                          variant='outline'
                          size='sm'
                          onClick={handleFetchModels}
                        >
                          {t('Retry')}
                        </Button>
                      }
                    />
                  )}
                {discovery.status === 'success' &&
                  discovery.models.length > 0 && (
                    <div className='space-y-3'>
                      <UpstreamModelSelection
                        models={discovery.models}
                        selected={currentModelsArray}
                        existingModels={
                          isEditing
                            ? initialModelsRef.current
                            : currentModelsArray
                        }
                        onChange={handleModelsChange}
                        showChanges={isEditing}
                        redirectModels={redirectModelList}
                        redirectSourceModels={redirectModelKeyList}
                        onRedirectModel={handleFetchedRedirect}
                        aliases={upstreamAliases}
                      />
                      <div className='flex flex-wrap items-center justify-between gap-2'>
                        <p className='text-muted-foreground text-xs'>
                          {t(
                            'Redirect fetched upstream names to the names your users call.'
                          )}
                        </p>
                        <div className='flex flex-wrap gap-2'>
                          {viewportWide && (
                            <Button
                              type='button'
                              variant='outline'
                              size='sm'
                              onClick={
                                redirectPanelOpen
                                  ? closeRedirectPanel
                                  : openRedirectPanel
                              }
                            >
                              <PanelLeftOpen aria-hidden='true' />
                              {redirectPanelOpen
                                ? t('Hide redirect panel')
                                : t('Redirect panel')}
                            </Button>
                          )}
                          <Button
                            type='button'
                            variant='outline'
                            size='sm'
                            onClick={() =>
                              setBatchMapping({
                                source: 'upstream',
                              })
                            }
                          >
                            <ArrowRightLeft aria-hidden='true' />
                            {t('Batch redirect')}
                          </Button>
                        </div>
                      </div>
                    </div>
                  )}
                {isEditing && !previewModels && (
                  <p className='text-muted-foreground text-xs'>
                    {t(
                      'Model discovery uses the saved channel connection settings.'
                    )}
                  </p>
                )}
                {!isEditing && isBatchMode && (
                  <p className='text-muted-foreground text-xs'>
                    {t(
                      'Model discovery uses the first key; other keys are not tested.'
                    )}
                  </p>
                )}
              </div>
            )}

            <Separator className='my-4' />

            <div className='space-y-3'>
              <div>
                <p className='text-sm font-medium'>{t('Quick actions')}</p>
                <p className='text-muted-foreground text-xs'>
                  {t(
                    'Use presets or upstream discovery to populate the model list faster.'
                  )}
                </p>
              </div>
              <div className='flex flex-wrap gap-2'>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  onClick={handleFillRelatedModels}
                  disabled={!basicModels.length}
                >
                  <FileText className='mr-2 h-4 w-4' aria-hidden='true' />
                  {t('Fill Related Models')}
                </Button>
                {MODEL_FETCHABLE_TYPES.has(currentType) && (
                  <>
                    <Button
                      type='button'
                      variant='outline'
                      size='sm'
                      onClick={handleFetchModels}
                      disabled={
                        !canDiscoverModels || discovery.status === 'loading'
                      }
                    >
                      <Sparkles className='mr-2 h-4 w-4' aria-hidden='true' />
                      {t('Fetch from Upstream')}
                    </Button>
                    {!canDiscoverModels && (
                      <span className='text-muted-foreground basis-full text-xs'>
                        {t('No permission to perform this action')}
                      </span>
                    )}
                  </>
                )}
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  onClick={handleCopyModels}
                  disabled={currentModelsArray.length === 0}
                >
                  <Copy className='mr-2 h-4 w-4' aria-hidden='true' />
                  {t('Copy All')}
                </Button>
                <Button
                  type='button'
                  variant='ghost'
                  size='sm'
                  onClick={handleClearModels}
                  disabled={currentModelsArray.length === 0}
                >
                  <Eraser className='mr-2 h-4 w-4' aria-hidden='true' />
                  {t('Clear All')}
                </Button>
              </div>
              {prefillGroups.length > 0 && (
                <div className='flex flex-wrap items-center gap-2'>
                  <span className='text-muted-foreground text-xs'>
                    {t('Preset groups')}:
                  </span>
                  {prefillGroups.map((group) => (
                    <Button
                      key={group.id}
                      type='button'
                      variant='secondary'
                      size='sm'
                      onClick={() => handleAddPrefillGroup(group)}
                    >
                      {group.name}
                    </Button>
                  ))}
                </div>
              )}
            </div>
          </div>

          <div className='border-border/60 rounded-lg border p-4'>
            <FormField
              control={form.control}
              name='group'
              render={({ field }) => (
                <FormItem className='space-y-3'>
                  <div className='space-y-1'>
                    <FormLabel required>{t('Groups')}</FormLabel>
                    <FormDescription>
                      {t(FIELD_DESCRIPTIONS.GROUP)}
                    </FormDescription>
                  </div>
                  <FormControl>
                    {isLoadingGroups ? (
                      <Skeleton className='h-10 w-full' />
                    ) : (
                      <MultiSelect
                        options={groupOptions}
                        selected={field.value}
                        onChange={field.onChange}
                        placeholder={t(FIELD_PLACEHOLDERS.GROUP)}
                      />
                    )}
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
          </div>
        </div>
      </ChannelModelsSection>
    </div>
  )

  const connectionSection = (
    <div className='scroll-mt-4'>
      <ChannelApiAccessSection>
        {CHANNEL_TYPE_WARNINGS[currentType] && (
          <Alert>
            <AlertDescription>
              {t(CHANNEL_TYPE_WARNINGS[currentType])}
            </AlertDescription>
          </Alert>
        )}

        {sensitiveLocked && (
          <Alert className='border-amber-200 bg-amber-50 text-amber-900 dark:border-amber-500/40 dark:bg-amber-500/10 dark:text-amber-50'>
            <AlertDescription>
              {t('No permission to perform this action')}
            </AlertDescription>
          </Alert>
        )}

        <div className='border-border/60 bg-muted/10 rounded-lg border p-4'>
          <fieldset
            disabled={sensitiveLocked}
            className='space-y-4 disabled:opacity-60'
          >
            {/* Azure (type 3) */}
            {currentType === 3 && (
              <>
                <FormField
                  control={form.control}
                  name='base_url'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel required>
                        {t('AZURE_OPENAI_ENDPOINT')}
                      </FormLabel>
                      <FormControl>
                        <Input
                          placeholder={t(
                            'e.g., https://docs-test-001.openai.azure.com'
                          )}
                          {...field}
                        />
                      </FormControl>
                      <FormDescription>
                        {t('Your Azure OpenAI endpoint URL')}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name='other'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel required>{t('Default API Version')}</FormLabel>
                      <FormControl>
                        <Input
                          placeholder={t('e.g., 2025-04-01-preview')}
                          {...field}
                        />
                      </FormControl>
                      <FormDescription>
                        {t('Default API version for this channel')}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name='azure_responses_version'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Responses API Version')}</FormLabel>
                      <FormControl>
                        <Input placeholder={t('e.g., preview')} {...field} />
                      </FormControl>
                      <FormDescription>
                        {t(
                          'Default Responses API version, if empty, will use the API version above'
                        )}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              </>
            )}

            {/* Custom (type 8) */}
            {currentType === 8 && (
              <FormField
                control={form.control}
                name='base_url'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel required>
                      {t('Full Base URL (supports')} {'{'}
                      {t('model')}
                      {'}'} {t('variable)')}
                    </FormLabel>
                    <FormControl>
                      <Input
                        placeholder={t(
                          'e.g., https://api.openai.com/v1/chat/completions'
                        )}
                        {...field}
                      />
                    </FormControl>
                    <FormDescription>
                      {t('Enter the complete URL, supports')} {'{'}
                      {t('model')}
                      {'}'} {t('variable')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            )}

            {/* Xunfei/Spark (type 18) */}
            {currentType === 18 && (
              <FormField
                control={form.control}
                name='other'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel required>{t('Model Version')}</FormLabel>
                    <FormControl>
                      <Input placeholder={t('e.g., v2.1')} {...field} />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Spark model version, e.g., v2.1 (version number in API URL)'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            )}

            {/* OpenRouter (type 20) */}
            {currentType === 20 && (
              <FormField
                control={form.control}
                name='is_enterprise_account'
                render={({ field }) => (
                  <FormItem className='flex items-center justify-between'>
                    <div className='space-y-0.5'>
                      <FormLabel>{t('Enterprise Account')}</FormLabel>
                      <FormDescription>
                        {t(
                          'Enable if this is an OpenRouter enterprise account with special response format'
                        )}
                      </FormDescription>
                    </div>
                    <FormControl>
                      <Switch
                        disabled={sensitiveLocked}
                        checked={field.value}
                        onCheckedChange={field.onChange}
                      />
                    </FormControl>
                  </FormItem>
                )}
              />
            )}

            {/* AWS (type 33) */}
            {currentType === 33 && (
              <FormField
                control={form.control}
                name='aws_key_type'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('AWS Key Format')}</FormLabel>
                    <Select
                      disabled={sensitiveLocked}
                      items={[
                        {
                          value: 'ak_sk',
                          label: t('AccessKey / SecretAccessKey'),
                        },
                        {
                          value: 'api_key',
                          label: t('API Key'),
                        },
                      ]}
                      onValueChange={field.onChange}
                      value={field.value}
                    >
                      <FormControl>
                        <SelectTrigger>
                          <SelectValue placeholder={t('Select key format')} />
                        </SelectTrigger>
                      </FormControl>
                      <SelectContent alignItemWithTrigger={false}>
                        <SelectGroup>
                          <SelectItem value='ak_sk'>
                            {t('AccessKey / SecretAccessKey')}
                          </SelectItem>
                          <SelectItem value='api_key'>
                            {t('API Key')}
                          </SelectItem>
                        </SelectGroup>
                      </SelectContent>
                    </Select>
                    <FormDescription>
                      {field.value === 'api_key'
                        ? t('API Key mode: use APIKey|Region')
                        : t('AK/SK mode: use AccessKey|SecretAccessKey|Region')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            )}

            {/* AI Proxy Library (type 21) */}
            {currentType === 21 && (
              <FormField
                control={form.control}
                name='other'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel required>{t('Knowledge Base ID')}</FormLabel>
                    <FormControl>
                      <Input placeholder={t('e.g., 123456')} {...field} />
                    </FormControl>
                    <FormDescription>
                      {t('Enter the knowledge base ID')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            )}

            {/* FastGPT (type 22) */}
            {currentType === 22 && (
              <FormField
                control={form.control}
                name='base_url'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Private Deployment URL')}</FormLabel>
                    <FormControl>
                      <Input placeholder={baseUrlPlaceholder} {...field} />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'For private deployments, format: https://fastgpt.run/api/openapi'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            )}

            {/* SunoAPI (type 36) */}
            {currentType === 36 && (
              <FormField
                control={form.control}
                name='base_url'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel required>
                      {t('API Base URL (Important: Not Chat API)')}
                    </FormLabel>
                    <FormControl>
                      <Input
                        placeholder={t(
                          'e.g., https://api.example.com (path before /suno)'
                        )}
                        {...field}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Enter the path before /suno, usually just the domain'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            )}

            {/* Cloudflare Workers AI (type 39) */}
            {currentType === 39 && (
              <FormField
                control={form.control}
                name='other'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel required>{t('Account ID')}</FormLabel>
                    <FormControl>
                      <Input
                        placeholder={t('e.g., d6b5da8hk1awo8nap34ube6gh')}
                        {...field}
                      />
                    </FormControl>
                    <FormDescription>
                      {t('Your Cloudflare Account ID')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            )}

            {/* SiliconFlow (type 40) */}
            {currentType === 40 && (
              <Alert>
                <AlertDescription>
                  {t('Referral link:')}{' '}
                  <a
                    href='https://cloud.siliconflow.cn/i/hij0YNTZ'
                    target='_blank'
                    rel='noopener noreferrer'
                    className='text-primary underline'
                  >
                    {t('https://cloud.siliconflow.cn/i/hij0YNTZ')}
                  </a>
                </AlertDescription>
              </Alert>
            )}

            {/* Vertex AI (type 41) */}
            {currentType === 41 && (
              <>
                <FormField
                  control={form.control}
                  name='vertex_key_type'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Vertex AI Key Format')}</FormLabel>
                      <Select
                        disabled={sensitiveLocked}
                        items={[
                          { value: 'json', label: t('JSON') },
                          {
                            value: 'api_key',
                            label: t('API Key'),
                          },
                        ]}
                        onValueChange={field.onChange}
                        value={field.value}
                      >
                        <FormControl>
                          <SelectTrigger>
                            <SelectValue />
                          </SelectTrigger>
                        </FormControl>
                        <SelectContent alignItemWithTrigger={false}>
                          <SelectGroup>
                            <SelectItem value='json'>{t('JSON')}</SelectItem>
                            <SelectItem value='api_key'>
                              {t('API Key')}
                            </SelectItem>
                          </SelectGroup>
                        </SelectContent>
                      </Select>
                      <FormDescription>
                        {field.value === 'json'
                          ? t('JSON format supports service account JSON files')
                          : t('API Key mode (does not support batch creation)')}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                {vertexKeyType === 'json' && (
                  <FormItem>
                    <FormLabel>{t('Service account JSON file(s)')}</FormLabel>
                    <FormControl>
                      <Input
                        type='file'
                        accept='.json,application/json'
                        multiple={isBatchMode}
                        onChange={async (e) => {
                          const fileList = e.target.files
                          const files = fileList ? [...fileList] : []
                          // allow re-selecting the same file
                          e.target.value = ''

                          if (files.length === 0) {
                            toast.info(t('Please upload key file(s)'))
                            return
                          }

                          const keys: unknown[] = []
                          for (const file of files) {
                            try {
                              const txt = await file.text()
                              keys.push(JSON.parse(txt))
                            } catch {
                              toast.error(
                                t('Failed to parse JSON file: {{name}}', {
                                  name: file.name,
                                })
                              )
                              return
                            }
                          }

                          if (keys.length === 0) {
                            toast.info(t('Please upload key file(s)'))
                            return
                          }

                          const keyValue = isBatchMode
                            ? JSON.stringify(keys)
                            : JSON.stringify(keys[0])

                          form.setValue('key', keyValue, {
                            shouldDirty: true,
                            shouldValidate: true,
                          })

                          toast.success(
                            t('Parsed {{count}} service account file(s)', {
                              count: keys.length,
                            })
                          )
                        }}
                      />
                    </FormControl>
                    <FormDescription>
                      {isBatchMode
                        ? t('Upload multiple JSON files in batch modes')
                        : t('Upload a single service account JSON file')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
                <FormField
                  control={form.control}
                  name='other'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel required>{t('Deployment Region')}</FormLabel>
                      <FormControl>
                        <Textarea
                          placeholder={t(
                            'e.g., us-central1 or JSON format for model-specific regions'
                          )}
                          rows={3}
                          {...field}
                        />
                      </FormControl>
                      <FormDescription>
                        {t('Enter deployment region or JSON mapping:')} {'{'}
                        {t(
                          '"default": "us-central1", "claude-3-5-sonnet-20240620": "europe-west1"'
                        )}
                        {'}'}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              </>
            )}

            {/* VolcEngine (type 45) */}
            {currentType === 45 && !doubaoApiEditUnlocked && (
              <FormField
                control={form.control}
                name='base_url'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel
                      required
                      className='cursor-pointer select-none'
                      onClick={handleApiConfigSecretClick}
                    >
                      {t('API Base URL')}
                    </FormLabel>
                    <Select
                      disabled={sensitiveLocked}
                      items={[
                        {
                          value: 'https://ark.cn-beijing.volces.com',
                          label: t('https://ark.cn-beijing.volces.com'),
                        },
                        {
                          value: 'https://ark.ap-southeast.bytepluses.com',
                          label: t('https://ark.ap-southeast.bytepluses.com'),
                        },
                      ]}
                      onValueChange={field.onChange}
                      value={
                        field.value === 'doubao-coding-plan'
                          ? 'https://ark.cn-beijing.volces.com'
                          : field.value || 'https://ark.cn-beijing.volces.com'
                      }
                    >
                      <FormControl>
                        <SelectTrigger>
                          <SelectValue />
                        </SelectTrigger>
                      </FormControl>
                      <SelectContent alignItemWithTrigger={false}>
                        <SelectGroup>
                          <SelectItem value='https://ark.cn-beijing.volces.com'>
                            {t('https://ark.cn-beijing.volces.com')}
                          </SelectItem>
                          <SelectItem value='https://ark.ap-southeast.bytepluses.com'>
                            {t('https://ark.ap-southeast.bytepluses.com')}
                          </SelectItem>
                        </SelectGroup>
                      </SelectContent>
                    </Select>
                    <FormDescription>
                      {t('Select the API endpoint region')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            )}

            {/* VolcEngine (type 45) - Custom API URL (unlocked) */}
            {currentType === 45 && doubaoApiEditUnlocked && (
              <FormField
                control={form.control}
                name='base_url'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel required>{t('API Base URL')}</FormLabel>
                    <FormControl>
                      <Input placeholder={baseUrlPlaceholder} {...field} />
                    </FormControl>
                    <FormDescription>
                      {t('Enter custom API endpoint URL')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            )}

            {/* Coze (type 49) */}
            {currentType === 49 && (
              <FormField
                control={form.control}
                name='other'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel required>{t('Agent ID')}</FormLabel>
                    <FormControl>
                      <Input
                        placeholder={t('e.g., 7342866812345')}
                        {...field}
                      />
                    </FormControl>
                    <FormDescription>
                      {t('Enter the Coze agent ID')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            )}

            {/* General base_url for other types */}
            {![3, 8, 22, 36, 45].includes(currentType) && (
              <FormField
                control={form.control}
                name='base_url'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel
                      required={
                        currentType === CHANNEL_TYPE_TASK_PLUGIN ||
                        currentType === CHANNEL_TYPE_VLLM ||
                        currentType === CHANNEL_TYPE_SGLANG
                      }
                    >
                      {t('Base URL')}
                    </FormLabel>
                    <FormControl>
                      <Input placeholder={baseUrlPlaceholder} {...field} />
                    </FormControl>
                    {currentType !== CHANNEL_TYPE_TASK_PLUGIN && (
                      <FormDescription>
                        {t(
                          'Custom API base URL. For official channels, New API has built-in addresses. Only fill this for third-party proxy sites or special endpoints. Do not add /v1 or trailing slash.'
                        )}
                      </FormDescription>
                    )}
                    {currentType === CHANNEL_TYPE_TASK_PLUGIN &&
                      !boundTaskPlugin?.baseUrl && (
                        <FormDescription>
                          {t(
                            'The upstream address this plugin sends requests to. The plugin declares no default, so it must be filled in.'
                          )}
                        </FormDescription>
                      )}
                    {currentType === CHANNEL_TYPE_TASK_PLUGIN &&
                      boundTaskPlugin?.baseUrl && (
                        <FormDescription className='flex flex-wrap items-center gap-x-1'>
                          <span>{t('Plugin default')}:</span>
                          <span className='font-mono break-all'>
                            {boundTaskPlugin.baseUrl}
                          </span>
                          {(field.value ?? '').trim().replace(/\/+$/, '') !==
                            boundTaskPlugin.baseUrl && (
                            <Button
                              type='button'
                              variant='link'
                              size='xs'
                              className='h-auto p-0'
                              onClick={() =>
                                form.setValue(
                                  'base_url',
                                  boundTaskPlugin.baseUrl ?? '',
                                  {
                                    shouldDirty: true,
                                    shouldValidate: true,
                                  }
                                )
                              }
                            >
                              {t('Use default')}
                            </Button>
                          )}
                        </FormDescription>
                      )}
                    <FormMessage />
                    {(taskPluginBaseUrlTrust?.plainHttp ||
                      taskPluginBaseUrlTrust?.privateHost) && (
                      <Alert>
                        <AlertCircle />
                        <AlertDescription>
                          {taskPluginBaseUrlTrust?.plainHttp &&
                            t(
                              'This base URL uses plain HTTP, so the channel key is sent unencrypted.'
                            )}
                          {taskPluginBaseUrlTrust?.plainHttp &&
                            taskPluginBaseUrlTrust?.privateHost &&
                            ' '}
                          {taskPluginBaseUrlTrust?.privateHost &&
                            t(
                              'This base URL points at a private or local network host. Make sure it is an upstream you control.'
                            )}
                        </AlertDescription>
                      </Alert>
                    )}
                  </FormItem>
                )}
              />
            )}

            {currentType === CHANNEL_TYPE_ADVANCED_CUSTOM && (
              <FormField
                control={form.control}
                name='advanced_custom'
                render={({ field }) => (
                  <FormItem className='space-y-3 border-y py-4'>
                    <div className='flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between'>
                      <div className='space-y-2'>
                        <FormLabel>{t('Advanced Custom Routes')}</FormLabel>
                        <div className='flex flex-wrap gap-2'>
                          <Badge variant='secondary'>
                            {t('Routes')}: {advancedCustomStats.routeCount}
                          </Badge>
                          {advancedCustomRouteTypeLabels.map((label) => (
                            <Badge
                              key={label}
                              variant='outline'
                              className='max-w-[12rem]'
                              title={label}
                            >
                              <span className='truncate'>{label}</span>
                            </Badge>
                          ))}
                          {hiddenAdvancedCustomRouteTypeCount > 0 && (
                            <Badge
                              variant='outline'
                              title={advancedCustomRouteTypeTitle}
                            >
                              +{hiddenAdvancedCustomRouteTypeCount}
                            </Badge>
                          )}
                          {!advancedCustomStats.valid && (
                            <Badge variant='destructive'>
                              {t('Incomplete')}
                            </Badge>
                          )}
                        </div>
                      </div>
                      <Button
                        type='button'
                        variant='outline'
                        size='sm'
                        onClick={() => setAdvancedCustomEditorOpen(true)}
                      >
                        <Route className='mr-2 h-4 w-4' />
                        {t('Configure routes')}
                      </Button>
                    </div>
                    <FormControl>
                      <input type='hidden' {...field} />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            )}

            <ChannelAuthSection>
              {!isEditing && (
                <FormField
                  control={form.control}
                  name='multi_key_mode'
                  render={({ field }) => (
                    <FormItem className='flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between'>
                      <FormLabel className='text-muted-foreground text-xs font-medium'>
                        {t('Add Mode')}
                      </FormLabel>
                      <Select
                        disabled={sensitiveLocked}
                        items={addModeOptions.map((option) => ({
                          value: option.value,
                          label: t(option.label),
                        }))}
                        onValueChange={field.onChange}
                        value={field.value}
                      >
                        <FormControl>
                          <SelectTrigger size='sm' className='w-full sm:w-56'>
                            <SelectValue />
                          </SelectTrigger>
                        </FormControl>
                        <SelectContent alignItemWithTrigger={false}>
                          <SelectGroup>
                            {addModeOptions.map((option) => (
                              <SelectItem
                                key={option.value}
                                value={option.value}
                              >
                                {t(option.label)}
                              </SelectItem>
                            ))}
                          </SelectGroup>
                        </SelectContent>
                      </Select>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              )}

              <FormField
                control={form.control}
                name='key'
                render={({ field }) => {
                  let keyPlaceholder = t(getKeyPromptForType(currentType))
                  if (isEditing) {
                    keyPlaceholder = t('Leave empty to keep existing key')
                  } else if (
                    currentType === 33 &&
                    awsKeyType === 'api_key' &&
                    isBatchMode
                  ) {
                    keyPlaceholder = t(
                      'Enter API Key, one per line, format: APIKey|Region'
                    )
                  } else if (currentType === 33 && awsKeyType === 'api_key') {
                    keyPlaceholder = t('Enter API Key, format: APIKey|Region')
                  } else if (currentType === 33 && isBatchMode) {
                    keyPlaceholder = t(
                      'Enter key, one per line, format: AccessKey|SecretAccessKey|Region'
                    )
                  } else if (currentType === 33) {
                    keyPlaceholder = t(
                      'Enter key, format: AccessKey|SecretAccessKey|Region'
                    )
                  } else if (isBatchMode) {
                    keyPlaceholder = t(
                      'Enter one key per line for batch creation'
                    )
                  }

                  let keyDescription: ReactNode = t(FIELD_DESCRIPTIONS.KEY)
                  if (isEditing) {
                    let keyModeDescription = t(
                      'Append mode: New keys will be added to the end of the existing key list'
                    )
                    if (keyMode === 'replace') {
                      keyModeDescription = t(
                        'Replace mode: Will completely replace all existing keys'
                      )
                    }
                    keyDescription = (
                      <>
                        {t(
                          'Enter new key to update, or leave empty to keep current key'
                        )}
                        {isMultiKeyChannel && (
                          <span className='text-warning mt-1 block'>
                            {keyModeDescription}
                          </span>
                        )}
                      </>
                    )
                  } else if (isBatchMode) {
                    keyDescription = t(
                      'Enter one API key per line for batch creation'
                    )
                  }
                  return (
                    <FormItem>
                      <FormLabel required>{t('API Key')}</FormLabel>
                      <FormControl>
                        <Textarea
                          placeholder={keyPlaceholder}
                          rows={isBatchMode ? 8 : 4}
                          {...field}
                        />
                      </FormControl>
                      <FormDescription>
                        <span className='flex flex-col gap-2'>
                          <span>{keyDescription}</span>
                          {!isEditing && isBatchMode && (
                            <Button
                              type='button'
                              variant='outline'
                              size='sm'
                              onClick={handleDeduplicateKeys}
                              className='w-fit'
                            >
                              <Trash2 className='mr-2 h-4 w-4' />
                              {t('Remove Duplicates')}
                            </Button>
                          )}
                        </span>
                      </FormDescription>
                      {isEditing && canRevealChannelKey && (
                        <div className='border-border/60 mt-4 flex flex-col gap-3 border-y border-dashed py-4'>
                          <div className='flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between'>
                            <div>
                              <p className='text-sm font-medium'>
                                {t('Current key')}
                              </p>
                              <p className='text-muted-foreground text-xs'>
                                {t(
                                  'Verification required to reveal the saved key.'
                                )}
                              </p>
                            </div>
                            <div className='flex items-center gap-2'>
                              <Button
                                type='button'
                                variant='outline'
                                size='sm'
                                onClick={handleRevealKey}
                                disabled={
                                  isChannelKeyLoading || verification.isActive
                                }
                              >
                                {isChannelKeyLoading ||
                                verification.isActive ? (
                                  <Loader2 className='mr-2 h-4 w-4 animate-spin' />
                                ) : (
                                  <Eye className='mr-2 h-4 w-4' />
                                )}
                                {t('Reveal key')}
                              </Button>
                              <Button
                                type='button'
                                variant='ghost'
                                size='sm'
                                onClick={async () => {
                                  if (channelKey) {
                                    await copyToClipboard(channelKey)
                                  }
                                }}
                                disabled={!channelKey}
                              >
                                <Copy className='mr-2 h-4 w-4' />
                                {t('Copy')}
                              </Button>
                            </div>
                          </div>
                          <Input
                            readOnly
                            value={channelKey ?? ''}
                            placeholder={t('Hidden — verify to reveal')}
                            className='font-mono'
                          />
                        </div>
                      )}
                      <FormMessage />
                    </FormItem>
                  )
                }}
              />

              {currentType === 57 && (
                <div className='border-border/60 flex flex-col gap-3 border-y py-4'>
                  <div className='flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between'>
                    <div className='text-muted-foreground text-xs'>
                      {t(
                        'Codex channels use an OAuth JSON credential as the key.'
                      )}
                    </div>
                    <div className='flex flex-wrap items-center gap-2'>
                      {isEditing && channelId && (
                        <Button
                          type='button'
                          variant='outline'
                          size='sm'
                          onClick={handleRefreshCodexCredential}
                          disabled={
                            sensitiveLocked || isCodexCredentialRefreshing
                          }
                        >
                          {isCodexCredentialRefreshing ? (
                            <Loader2 className='mr-2 h-4 w-4 animate-spin' />
                          ) : (
                            <RefreshCw className='mr-2 h-4 w-4' />
                          )}
                          {isCodexCredentialRefreshing
                            ? t('Refreshing...')
                            : t('Refresh credential')}
                        </Button>
                      )}
                    </div>
                  </div>
                  <Alert className='border-amber-200 bg-amber-50 text-amber-900 dark:border-amber-500/40 dark:bg-amber-500/10 dark:text-amber-50'>
                    <AlertDescription>
                      {t(
                        "Disclaimer: Personal use only. Do not distribute or share any credentials. This channel has prerequisites and requires prior setup; use it only if you understand the flow and risks, and comply with OpenAI's terms and policies. Credentials and configuration are for Codex CLI integration only, and are not intended for any other client, platform, or channel."
                      )}
                    </AlertDescription>
                  </Alert>
                </div>
              )}

              {isEditing && isMultiKeyChannel && (
                <FormField
                  control={form.control}
                  name='key_mode'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Key Update Mode')}</FormLabel>
                      <Select
                        disabled={sensitiveLocked}
                        items={[
                          {
                            value: 'append',
                            label: t('Append to existing keys'),
                          },
                          {
                            value: 'replace',
                            label: t('Replace all existing keys'),
                          },
                        ]}
                        onValueChange={field.onChange}
                        value={field.value}
                      >
                        <FormControl>
                          <SelectTrigger>
                            <SelectValue />
                          </SelectTrigger>
                        </FormControl>
                        <SelectContent alignItemWithTrigger={false}>
                          <SelectGroup>
                            <SelectItem value='append'>
                              {t('Append to existing keys')}
                            </SelectItem>
                            <SelectItem value='replace'>
                              {t('Replace all existing keys')}
                            </SelectItem>
                          </SelectGroup>
                        </SelectContent>
                      </Select>
                      <FormDescription>
                        {field.value === 'replace'
                          ? t(
                              'Replace mode: Will completely replace all existing keys'
                            )
                          : t(
                              'Append mode: New keys will be added to the end of the existing key list'
                            )}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              )}

              {(isMultiKeyChannel ||
                (!isEditing && multiKeyMode === 'multi_to_single')) && (
                <FormField
                  control={form.control}
                  name='multi_key_type'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Multi-Key Strategy')}</FormLabel>
                      <Select
                        disabled={sensitiveLocked}
                        items={[
                          {
                            value: 'random',
                            label: t('Random'),
                          },
                          {
                            value: 'polling',
                            label: t('Polling'),
                          },
                        ]}
                        onValueChange={field.onChange}
                        value={field.value}
                      >
                        <FormControl>
                          <SelectTrigger>
                            <SelectValue />
                          </SelectTrigger>
                        </FormControl>
                        <SelectContent alignItemWithTrigger={false}>
                          <SelectGroup>
                            <SelectItem value='random'>
                              {t('Random')}
                            </SelectItem>
                            <SelectItem value='polling'>
                              {t('Polling')}
                            </SelectItem>
                          </SelectGroup>
                        </SelectContent>
                      </Select>
                      <FormDescription>
                        {multiKeyType === 'polling' ? (
                          <span className='text-warning'>
                            {t(
                              'Polling mode requires Redis and memory cache, otherwise performance will be significantly degraded'
                            )}
                          </span>
                        ) : (
                          t(
                            'Randomly select a key from the pool for each request'
                          )
                        )}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              )}
            </ChannelAuthSection>
          </fieldset>
        </div>
      </ChannelApiAccessSection>
    </div>
  )

  let formContent: ReactNode
  if (!isEditing && !providerTarget) {
    formContent = null
  } else if (isEditing && isChannelError && !channelData?.data) {
    formContent = (
      <ErrorState
        title={t('Failed to load channel')}
        description={getServerErrorMessage(
          channelError,
          t('Failed to load channel')
        )}
        onRetry={() => {
          void refetchChannel()
        }}
      />
    )
  } else if (isChannelDetailLoading) {
    formContent = <ChannelEditorLoadingState />
  } else {
    formContent = (
      <ChannelConfiguration
        section={configurationSection}
        onSectionChange={setConfigurationSection}
        statuses={configuration.sections}
        badges={{
          routing: {
            count: mappingCount,
            label: t('{{count}} model mapping(s)', { count: mappingCount }),
          },
        }}
        connection={
          <>
            {basicSection}
            {connectionSection}
          </>
        }
        models={modelsSection}
        quickOptions={
          viewportWide ? null : (
            <ChannelQuickOptions
              channelType={currentType}
              sensitiveLocked={sensitiveLocked}
              disabled={isSubmitting}
              confirmEnablePassthrough={confirmEnablePassthrough}
            />
          )
        }
        routing={
          <>
            {redirectPanelActive ? redirectPanelNotice : modelMappingFields}
            {routingFields}
          </>
        }
        request={
          <>
            {overrideFields}
            <div
              role='group'
              aria-label={t('Request processing')}
              className={channelConfigurationBlockClassName(
                configuration.blocks.requestProcessing,
                'space-y-4'
              )}
            >
              <CardHeading
                status={configuration.blocks.requestProcessing}
                title={t('Request processing')}
                icon={<Settings className='size-4' />}
              />
              <fieldset
                disabled={sensitiveLocked}
                className='space-y-4 disabled:opacity-60'
              >
                <ResponsesWebSocketSetting
                  channelType={currentType}
                  disabled={sensitiveLocked || isSubmitting}
                />
                {formatFields}
                {ollamaOpenAIChatFields}
                {thinkingFields}
                {currentType !== CHANNEL_TYPE_ADVANCED_CUSTOM &&
                  passthroughFields}
                {systemPromptFields}
                {systemPromptOverrideFields}
              </fieldset>
            </div>
            {fieldPassthroughFields}
          </>
        }
        other={
          <>
            <div
              role='group'
              aria-label={t('Channel Extra Settings')}
              className={channelConfigurationBlockClassName(
                configuration.blocks.extraSettings,
                'space-y-4'
              )}
            >
              <CardHeading
                status={configuration.blocks.extraSettings}
                title={t('Channel Extra Settings')}
                icon={<Settings className='size-4' />}
              />
              <fieldset
                disabled={sensitiveLocked}
                className='space-y-4 disabled:opacity-60'
              >
                {taskPollingFields}
                {proxyFields}
                {httpProtocolFields}
                {httpShardsFields}
              </fieldset>
            </div>
            {upstreamModelDetectionFields}
            {notesFields}
          </>
        }
      />
    )
  }
  let description = t(
    'Configure the connection and models, then create the channel.'
  )
  if (showProviderPicker && providerTarget) {
    description = `${t('Current:')} ${providerLabel}`
  } else if (isEditing) {
    description = t(
      "Update channel configuration and click save when you're done."
    )
  } else if (showProviderPicker) {
    description = t('Choose a provider or plugin to configure your channel.')
  }

  return (
    <>
      <Sheet open={open} onOpenChange={handleOpenChange}>
        <SheetContent
          side={drawerSide}
          className={sideDrawerContentClassName('sm:max-w-7xl')}
        >
          <SheetHeader className={sideDrawerHeaderClassName('pr-12 sm:pr-14')}>
            <div className='flex flex-col gap-2'>
              <div className='flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between'>
                <div className='flex min-w-0 items-center gap-2 sm:gap-3'>
                  <SheetTitle className='flex shrink-0 items-center gap-2 sm:gap-3'>
                    <IconBadge tone='info' size='title'>
                      <Server className='size-5' />
                    </IconBadge>
                    <span>
                      {isEditing ? t('Edit Channel') : t('Create Channel')}
                    </span>
                  </SheetTitle>
                  {(!showProviderPicker || providerTarget) && (
                    <Button
                      ref={providerControlRef}
                      type='button'
                      variant='outline'
                      aria-label={
                        showProviderPicker
                          ? t('Back to configuration')
                          : t('Change provider')
                      }
                      aria-description={providerLabel}
                      title={providerLabel}
                      className='min-w-0 shrink gap-2 sm:max-w-md'
                      disabled={
                        isSubmitting ||
                        (!showProviderPicker &&
                          (!canEditSensitive ||
                            (isEditing && !channelData?.data)))
                      }
                      onClick={() => setChoosingProvider(!showProviderPicker)}
                    >
                      {showProviderPicker ? (
                        <>
                          <ArrowLeft className='size-4' aria-hidden='true' />
                          <span className='shrink-0 sm:hidden'>
                            {t('Back')}
                          </span>
                          <span className='hidden shrink-0 sm:inline'>
                            {t('Back to configuration')}
                          </span>
                        </>
                      ) : (
                        <ChannelTypeLogo
                          type={currentType}
                          plugin={boundTaskPlugin}
                          size={18}
                        />
                      )}
                      <span className='min-w-0 truncate'>{providerLabel}</span>
                      {!showProviderPicker && (
                        <>
                          <span className='hidden shrink-0 sm:inline'>
                            {t('Change provider')}
                          </span>
                          <ChevronDown className='size-4' aria-hidden='true' />
                        </>
                      )}
                    </Button>
                  )}
                </div>
                {!isEditing && !showProviderPicker && (
                  <Button
                    type='button'
                    variant='outline'
                    size='sm'
                    className='shrink-0'
                    onClick={pasteConnectionInfoFromClipboard}
                  >
                    <ClipboardPaste className='size-4' />
                    <span>{t('Paste Connection Info')}</span>
                  </Button>
                )}
              </div>
              <div className='flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between'>
                <div className='flex min-w-0 flex-wrap items-center gap-2 sm:flex-1'>
                  {isEditing && channelData?.data && (
                    <Badge variant='secondary' className='shrink-0'>
                      {t(
                        CHANNEL_STATUS_LABELS[
                          currentStatus as keyof typeof CHANNEL_STATUS_LABELS
                        ] || 'Unknown'
                      )}
                    </Badge>
                  )}
                  <SheetDescription
                    className={cn(
                      showProviderPicker && providerTarget && 'truncate'
                    )}
                    title={
                      showProviderPicker && providerTarget
                        ? description
                        : undefined
                    }
                  >
                    {description}
                  </SheetDescription>
                </div>
                {viewportWide &&
                  !showProviderPicker &&
                  !channelError &&
                  !isChannelDetailLoading && (
                    <ChannelQuickOptions
                      form={form}
                      layout='inline'
                      channelType={currentType}
                      sensitiveLocked={sensitiveLocked}
                      disabled={isSubmitting}
                      className='sm:justify-end'
                      confirmEnablePassthrough={confirmEnablePassthrough}
                    />
                  )}
              </div>
            </div>
          </SheetHeader>

          {showProviderPicker && (
            <ChannelProviderPicker
              isCreating={!isEditing}
              plugins={taskPluginOptionsQuery.data ?? []}
              currentProvider={providerTarget}
              canBindPlugin={canBindTaskPlugin}
              loading={taskPluginOptionsQuery.isLoading}
              failed={taskPluginOptionsQuery.isError}
              disabled={isSubmitting || !canEditSensitive}
              onRetry={() => {
                void taskPluginOptionsQuery.refetch()
              }}
              onSelect={selectProvider}
            />
          )}

          {sensitiveLocked && (
            <Alert className='border-amber-200 bg-amber-50 text-amber-900 dark:border-amber-500/40 dark:bg-amber-500/10 dark:text-amber-50'>
              <AlertDescription>
                {t(
                  'Sensitive channel settings are read-only for your account.'
                )}{' '}
                {t(
                  'You can still edit non-sensitive operations fields such as models, groups, priority, and weight.'
                )}
              </AlertDescription>
            </Alert>
          )}

          {!isEditing && !showProviderPicker && clipboardConnectionInfo && (
            <Alert>
              <AlertDescription className='flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between'>
                <span>{t('Connection info detected in clipboard')}</span>
                <span className='flex shrink-0 gap-2'>
                  <Button
                    type='button'
                    size='sm'
                    onClick={() => applyConnectionInfo(clipboardConnectionInfo)}
                  >
                    {t('Fill in')}
                  </Button>
                  <Button
                    type='button'
                    variant='ghost'
                    size='sm'
                    onClick={() => setClipboardConnectionInfo(null)}
                  >
                    {t('Ignore')}
                  </Button>
                </span>
              </AlertDescription>
            </Alert>
          )}

          <Form {...form}>
            <form
              id='channel-form'
              ref={channelFormRef}
              onSubmit={form.handleSubmit(onSubmit, onInvalid)}
              className={sideDrawerFormClassName(
                cn(
                  'gap-5',
                  (!isEditing ||
                    (!isChannelDetailLoading && channelData?.data)) &&
                    'overflow-hidden',
                  showProviderPicker && 'hidden'
                )
              )}
              hidden={showProviderPicker}
            >
              {formContent}
            </form>
          </Form>

          <SheetFooter className={sideDrawerFooterClassName()}>
            {showProviderPicker && providerTarget ? (
              <Button
                type='button'
                variant='outline'
                disabled={isSubmitting}
                onClick={() => setChoosingProvider(false)}
              >
                {t('Cancel')}
              </Button>
            ) : (
              <SheetClose
                render={<Button variant='outline' disabled={isSubmitting} />}
              >
                {t('Cancel')}
              </SheetClose>
            )}
            {!showProviderPicker && (
              <Button
                form='channel-form'
                type='submit'
                disabled={
                  isSubmitting ||
                  (!isEditing && !canEditSensitive) ||
                  (isEditing && !channelData?.data)
                }
              >
                {isSubmitting && (
                  <Loader2 className='mr-2 h-4 w-4 animate-spin' />
                )}
                {isEditing ? t('Update Channel') : t('Create Channel')}
              </Button>
            )}
          </SheetFooter>
          {redirectPanelActive && redirectPanelPlacement && (
            <ModelRedirectPanel
              defaultPosition={{
                x: redirectPanelPlacement.x,
                y: redirectPanelPlacement.y,
              }}
              width={redirectPanelPlacement.width}
              onClose={closeRedirectPanel}
              mappingCount={mappingCount}
              mappingValue={formValues.model_mapping || ''}
              onMappingChange={handlePanelMappingChange}
              onMappingCommit={handlePanelMappingCommit}
              onBatchAdd={() =>
                setBatchMapping({
                  source: batchMappingSource,
                })
              }
              syncModels={redirectSyncModels}
              onSyncModelsChange={setRedirectSyncModels}
              sourceModelOptions={modelOptions.map((option) => option.value)}
              targetModelOptions={[
                ...new Set([
                  ...upstreamModelList,
                  ...modelOptions.map((option) => option.value),
                ]),
              ]}
              draftRequest={mappingDraftRequest}
              onDraftRequestHandled={() => setMappingDraftRequest(null)}
              disabled={isSubmitting}
            />
          )}
        </SheetContent>
      </Sheet>

      {open && modelConfiguration && (
        <ConfigureModelsDialog
          open
          models={currentModelsArray}
          plugins={pluginExtensions}
          initialPluginKey={modelConfiguration.pluginKey}
          onOpenChange={(nextOpen) => {
            if (!nextOpen) setModelConfiguration(null)
          }}
          onApply={handleModelsChange}
          onRedirect={(model) => requestMappingDraft(model, '')}
        />
      )}

      {open && batchMapping && (
        <ModelMappingBatchDialog
          open
          upstreamModels={upstreamModelList}
          channelModels={currentModelsArray}
          initialSource={batchMapping.source}
          onOpenChange={(nextOpen) => {
            if (!nextOpen) setBatchMapping(null)
          }}
          onApply={handleBatchMappingApply}
        />
      )}

      {paramOverrideEditorOpen && !sensitiveLocked && (
        <ParamOverrideEditorDialog
          open={paramOverrideEditorOpen}
          value={formValues.param_override || ''}
          onOpenChange={setParamOverrideEditorOpen}
          onSave={(nextValue) => {
            form.setValue('param_override', nextValue, {
              shouldDirty: true,
              shouldValidate: true,
            })
          }}
        />
      )}

      {advancedCustomEditorOpen && !sensitiveLocked && (
        <AdvancedCustomEditorDialog
          open={advancedCustomEditorOpen}
          value={formValues.advanced_custom || ''}
          onOpenChange={setAdvancedCustomEditorOpen}
          onSave={(nextValue) => {
            form.setValue('advanced_custom', nextValue, {
              shouldDirty: true,
              shouldValidate: true,
            })
          }}
        />
      )}

      <SecureVerificationDialog {...verification.dialogProps} />

      {/* Missing Models Confirmation Dialog */}
      <MissingModelsConfirmationDialog
        open={missingModelsDialogOpen}
        missingModels={missingModelsList}
        onConfirm={handleMissingModelsAction}
        onOpenChange={setMissingModelsDialogOpen}
      />

      <StatusCodeRiskDialog
        open={statusCodeRiskOpen}
        onOpenChange={(v) => {
          if (!v) handleStatusCodeRiskAction(false)
        }}
        detailItems={statusCodeRiskDetailItems}
        onConfirm={() => handleStatusCodeRiskAction(true)}
      />
      <PassthroughWarningDialog
        open={passthroughWarningKind !== null}
        kind={passthroughWarningKind}
        onOpenChange={(v) => {
          if (!v) handlePassthroughWarningAction(false)
        }}
        handleConfirm={() => handlePassthroughWarningAction(true)}
      />
    </>
  )
}
