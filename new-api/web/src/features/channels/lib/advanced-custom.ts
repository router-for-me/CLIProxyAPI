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
import type {
  AdvancedCustomAuthType,
  AdvancedCustomConfig,
  AdvancedCustomConverter,
  AdvancedCustomRoute,
  AdvancedCustomRouteAuth,
} from '../types'

export const CHANNEL_TYPE_ADVANCED_CUSTOM = 58
export const ADVANCED_CUSTOM_MODEL_LIST_PATH = '/v1/models'
export const ADVANCED_CUSTOM_MODEL_LIST_LABEL = 'OpenAI Models'
export const ADVANCED_CUSTOM_BALANCE_PATH =
  '/v1/dashboard/billing/credit_grants'
export const ADVANCED_CUSTOM_BALANCE_LABEL = 'Balance Query'

export const ADVANCED_CUSTOM_CONVERTER_OPTIONS: Array<{
  value: AdvancedCustomConverter
  label: string
  triggerLabel: string
}> = [
  {
    value: 'none',
    label: 'Native forwarding',
    triggerLabel: 'Native forwarding',
  },
  {
    value: 'anthropic_messages_to_openai_chat_completions',
    label: 'Anthropic Messages to OpenAI Chat',
    triggerLabel: 'To OpenAI Chat',
  },
  {
    value: 'openai_chat_completions_to_anthropic_messages',
    label: 'OpenAI Chat to Anthropic Messages',
    triggerLabel: 'To Anthropic Messages',
  },
  {
    value: 'openai_chat_completions_to_openai_responses',
    label: 'OpenAI Chat to OpenAI Responses',
    triggerLabel: 'To OpenAI Responses',
  },
  {
    value: 'openai_responses_to_openai_chat_completions',
    label: 'OpenAI Responses to OpenAI Chat',
    triggerLabel: 'To OpenAI Chat',
  },
  {
    value: 'openai_responses_to_gemini_generate_content',
    label: 'OpenAI Responses to Gemini Generate Content',
    triggerLabel: 'To Gemini Generate Content',
  },
  {
    value: 'gemini_generate_content_to_openai_chat_completions',
    label: 'Gemini Generate Content to OpenAI Chat',
    triggerLabel: 'To OpenAI Chat',
  },
  {
    value: 'openai_chat_completions_to_gemini_generate_content',
    label: 'OpenAI Chat to Gemini Generate Content',
    triggerLabel: 'To Gemini Generate Content',
  },
]

export type AdvancedCustomAuthMode = 'default' | AdvancedCustomAuthType

export const ADVANCED_CUSTOM_AUTH_MODE_OPTIONS: Array<{
  value: AdvancedCustomAuthMode
  label: string
}> = [
  { value: 'default', label: 'Default Bearer' },
  { value: 'none', label: 'No Auth' },
  { value: 'header', label: 'Header' },
  { value: 'query', label: 'Query' },
]

export type AdvancedCustomIncomingPathOption = {
  value: string
  /** Official API route name. Render verbatim instead of passing it to i18n. */
  label: string
}

export const ADVANCED_CUSTOM_INCOMING_PATH_OPTIONS: AdvancedCustomIncomingPathOption[] =
  [
    {
      value: '/v1/chat/completions',
      label: 'OpenAI Chat',
    },
    {
      value: '/v1/responses',
      label: 'OpenAI Responses',
    },
    {
      value: '/v1/responses/compact',
      label: 'OpenAI Responses Compact',
    },
    {
      value: '/v1/alpha/search',
      label: 'Codex Alpha Search',
    },
    {
      value: '/v1/embeddings',
      label: 'OpenAI Embeddings',
    },
    {
      value: '/v1/images/generations',
      label: 'OpenAI Image Generations',
    },
    {
      value: '/v1/images/edits',
      label: 'OpenAI Image Edits',
    },
    {
      value: '/v1/completions',
      label: 'OpenAI Completions',
    },
    {
      value: '/v1/audio/speech',
      label: 'OpenAI Audio Speech',
    },
    {
      value: '/v1/audio/transcriptions',
      label: 'OpenAI Audio Transcriptions',
    },
    {
      value: '/v1/audio/translations',
      label: 'OpenAI Audio Translations',
    },
    {
      value: '/v1/rerank',
      label: 'Rerank',
    },
    {
      value: '/v1/realtime',
      label: 'OpenAI Realtime',
    },
    {
      value: '/v1/messages',
      label: 'Claude Messages',
    },
    {
      value: '/v1beta/models/{model}:generateContent',
      label: 'Gemini Generate Content',
    },
    {
      value: '/v1beta/models/{model}:embedContent',
      label: 'Gemini Embed Content',
    },
    {
      value: '/v1beta/models/{model}:batchEmbedContents',
      label: 'Gemini Batch Embed Contents',
    },
  ]

const ADVANCED_CUSTOM_ROUTE_SUMMARY_LABELS: Record<string, string> = {
  '/v1/chat/completions': 'OpenAI Chat',
  [ADVANCED_CUSTOM_MODEL_LIST_PATH]: ADVANCED_CUSTOM_MODEL_LIST_LABEL,
  [ADVANCED_CUSTOM_BALANCE_PATH]: ADVANCED_CUSTOM_BALANCE_LABEL,
}

export type AdvancedCustomValidationError = {
  message: string
  routeIndex?: number
}

export type AdvancedCustomTemplateOption = {
  value: string
  label: string
  config: AdvancedCustomConfig
}

export type AdvancedCustomConverterDefaults = {
  upstream_path: string
  auth?: AdvancedCustomRouteAuth
}

export const ADVANCED_CUSTOM_MODEL_REGEX_PREFIX = 're:'

export type AdvancedCustomModelRuleKind = 'exact' | 'regex'

const openAIChatPath = '/v1/chat/completions'
const openAIResponsesPath = '/v1/responses'
const claudeMessagesPath = '/v1/messages'
const geminiGenerateContentPath = '/v1beta/models/{model}:generateContent'

const bearerHeaderAuth = (): AdvancedCustomRouteAuth => ({
  type: 'header',
  name: 'Authorization',
  value: 'Bearer {api_key}',
})

const apiKeyHeaderAuth = (): AdvancedCustomRouteAuth => ({
  type: 'header',
  name: 'x-api-key',
  value: '{api_key}',
})

const geminiQueryAuth = (): AdvancedCustomRouteAuth => ({
  type: 'query',
  name: 'key',
  value: '{api_key}',
})

function createOpenAINativeRoutes(): AdvancedCustomRoute[] {
  return [
    '/v1/chat/completions',
    '/v1/completions',
    '/v1/responses',
    '/v1/responses/compact',
    '/v1/embeddings',
    '/v1/images/generations',
    '/v1/images/edits',
    '/v1/audio/speech',
    '/v1/audio/transcriptions',
    '/v1/audio/translations',
    '/v1/realtime',
  ].map((path) => ({
    incoming_path: path,
    upstream_path: path,
    converter: 'none',
    auth: bearerHeaderAuth(),
  }))
}

function createClaudeNativeRoutes(): AdvancedCustomRoute[] {
  return [
    {
      incoming_path: '/v1/messages',
      upstream_path: '/v1/messages',
      converter: 'none',
      auth: apiKeyHeaderAuth(),
    },
  ]
}

function createGeminiNativeRoutes(): AdvancedCustomRoute[] {
  return [
    '/v1beta/models/{model}:generateContent',
    '/v1beta/models/{model}:embedContent',
    '/v1beta/models/{model}:batchEmbedContents',
  ].map((path) => ({
    incoming_path: path,
    upstream_path: path,
    converter: 'none',
    auth: geminiQueryAuth(),
  }))
}

function createGatewayNativeRoutes(): AdvancedCustomRoute[] {
  return ['/v1/alpha/search', '/v1/rerank'].map((path) => ({
    incoming_path: path,
    upstream_path: path,
    converter: 'none',
    auth: bearerHeaderAuth(),
  }))
}

export const ADVANCED_CUSTOM_TEMPLATE_OPTIONS: AdvancedCustomTemplateOption[] =
  [
    {
      value: 'all_protocols',
      label: 'All routes',
      config: {
        advanced_routes: [
          ...createOpenAINativeRoutes(),
          ...createClaudeNativeRoutes(),
          ...createGeminiNativeRoutes(),
          ...createGatewayNativeRoutes(),
        ],
      },
    },
    {
      value: 'openai_only',
      label: 'OpenAI only',
      config: {
        advanced_routes: createOpenAINativeRoutes(),
      },
    },
    {
      value: 'claude_only',
      label: 'Claude only',
      config: {
        advanced_routes: createClaudeNativeRoutes(),
      },
    },
    {
      value: 'gemini_only',
      label: 'Gemini only',
      config: {
        advanced_routes: createGeminiNativeRoutes(),
      },
    },
  ]

export function cloneAdvancedCustomConfig(
  config: AdvancedCustomConfig
): AdvancedCustomConfig {
  return structuredClone(config)
}

export function getAdvancedCustomTemplateConfig(
  templateKey: string
): AdvancedCustomConfig {
  const template =
    ADVANCED_CUSTOM_TEMPLATE_OPTIONS.find(
      (option) => option.value === templateKey
    ) || ADVANCED_CUSTOM_TEMPLATE_OPTIONS[0]
  return cloneAdvancedCustomConfig(template.config)
}

export function isAdvancedCustomManagementPath(path: string): boolean {
  return (
    path === ADVANCED_CUSTOM_MODEL_LIST_PATH ||
    path === ADVANCED_CUSTOM_BALANCE_PATH
  )
}

export function getAdvancedCustomManagementRoute(
  config: AdvancedCustomConfig,
  path: string
): AdvancedCustomRoute | undefined {
  return normalizeAdvancedCustomConfig(config).advanced_routes?.find(
    (route) => route.incoming_path?.trim() === path
  )
}

export function replaceAdvancedCustomManagementRoute(
  config: AdvancedCustomConfig,
  path: string,
  route: AdvancedCustomRoute | null
): AdvancedCustomConfig {
  const normalized = normalizeAdvancedCustomConfig(config)
  const routes = [...(normalized.advanced_routes || [])]
  const index = routes.findIndex(
    (candidate) => candidate.incoming_path?.trim() === path
  )
  if (route === null) {
    if (index >= 0) routes.splice(index, 1)
  } else {
    const managementRoute: AdvancedCustomRoute = {
      incoming_path: path,
      upstream_path: route.upstream_path || '',
      converter: 'none',
      models: [],
      auth: route.auth,
    }
    if (index >= 0) routes[index] = managementRoute
    else routes.push(managementRoute)
  }
  return { advanced_routes: routes }
}

export function replaceAdvancedCustomForwardingRoutes(
  config: AdvancedCustomConfig,
  forwardingRoutes: AdvancedCustomRoute[]
): AdvancedCustomConfig {
  const normalized = normalizeAdvancedCustomConfig(config)
  const routes = normalized.advanced_routes || []
  const firstForwardingIndex = routes.findIndex(
    (route) =>
      !isAdvancedCustomManagementPath(route.incoming_path?.trim() || '')
  )
  const managementRoutes = routes.filter((route) =>
    isAdvancedCustomManagementPath(route.incoming_path?.trim() || '')
  )
  if (firstForwardingIndex < 0) {
    return { advanced_routes: [...managementRoutes, ...forwardingRoutes] }
  }

  const before = routes
    .slice(0, firstForwardingIndex)
    .filter((route) =>
      isAdvancedCustomManagementPath(route.incoming_path?.trim() || '')
    )
  const after = routes
    .slice(firstForwardingIndex)
    .filter((route) =>
      isAdvancedCustomManagementPath(route.incoming_path?.trim() || '')
    )
  return { advanced_routes: [...before, ...forwardingRoutes, ...after] }
}

export function createAdvancedCustomRoute(): AdvancedCustomRoute {
  return {
    incoming_path: openAIChatPath,
    upstream_path: openAIChatPath,
    converter: 'none',
  }
}

export function createAdvancedCustomConfig(): AdvancedCustomConfig {
  return {
    advanced_routes: [createAdvancedCustomRoute()],
  }
}

export function createAdvancedCustomManagementRoute(
  path: string
): AdvancedCustomRoute {
  return {
    incoming_path: path,
    upstream_path: path,
    converter: 'none',
    models: [],
  }
}

export function getAdvancedCustomUpstreamPathPlaceholder(
  converter: AdvancedCustomConverter,
  incomingPath = getDefaultAdvancedCustomIncomingPath(converter)
): string {
  return getAdvancedCustomConverterDefaults(converter, incomingPath)
    .upstream_path
}

export function getAdvancedCustomConverterDefaults(
  converter: AdvancedCustomConverter,
  incomingPath: string
): AdvancedCustomConverterDefaults {
  const normalizedIncomingPath =
    incomingPath.trim() || getDefaultAdvancedCustomIncomingPath(converter)

  if (converter === 'none') {
    return {
      upstream_path: normalizedIncomingPath,
      auth: getAdvancedCustomNativeAuth(normalizedIncomingPath),
    }
  }
  if (
    converter === 'anthropic_messages_to_openai_chat_completions' ||
    converter === 'gemini_generate_content_to_openai_chat_completions' ||
    converter === 'openai_responses_to_openai_chat_completions'
  ) {
    return { upstream_path: openAIChatPath, auth: bearerHeaderAuth() }
  }
  if (converter === 'openai_chat_completions_to_openai_responses') {
    return { upstream_path: openAIResponsesPath, auth: bearerHeaderAuth() }
  }
  if (converter === 'openai_chat_completions_to_anthropic_messages') {
    return { upstream_path: claudeMessagesPath, auth: apiKeyHeaderAuth() }
  }
  if (
    converter === 'openai_chat_completions_to_gemini_generate_content' ||
    converter === 'openai_responses_to_gemini_generate_content'
  ) {
    return { upstream_path: geminiGenerateContentPath, auth: geminiQueryAuth() }
  }

  return {
    upstream_path: normalizedIncomingPath || openAIChatPath,
    auth: getAdvancedCustomNativeAuth(normalizedIncomingPath),
  }
}

function getAdvancedCustomNativeAuth(
  incomingPath: string
): AdvancedCustomRouteAuth {
  if (incomingPath === claudeMessagesPath) {
    return apiKeyHeaderAuth()
  }
  if (
    incomingPath.includes(':generateContent') ||
    incomingPath.includes(':streamGenerateContent') ||
    incomingPath.includes(':embedContent') ||
    incomingPath.includes(':batchEmbedContents')
  ) {
    return geminiQueryAuth()
  }
  return bearerHeaderAuth()
}

export function getAdvancedCustomIncomingPathOptions(
  converter: AdvancedCustomConverter
): AdvancedCustomIncomingPathOption[] {
  return ADVANCED_CUSTOM_INCOMING_PATH_OPTIONS.filter((option) =>
    isConverterPathAllowed(option.value, converter)
  )
}

export function getDefaultAdvancedCustomIncomingPath(
  converter: AdvancedCustomConverter
): string {
  return (
    getAdvancedCustomIncomingPathOptions(converter)[0]?.value ||
    '/v1/chat/completions'
  )
}

export function isAdvancedCustomIncomingPathAllowed(
  incomingPath: string,
  converter: AdvancedCustomConverter
): boolean {
  return isConverterPathAllowed(incomingPath, converter)
}

export function isAdvancedCustomPassThroughAllowed(
  converter: AdvancedCustomConverter
): boolean {
  return converter === 'none'
}

export function getAdvancedCustomConverterOptions(
  incomingPath: string
): typeof ADVANCED_CUSTOM_CONVERTER_OPTIONS {
  const normalizedIncomingPath = incomingPath.trim()
  return ADVANCED_CUSTOM_CONVERTER_OPTIONS.filter(
    (option) =>
      option.value === 'none' ||
      isConverterPathAllowed(normalizedIncomingPath, option.value)
  )
}

export function getAdvancedCustomIncomingPathLabel(value: string): string {
  return (
    ADVANCED_CUSTOM_INCOMING_PATH_OPTIONS.find(
      (option) => option.value === value
    )?.label || value
  )
}

export function parseAdvancedCustomConfig(
  value: string | undefined
): AdvancedCustomConfig | null {
  if (!value?.trim()) return null
  try {
    const parsed = JSON.parse(value)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      return null
    }
    return normalizeAdvancedCustomConfig(parsed as AdvancedCustomConfig)
  } catch {
    return null
  }
}

export function stringifyAdvancedCustomConfig(
  config: AdvancedCustomConfig
): string {
  return JSON.stringify(normalizeAdvancedCustomConfig(config), null, 2)
}

export function normalizeAdvancedCustomConfig(
  config: AdvancedCustomConfig
): AdvancedCustomConfig {
  const routes = Array.isArray(config.advanced_routes)
    ? config.advanced_routes.map(normalizeAdvancedCustomRoute)
    : []

  return {
    advanced_routes: routes,
  }
}

export function parseAdvancedCustomRouteModels(value: string): string[] {
  return [
    ...new Set(
      value
        .split(',')
        .map((model) => model.trim())
        .filter(Boolean)
    ),
  ]
}

export function getAdvancedCustomModelRuleKind(
  modelRule: string
): AdvancedCustomModelRuleKind {
  return modelRule.startsWith(ADVANCED_CUSTOM_MODEL_REGEX_PREFIX)
    ? 'regex'
    : 'exact'
}

export function getAdvancedCustomRegexModelPattern(modelRule: string): string {
  return modelRule.slice(ADVANCED_CUSTOM_MODEL_REGEX_PREFIX.length)
}

export function validateAdvancedCustomConfig(
  config: AdvancedCustomConfig | null
): AdvancedCustomValidationError | null {
  if (!config) {
    return { message: 'Advanced custom configuration is required' }
  }

  const normalized = normalizeAdvancedCustomConfig(config)
  const routes = normalized.advanced_routes || []
  if (routes.length === 0) {
    return {
      message: 'Advanced custom configuration requires at least one route',
    }
  }

  const routeModelsByPath = new Map<
    string,
    { catchAllIndex: number | null; models: Map<string, number> }
  >()
  let modelListRouteIndex: number | null = null
  let balanceRouteIndex: number | null = null
  for (let index = 0; index < routes.length; index += 1) {
    const route = routes[index]
    const incomingPath = route.incoming_path?.trim() || ''
    const upstreamPath = getAdvancedCustomRouteUpstreamPath(route)
    const converter = route.converter || 'none'
    const routeModels = normalizeAdvancedCustomRouteModels(route.models)

    if (!incomingPath) {
      return { routeIndex: index, message: 'Incoming path is required' }
    }
    if (!incomingPath.startsWith('/')) {
      return { routeIndex: index, message: 'Incoming path must start with /' }
    }
    if (incomingPath.includes('?')) {
      return {
        routeIndex: index,
        message: 'Incoming path must not include query',
      }
    }
    if (isAdvancedCustomManagementPath(incomingPath)) {
      const isModelListRoute = incomingPath === ADVANCED_CUSTOM_MODEL_LIST_PATH
      const existingIndex = isModelListRoute
        ? modelListRouteIndex
        : balanceRouteIndex
      const routeLabel = isModelListRoute ? 'OpenAI Models' : 'Balance Query'
      if (existingIndex !== null) {
        return {
          routeIndex: index,
          message: `Only one ${routeLabel} route is allowed`,
        }
      }
      if (isModelListRoute) modelListRouteIndex = index
      else balanceRouteIndex = index
      if (routeModels.length > 0) {
        return {
          routeIndex: index,
          message: `${routeLabel} route does not support client model rules`,
        }
      }
      if (converter !== 'none') {
        return {
          routeIndex: index,
          message: `${routeLabel} route must use native forwarding`,
        }
      }
      if (upstreamPath.includes('{model}')) {
        return {
          routeIndex: index,
          message: `${routeLabel} upstream path must not contain {model}`,
        }
      }
      if (route.pass_through_body_enabled) {
        return {
          routeIndex: index,
          message: `${routeLabel} route does not support pass-through`,
        }
      }
    }
    const routeModelsError = validateAdvancedCustomRouteModels(
      index,
      incomingPath,
      routeModels,
      routeModelsByPath
    )
    if (routeModelsError) {
      return routeModelsError
    }

    if (!upstreamPath) {
      return { routeIndex: index, message: 'Upstream path is required' }
    }
    if (!isFullHttpURLOrAbsolutePath(upstreamPath)) {
      return {
        routeIndex: index,
        message: 'Upstream path must be a full URL or a path starting with /',
      }
    }
    if (!isAdvancedCustomConverter(converter)) {
      return { routeIndex: index, message: 'Converter is not registered' }
    }
    if (!isConverterPathAllowed(incomingPath, converter)) {
      return {
        routeIndex: index,
        message: 'Converter does not match incoming path',
      }
    }
    if (
      route.pass_through_body_enabled &&
      !isAdvancedCustomPassThroughAllowed(converter)
    ) {
      return {
        routeIndex: index,
        message: 'Pass-through requires native forwarding',
      }
    }

    const authError = validateRouteAuth(route.auth)
    if (authError) {
      return { routeIndex: index, message: authError }
    }
  }

  return null
}

export function hasValidAdvancedCustomModelListRoute(
  config: AdvancedCustomConfig | null
): boolean {
  if (!config || validateAdvancedCustomConfig(config)) return false
  const normalized = normalizeAdvancedCustomConfig(config)
  return (normalized.advanced_routes || []).some(
    (route) => route.incoming_path?.trim() === ADVANCED_CUSTOM_MODEL_LIST_PATH
  )
}

export function advancedCustomConfigUsesRelativeUpstreamPath(
  config: AdvancedCustomConfig | null
): boolean {
  if (!config) return false
  const normalized = normalizeAdvancedCustomConfig(config)
  return (normalized.advanced_routes || []).some((route) =>
    getAdvancedCustomRouteUpstreamPath(route).startsWith('/')
  )
}

export function getAdvancedCustomStats(value: string | undefined): {
  routeCount: number
  valid: boolean
  routeTypeLabels: string[]
} {
  const config = parseAdvancedCustomConfig(value)
  if (!config) {
    return { routeCount: 0, valid: false, routeTypeLabels: [] }
  }
  const normalized = normalizeAdvancedCustomConfig(config)
  const routes = normalized.advanced_routes || []
  const routeTypeLabels: string[] = []
  const seenRouteTypeLabels = new Set<string>()

  for (const route of routes) {
    const label = getAdvancedCustomRouteSummaryLabel(route)
    if (!label || seenRouteTypeLabels.has(label)) continue
    routeTypeLabels.push(label)
    seenRouteTypeLabels.add(label)
  }

  return {
    routeCount: routes.length,
    valid: validateAdvancedCustomConfig(normalized) === null,
    routeTypeLabels,
  }
}

export function getAdvancedCustomAuthMode(
  route: AdvancedCustomRoute
): AdvancedCustomAuthMode {
  return route.auth?.type || 'default'
}

export function buildAdvancedCustomAuth(
  mode: AdvancedCustomAuthMode,
  previousAuth: AdvancedCustomRouteAuth | undefined
): AdvancedCustomRouteAuth | undefined {
  if (mode === 'default') return undefined
  if (mode === 'none') return { type: 'none' }
  if (mode === 'header') {
    return {
      type: 'header',
      name: previousAuth?.name || 'Authorization',
      value: previousAuth?.value || 'Bearer {api_key}',
    }
  }
  return {
    type: 'query',
    name: previousAuth?.name || 'api_key',
    value: previousAuth?.value || '{api_key}',
  }
}

function normalizeAdvancedCustomRoute(
  route: AdvancedCustomRoute
): AdvancedCustomRoute {
  const nextRoute: AdvancedCustomRoute = {
    incoming_path: route.incoming_path || '',
    upstream_path: getAdvancedCustomRouteUpstreamPath(route),
    converter: route.converter || 'none',
  }
  const models = normalizeAdvancedCustomRouteModels(route.models)
  if (models.length > 0) {
    nextRoute.models = models
  }
  if (route.pass_through_body_enabled === true) {
    nextRoute.pass_through_body_enabled = true
  }
  if (route.auth) {
    nextRoute.auth = {
      type: route.auth.type,
      name: route.auth.name || '',
      value: route.auth.value || '',
    }
  }
  return nextRoute
}

function normalizeAdvancedCustomRouteModels(
  models: string[] | undefined
): string[] {
  if (!Array.isArray(models)) return []
  return models.map((model) => model.trim()).filter(Boolean)
}

function validateAdvancedCustomRouteModels(
  routeIndex: number,
  incomingPath: string,
  models: string[],
  routeModelsByPath: Map<
    string,
    { catchAllIndex: number | null; models: Map<string, number> }
  >
): AdvancedCustomValidationError | null {
  let state = routeModelsByPath.get(incomingPath)
  if (!state) {
    state = { catchAllIndex: null, models: new Map<string, number>() }
    routeModelsByPath.set(incomingPath, state)
  }

  if (models.length === 0) {
    if (state.catchAllIndex !== null) {
      return {
        routeIndex,
        message:
          'Only one catch-all route is allowed for the same incoming path',
      }
    }
    state.catchAllIndex = routeIndex
    return null
  }

  if (state.catchAllIndex !== null) {
    return {
      routeIndex,
      message: 'Catch-all route must be last for the same incoming path',
    }
  }

  const seenInRoute = new Set<string>()
  for (const model of models) {
    if (
      getAdvancedCustomModelRuleKind(model) === 'regex' &&
      getAdvancedCustomRegexModelPattern(model) === ''
    ) {
      return { routeIndex, message: 'Model regex cannot be empty' }
    }
    if (seenInRoute.has(model)) {
      return { routeIndex, message: 'Duplicate model in route models' }
    }
    seenInRoute.add(model)
    if (state.models.has(model)) {
      return {
        routeIndex,
        message: 'Route models must be unique for the same incoming path',
      }
    }
    state.models.set(model, routeIndex)
  }
  return null
}

function getAdvancedCustomRouteUpstreamPath(
  route: AdvancedCustomRoute
): string {
  return (route.upstream_path || '').trim()
}

function getAdvancedCustomRouteSummaryLabel(
  route: AdvancedCustomRoute
): string | null {
  const incomingPath = route.incoming_path?.trim() || ''
  if (!incomingPath) return null
  return (
    ADVANCED_CUSTOM_ROUTE_SUMMARY_LABELS[incomingPath] ||
    getAdvancedCustomIncomingPathLabel(incomingPath)
  )
}

function isFullHttpURLOrAbsolutePath(value: string): boolean {
  if (value.startsWith('/')) return !value.startsWith('//')

  try {
    const parsed = new URL(value)
    return (
      Boolean(parsed.host) &&
      (parsed.protocol === 'http:' || parsed.protocol === 'https:')
    )
  } catch {
    return false
  }
}

function isAdvancedCustomConverter(
  value: string
): value is AdvancedCustomConverter {
  return ADVANCED_CUSTOM_CONVERTER_OPTIONS.some(
    (option) => option.value === value
  )
}

function isConverterPathAllowed(
  incomingPath: string,
  converter: AdvancedCustomConverter
): boolean {
  if (converter === 'none') return true
  if (incomingPath === '/v1/alpha/search') return false
  if (converter === 'anthropic_messages_to_openai_chat_completions') {
    return incomingPath === '/v1/messages'
  }
  if (
    converter === 'openai_chat_completions_to_anthropic_messages' ||
    converter === 'openai_chat_completions_to_openai_responses' ||
    converter === 'openai_chat_completions_to_gemini_generate_content'
  ) {
    return incomingPath === '/v1/chat/completions'
  }
  if (converter === 'openai_responses_to_openai_chat_completions') {
    return incomingPath === '/v1/responses'
  }
  if (converter === 'openai_responses_to_gemini_generate_content') {
    return incomingPath === '/v1/responses'
  }
  return (
    incomingPath.includes(':generateContent') ||
    incomingPath.includes(':streamGenerateContent')
  )
}

function validateRouteAuth(
  auth: AdvancedCustomRouteAuth | undefined
): string | null {
  if (!auth) return null
  if (auth.type === 'none') return null
  if (auth.type !== 'header' && auth.type !== 'query') {
    return 'Auth type is invalid'
  }
  if (!auth.name?.trim()) {
    return 'Auth name is required'
  }
  if (!auth.value?.trim()) {
    return 'Auth value is required'
  }
  return null
}
