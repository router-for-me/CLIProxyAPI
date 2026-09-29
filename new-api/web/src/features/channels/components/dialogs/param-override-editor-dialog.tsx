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
import {
  ChevronDown,
  ChevronUp,
  Copy,
  GripVertical,
  Plus,
  Search,
  Trash2,
} from 'lucide-react'
import {
  type DragEvent,
  type KeyboardEvent,
  useCallback,
  useEffect,
  useMemo,
  useState,
} from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { JsonCodeEditor } from '@/components/json-code-editor'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { Combobox } from '@/components/ui/combobox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { cn } from '@/lib/utils'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type ParamOverrideCondition = {
  id: string
  path: string
  mode: string
  value_text: string
  invert: boolean
  pass_missing_key: boolean
}

type ParamOverrideOperation = {
  id: string
  description: string
  path: string
  mode: string
  from: string
  to: string
  value_text: string
  keep_origin: boolean
  logic: string
  conditions: ParamOverrideCondition[]
}

export type ParamOverrideEditorDialogProps = {
  open: boolean
  value: string
  onOpenChange: (open: boolean) => void
  onSave: (value: string) => void
}

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const OPERATION_MODE_OPTIONS = [
  { label: 'Set Field', value: 'set' },
  { label: 'Delete Field', value: 'delete' },
  { label: 'Append to End', value: 'append' },
  { label: 'Prepend to Start', value: 'prepend' },
  { label: 'Copy Field', value: 'copy' },
  { label: 'Move Field', value: 'move' },
  { label: 'String Replace', value: 'replace' },
  { label: 'Regex Replace', value: 'regex_replace' },
  { label: 'Trim Prefix', value: 'trim_prefix' },
  { label: 'Trim Suffix', value: 'trim_suffix' },
  { label: 'Ensure Prefix', value: 'ensure_prefix' },
  { label: 'Ensure Suffix', value: 'ensure_suffix' },
  { label: 'Trim Space', value: 'trim_space' },
  { label: 'To Lowercase', value: 'to_lower' },
  { label: 'To Uppercase', value: 'to_upper' },
  { label: 'Return Custom Error', value: 'return_error' },
  { label: 'Prune Object Items', value: 'prune_objects' },
  { label: 'Pass Through Headers', value: 'pass_headers' },
  { label: 'Sync Fields', value: 'sync_fields' },
  { label: 'Set Request Header', value: 'set_header' },
  { label: 'Delete Request Header', value: 'delete_header' },
  { label: 'Copy Request Header', value: 'copy_header' },
  { label: 'Move Request Header', value: 'move_header' },
]

const OPERATION_MODE_VALUES = new Set(
  OPERATION_MODE_OPTIONS.map((o) => o.value)
)

const OPERATION_MODE_LABEL_MAP = OPERATION_MODE_OPTIONS.reduce<
  Record<string, string>
>((acc, item) => {
  acc[item.value] = item.label
  return acc
}, {})

const CONDITION_MODE_OPTIONS = [
  { label: 'Exact Match', value: 'full' },
  { label: 'Prefix', value: 'prefix' },
  { label: 'Suffix', value: 'suffix' },
  { label: 'Contains', value: 'contains' },
  { label: 'Greater Than', value: 'gt' },
  { label: 'Greater Than or Equal', value: 'gte' },
  { label: 'Less Than', value: 'lt' },
  { label: 'Less Than or Equal', value: 'lte' },
]

const CONDITION_MODE_VALUES = new Set(
  CONDITION_MODE_OPTIONS.map((o) => o.value)
)

const MODE_META: Record<
  string,
  {
    path?: boolean
    pathOptional?: boolean
    value?: boolean
    from?: boolean
    to?: boolean
    keepOrigin?: boolean
    pathAlias?: boolean
  }
> = {
  delete: { path: true },
  set: { path: true, value: true, keepOrigin: true },
  append: { path: true, value: true, keepOrigin: true },
  prepend: { path: true, value: true, keepOrigin: true },
  copy: { from: true, to: true },
  move: { from: true, to: true },
  replace: { path: true, from: true, to: false },
  regex_replace: { path: true, from: true, to: false },
  trim_prefix: { path: true, value: true },
  trim_suffix: { path: true, value: true },
  ensure_prefix: { path: true, value: true },
  ensure_suffix: { path: true, value: true },
  trim_space: { path: true },
  to_lower: { path: true },
  to_upper: { path: true },
  return_error: { value: true },
  prune_objects: { pathOptional: true, value: true },
  pass_headers: { value: true, keepOrigin: true },
  sync_fields: { from: true, to: true },
  set_header: { path: true, value: true, keepOrigin: true },
  delete_header: { path: true },
  copy_header: { from: true, to: true, keepOrigin: true, pathAlias: true },
  move_header: { from: true, to: true, keepOrigin: true, pathAlias: true },
}

const VALUE_REQUIRED_MODES = new Set([
  'trim_prefix',
  'trim_suffix',
  'ensure_prefix',
  'ensure_suffix',
  'set_header',
  'return_error',
  'prune_objects',
  'pass_headers',
])

const FROM_REQUIRED_MODES = new Set([
  'copy',
  'move',
  'replace',
  'regex_replace',
  'copy_header',
  'move_header',
  'sync_fields',
])

const TO_REQUIRED_MODES = new Set([
  'copy',
  'move',
  'copy_header',
  'move_header',
  'sync_fields',
])

const MODE_DESCRIPTIONS: Record<string, string> = {
  set: 'Write value to the target field',
  delete: 'Remove the target field',
  append: 'Append value to array / string / object end',
  prepend: 'Prepend value to array / string / object start',
  copy: 'Copy source field to target field',
  move: 'Move source field to target field',
  replace: 'Do string replacement in the target field',
  regex_replace: 'Do regex replacement in the target field',
  trim_prefix: 'Remove string prefix',
  trim_suffix: 'Remove string suffix',
  ensure_prefix: 'Ensure the string has a specified prefix',
  ensure_suffix: 'Ensure the string has a specified suffix',
  trim_space: 'Trim leading/trailing whitespace',
  to_lower: 'Convert string to lowercase',
  to_upper: 'Convert string to uppercase',
  return_error: 'Return a custom error immediately',
  prune_objects: 'Prune object items by conditions',
  pass_headers: 'Pass specified request headers to upstream',
  sync_fields: 'Auto-fill when one field exists and another is missing',
  set_header:
    'Set runtime request header: override entire value, or manipulate comma-separated tokens',
  delete_header: 'Delete a runtime request header',
  copy_header: 'Copy a request header',
  move_header: 'Move a request header',
}

const SYNC_TARGET_TYPE_OPTIONS = [
  { label: 'Request Body Field', value: 'json' },
  { label: 'Request Header Field', value: 'header' },
]

// Templates

const LEGACY_TEMPLATE = { temperature: 0, max_tokens: 1000 }

const OPERATION_TEMPLATE = {
  operations: [
    {
      description: 'Set default temperature for openai/* models.',
      path: 'temperature',
      mode: 'set',
      value: 0.7,
      conditions: [{ path: 'model', mode: 'prefix', value: 'openai/' }],
      logic: 'AND',
    },
  ],
}

const HEADER_PASSTHROUGH_TEMPLATE = {
  operations: [
    {
      description: 'Pass through X-Request-Id header to upstream.',
      mode: 'pass_headers',
      value: ['X-Request-Id'],
      keep_origin: true,
    },
  ],
}

const GEMINI_IMAGE_4K_TEMPLATE = {
  operations: [
    {
      description:
        'Set imageSize to 4K when model contains gemini/image and ends with 4k.',
      mode: 'set',
      path: 'generationConfig.imageConfig.imageSize',
      value: '4K',
      conditions: [
        { path: 'original_model', mode: 'contains', value: 'gemini' },
        { path: 'original_model', mode: 'contains', value: 'image' },
        { path: 'original_model', mode: 'suffix', value: '4k' },
      ],
      logic: 'AND',
    },
  ],
}

// Keep in sync with upstream Codex request headers:
// https://github.com/openai/codex/commit/7c7b4861d88960f7e3bd5b7f30f8351be666dd84
// https://github.com/openai/codex/commit/14df0e8833aad0d6d78287954b61ffac67af936c
// https://github.com/openai/codex/commit/ebdd8795e924a8149b616e46ca2ed7848c207a4b
const CODEX_CLI_HEADER_PASSTHROUGH_HEADERS = [
  'Originator',
  'Session_id',
  'Thread_id',
  'Session-Id',
  'Thread-Id',
  'X-Client-Request-Id',
  'User-Agent',
  'X-Codex-Beta-Features',
  'X-Codex-Turn-State',
  'X-Codex-Turn-Metadata',
  'X-Codex-Window-Id',
  'X-Codex-Parent-Thread-Id',
  // 'X-Codex-Installation-Id',
  'X-OpenAI-Subagent',
  'X-OpenAI-Memgen-Request',
  // 'X-OAI-Attestation',
  'X-ResponsesAPI-Include-Timing-Metrics',
  'X-OpenAI-Internal-Codex-Responses-Lite',
]

const CLAUDE_CLI_HEADER_PASSTHROUGH_HEADERS = [
  'X-Stainless-Arch',
  'X-Stainless-Lang',
  'X-Stainless-Os',
  'X-Stainless-Package-Version',
  'X-Stainless-Retry-Count',
  'X-Stainless-Runtime',
  'X-Stainless-Runtime-Version',
  'X-Stainless-Timeout',
  'User-Agent',
  'X-App',
  'Anthropic-Beta',
  'Anthropic-Dangerous-Direct-Browser-Access',
  'Anthropic-Version',
]

const buildPassHeadersTemplate = (headers: string[]) => ({
  operations: [
    { mode: 'pass_headers', value: [...headers], keep_origin: true },
  ],
})

const CODEX_CLI_HEADER_PASSTHROUGH_TEMPLATE = {
  operations: [
    {
      mode: 'pass_headers',
      value: [...CODEX_CLI_HEADER_PASSTHROUGH_HEADERS],
      keep_origin: true,
    },
  ],
}
const CLAUDE_CLI_HEADER_PASSTHROUGH_TEMPLATE = buildPassHeadersTemplate(
  CLAUDE_CLI_HEADER_PASSTHROUGH_HEADERS
)

const AWS_BEDROCK_ANTHROPIC_COMPAT_TEMPLATE = {
  operations: [
    {
      description:
        'Normalize anthropic-beta header tokens for Bedrock compatibility.',
      mode: 'set_header',
      path: 'anthropic-beta',
      value: {
        'advanced-tool-use-2025-11-20': 'tool-search-tool-2025-10-19',
        bash_20241022: null,
        bash_20250124: null,
        'code-execution-2025-08-25': null,
        'compact-2026-01-12': 'compact-2026-01-12',
        'computer-use-2025-01-24': 'computer-use-2025-01-24',
        'computer-use-2025-11-24': 'computer-use-2025-11-24',
        'context-1m-2025-08-07': 'context-1m-2025-08-07',
        'context-management-2025-06-27': 'context-management-2025-06-27',
        'effort-2025-11-24': null,
        'fast-mode-2026-02-01': null,
        'files-api-2025-04-14': null,
        'fine-grained-tool-streaming-2025-05-14': null,
        'interleaved-thinking-2025-05-14': 'interleaved-thinking-2025-05-14',
        'mcp-client-2025-11-20': null,
        'mcp-client-2025-04-04': null,
        'mcp-servers-2025-12-04': null,
        'output-128k-2025-02-19': null,
        'structured-output-2024-03-01': null,
        'prompt-caching-scope-2026-01-05': null,
        'skills-2025-10-02': null,
        'structured-outputs-2025-11-13': null,
        text_editor_20241022: null,
        text_editor_20250124: null,
        'token-efficient-tools-2025-02-19': null,
        'tool-search-tool-2025-10-19': 'tool-search-tool-2025-10-19',
        'web-fetch-2025-09-10': null,
        'web-search-2025-03-05': null,
        'oauth-2025-04-20': null,
      },
    },
    {
      description:
        'Remove all tools[*].custom.input_examples before upstream relay.',
      mode: 'delete',
      path: 'tools.*.custom.input_examples',
    },
  ],
}

type TemplatePresetConfig = {
  label: string
  kind: 'operations' | 'legacy'
  payload: Record<string, unknown>
}

const TEMPLATE_PRESET_CONFIG: Record<string, TemplatePresetConfig> = {
  operations_default: {
    label: 'New Format Template',
    kind: 'operations',
    payload: OPERATION_TEMPLATE,
  },
  legacy_default: {
    label: 'Legacy Format Template',
    kind: 'legacy',
    payload: LEGACY_TEMPLATE,
  },
  pass_headers_auth: {
    label: 'Header Passthrough (X-Request-Id)',
    kind: 'operations',
    payload: HEADER_PASSTHROUGH_TEMPLATE,
  },
  gemini_image_4k: {
    label: 'Gemini Image 4K',
    kind: 'operations',
    payload: GEMINI_IMAGE_4K_TEMPLATE,
  },
  claude_cli_headers_passthrough: {
    label: 'Claude CLI Header Passthrough',
    kind: 'operations',
    payload: CLAUDE_CLI_HEADER_PASSTHROUGH_TEMPLATE,
  },
  codex_cli_headers_passthrough: {
    label: 'Codex CLI Header Passthrough',
    kind: 'operations',
    payload: CODEX_CLI_HEADER_PASSTHROUGH_TEMPLATE,
  },
  aws_bedrock_anthropic_beta_override: {
    label: 'AWS Bedrock Claude Compat',
    kind: 'operations',
    payload: AWS_BEDROCK_ANTHROPIC_COMPAT_TEMPLATE,
  },
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

let localIdSeed = 0
const nextLocalId = () => `po_${Date.now()}_${localIdSeed++}`

const toValueText = (value: unknown): string => {
  if (value === undefined) return ''
  if (typeof value === 'string') return value
  try {
    return JSON.stringify(value)
  } catch {
    return String(value)
  }
}

const parseLooseValue = (valueText: string): unknown => {
  const raw = String(valueText ?? '').trim()
  if (raw === '') return ''
  try {
    return JSON.parse(raw)
  } catch {
    return raw
  }
}

const verifyJSON = (text: string): boolean => {
  try {
    JSON.parse(text)
    return true
  } catch {
    return false
  }
}

const normalizeCondition = (
  condition: Record<string, unknown> = {}
): ParamOverrideCondition => ({
  id: nextLocalId(),
  path: typeof condition.path === 'string' ? condition.path : '',
  mode: CONDITION_MODE_VALUES.has(condition.mode as string)
    ? (condition.mode as string)
    : 'full',
  value_text: toValueText(condition.value),
  invert: condition.invert === true,
  pass_missing_key: condition.pass_missing_key === true,
})

const createDefaultCondition = (): ParamOverrideCondition =>
  normalizeCondition({})

const normalizeOperation = (
  operation: Record<string, unknown> = {}
): ParamOverrideOperation => ({
  id: nextLocalId(),
  description:
    typeof operation.description === 'string' ? operation.description : '',
  path: typeof operation.path === 'string' ? operation.path : '',
  mode: OPERATION_MODE_VALUES.has(operation.mode as string)
    ? (operation.mode as string)
    : 'set',
  value_text: toValueText(operation.value),
  keep_origin: operation.keep_origin === true,
  from: typeof operation.from === 'string' ? operation.from : '',
  to: typeof operation.to === 'string' ? operation.to : '',
  logic: String(operation.logic || 'OR').toUpperCase() === 'AND' ? 'AND' : 'OR',
  conditions: Array.isArray(operation.conditions)
    ? (operation.conditions as Record<string, unknown>[]).map(
        normalizeCondition
      )
    : [],
})

const createDefaultOperation = (): ParamOverrideOperation =>
  normalizeOperation({ mode: 'set' })

const reorderOperations = (
  ops: ParamOverrideOperation[],
  sourceId: string,
  targetId: string,
  position: 'before' | 'after' = 'before'
): ParamOverrideOperation[] => {
  if (!sourceId || !targetId || sourceId === targetId) return ops
  const srcIdx = ops.findIndex((o) => o.id === sourceId)
  if (srcIdx < 0) return ops
  const next = [...ops]
  const [moved] = next.splice(srcIdx, 1)
  let insertIdx = next.findIndex((o) => o.id === targetId)
  if (insertIdx < 0) return ops
  if (position === 'after') insertIdx += 1
  next.splice(insertIdx, 0, moved)
  return next
}

const isOperationBlank = (operation: ParamOverrideOperation): boolean => {
  const hasCondition = operation.conditions.some(
    (c) =>
      c.path.trim() ||
      c.value_text.trim() ||
      c.mode !== 'full' ||
      c.invert ||
      c.pass_missing_key
  )
  return (
    operation.mode === 'set' &&
    !operation.path.trim() &&
    !operation.from.trim() &&
    !operation.to.trim() &&
    operation.value_text.trim() === '' &&
    !operation.keep_origin &&
    !hasCondition
  )
}

const getOperationSummary = (
  operation: ParamOverrideOperation,
  index: number
): string => {
  const mode = operation.mode || 'set'
  const modeLabel = OPERATION_MODE_LABEL_MAP[mode] || mode
  if (mode === 'sync_fields') {
    const from = operation.from.trim()
    const to = operation.to.trim()
    return `${index + 1}. ${modeLabel} · ${from || to || '-'}`
  }
  const path = operation.path.trim()
  const from = operation.from.trim()
  const to = operation.to.trim()
  return `${index + 1}. ${modeLabel} · ${path || from || to || '-'}`
}

const getModeTagTailwind = (mode: string): string => {
  if (mode.includes('header')) {
    return 'bg-cyan-500/15 text-cyan-700 dark:text-cyan-300 border-cyan-500/20'
  }
  if (mode.includes('replace') || mode.includes('trim')) {
    return 'bg-violet-500/15 text-violet-700 dark:text-violet-300 border-violet-500/20'
  }
  if (mode.includes('copy') || mode.includes('move')) {
    return 'bg-blue-500/15 text-blue-700 dark:text-blue-300 border-blue-500/20'
  }
  if (mode.includes('error') || mode.includes('prune')) {
    return 'bg-red-500/15 text-red-700 dark:text-red-300 border-red-500/20'
  }
  if (mode.includes('sync')) {
    return 'bg-green-500/15 text-green-700 dark:text-green-300 border-green-500/20'
  }
  return 'bg-muted text-muted-foreground'
}

const getModePathLabel = (mode: string): string => {
  if (mode === 'set_header' || mode === 'delete_header') return 'Header Name'
  if (mode === 'prune_objects') return 'Target Path (optional)'
  return 'Target Field Path'
}

const getModePathPlaceholder = (mode: string): string => {
  if (mode === 'set_header') return 'Authorization'
  if (mode === 'delete_header') return 'X-Debug-Mode'
  if (mode === 'prune_objects') return 'messages'
  return 'temperature'
}

const getModeFromLabel = (mode: string): string => {
  if (mode === 'replace') return 'Match Text'
  if (mode === 'regex_replace') return 'Regex Pattern'
  if (mode === 'copy_header' || mode === 'move_header') return 'Source Header'
  return 'Source Field'
}

const getModeFromPlaceholder = (mode: string): string => {
  if (mode === 'replace') return 'openai/'
  if (mode === 'regex_replace') return '^gpt-'
  if (mode === 'copy_header' || mode === 'move_header') return 'Authorization'
  return 'model'
}

const getModeToLabel = (mode: string): string => {
  if (mode === 'replace' || mode === 'regex_replace') return 'Replace With'
  if (mode === 'copy_header' || mode === 'move_header') return 'Target Header'
  return 'Target Field'
}

const getModeToPlaceholder = (mode: string): string => {
  if (mode === 'replace') return '(leave empty to delete)'
  if (mode === 'regex_replace') return 'openai/gpt-'
  if (mode === 'copy_header' || mode === 'move_header') return 'X-Upstream-Auth'
  return 'original_model'
}

const getModeValueLabel = (mode: string): string => {
  if (mode === 'set_header') {
    return 'Header Value (supports string or JSON mapping)'
  }
  if (mode === 'pass_headers') {
    return 'Pass-through Headers (comma-separated or JSON array)'
  }
  if (
    mode === 'trim_prefix' ||
    mode === 'trim_suffix' ||
    mode === 'ensure_prefix' ||
    mode === 'ensure_suffix'
  ) {
    return 'Prefix/Suffix Text'
  }
  if (mode === 'prune_objects') return 'Prune Rule (string or JSON object)'
  return 'Value (supports JSON or plain text)'
}

const getModeValuePlaceholder = (mode: string): string => {
  if (mode === 'set_header') return 'Bearer sk-xxx'
  if (mode === 'pass_headers') return 'Authorization, X-Request-Id'
  if (
    mode === 'trim_prefix' ||
    mode === 'trim_suffix' ||
    mode === 'ensure_prefix' ||
    mode === 'ensure_suffix'
  ) {
    return 'openai/'
  }
  if (mode === 'prune_objects') return '{"type":"redacted_thinking"}'
  return '0.7'
}

const parseSyncTargetSpec = (spec: string): { type: string; key: string } => {
  const raw = String(spec ?? '').trim()
  if (!raw) return { type: 'json', key: '' }
  const idx = raw.indexOf(':')
  if (idx < 0) return { type: 'json', key: raw }
  const prefix = raw.slice(0, idx).trim().toLowerCase()
  const key = raw.slice(idx + 1).trim()
  return prefix === 'header' ? { type: 'header', key } : { type: 'json', key }
}

const buildSyncTargetSpec = (type: string, key: string): string => {
  const normalizedType = type === 'header' ? 'header' : 'json'
  const normalizedKey = String(key ?? '').trim()
  if (!normalizedKey) return ''
  return `${normalizedType}:${normalizedKey}`
}

// return_error helpers

type ReturnErrorDraft = {
  message: string
  statusCode: number
  code: string
  type: string
  skipRetry: boolean
  simpleMode: boolean
}

const parseReturnErrorDraft = (valueText: string): ReturnErrorDraft => {
  const defaults: ReturnErrorDraft = {
    message: '',
    statusCode: 400,
    code: '',
    type: '',
    skipRetry: true,
    simpleMode: true,
  }
  const raw = String(valueText ?? '').trim()
  if (!raw) return defaults
  try {
    const parsed = JSON.parse(raw) as Record<string, unknown>
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
      const statusRaw =
        parsed.status_code !== undefined ? parsed.status_code : parsed.status
      const statusValue = Number(statusRaw)
      return {
        ...defaults,
        message: String(
          (parsed.message as string) || (parsed.msg as string) || ''
        ).trim(),
        statusCode:
          Number.isInteger(statusValue) &&
          statusValue >= 100 &&
          statusValue <= 599
            ? statusValue
            : 400,
        code: String((parsed.code as string) || '').trim(),
        type: String((parsed.type as string) || '').trim(),
        skipRetry: parsed.skip_retry !== false,
        simpleMode: false,
      }
    }
  } catch {
    /* treat as plain text */
  }
  return { ...defaults, message: raw, simpleMode: true }
}

const buildReturnErrorValueText = (
  draft: Partial<ReturnErrorDraft>
): string => {
  const message = String(draft.message || '').trim()
  if (draft.simpleMode) return message
  const statusCode = Number(draft.statusCode)
  const payload: Record<string, unknown> = {
    message,
    status_code:
      Number.isInteger(statusCode) && statusCode >= 100 && statusCode <= 599
        ? statusCode
        : 400,
  }
  const code = String(draft.code || '').trim()
  const type = String(draft.type || '').trim()
  if (code) payload.code = code
  if (type) payload.type = type
  if (draft.skipRetry === false) payload.skip_retry = false
  return JSON.stringify(payload)
}

// prune_objects helpers

type PruneRule = {
  id: string
  path: string
  mode: string
  value_text: string
  invert: boolean
  pass_missing_key: boolean
}

type PruneObjectsDraft = {
  simpleMode: boolean
  typeText: string
  logic: string
  recursive: boolean
  rules: PruneRule[]
}

const normalizePruneRule = (rule: Record<string, unknown> = {}): PruneRule => ({
  id: nextLocalId(),
  path: typeof rule.path === 'string' ? rule.path : '',
  mode: CONDITION_MODE_VALUES.has(rule.mode as string)
    ? (rule.mode as string)
    : 'full',
  value_text: toValueText(rule.value),
  invert: rule.invert === true,
  pass_missing_key: rule.pass_missing_key === true,
})

const parsePruneObjectsDraft = (valueText: string): PruneObjectsDraft => {
  const defaults: PruneObjectsDraft = {
    simpleMode: true,
    typeText: '',
    logic: 'AND',
    recursive: true,
    rules: [],
  }
  const raw = String(valueText ?? '').trim()
  if (!raw) return defaults
  try {
    const parsed = JSON.parse(raw)
    if (typeof parsed === 'string') {
      return { ...defaults, typeText: parsed.trim() }
    }
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
      const rules: PruneRule[] = []
      if (
        parsed.where &&
        typeof parsed.where === 'object' &&
        !Array.isArray(parsed.where)
      ) {
        for (const [path, value] of Object.entries(
          parsed.where as Record<string, unknown>
        )) {
          rules.push(normalizePruneRule({ path, mode: 'full', value }))
        }
      }
      if (Array.isArray(parsed.conditions)) {
        for (const item of parsed.conditions) {
          if (item && typeof item === 'object') {
            rules.push(normalizePruneRule(item))
          }
        }
      } else if (
        parsed.conditions &&
        typeof parsed.conditions === 'object' &&
        !Array.isArray(parsed.conditions)
      ) {
        for (const [path, value] of Object.entries(
          parsed.conditions as Record<string, unknown>
        )) {
          rules.push(normalizePruneRule({ path, mode: 'full', value }))
        }
      }
      const typeText =
        parsed.type === undefined ? '' : String(parsed.type).trim()
      const logic =
        String(parsed.logic || 'AND').toUpperCase() === 'OR' ? 'OR' : 'AND'
      const recursive = parsed.recursive !== false
      const hasAdvancedFields =
        parsed.logic !== undefined ||
        parsed.recursive !== undefined ||
        parsed.where !== undefined ||
        parsed.conditions !== undefined
      return {
        ...defaults,
        simpleMode: !hasAdvancedFields,
        typeText,
        logic,
        recursive,
        rules,
      }
    }
    return { ...defaults, typeText: String(parsed ?? '').trim() }
  } catch {
    return { ...defaults, typeText: raw }
  }
}

const buildPruneObjectsValueText = (draft: PruneObjectsDraft): string => {
  const typeText = String(draft.typeText || '').trim()
  if (draft.simpleMode) return typeText
  const payload: Record<string, unknown> = {}
  if (typeText) payload.type = typeText
  if (String(draft.logic || 'AND').toUpperCase() === 'OR') payload.logic = 'OR'
  if (draft.recursive === false) payload.recursive = false
  const conditions = (draft.rules || [])
    .filter((rule) => String(rule.path || '').trim())
    .map((rule) => {
      const conditionPayload: Record<string, unknown> = {
        path: String(rule.path || '').trim(),
        mode: CONDITION_MODE_VALUES.has(rule.mode) ? rule.mode : 'full',
      }
      const valueRaw = String(rule.value_text || '').trim()
      if (valueRaw !== '') conditionPayload.value = parseLooseValue(valueRaw)
      if (rule.invert) conditionPayload.invert = true
      if (rule.pass_missing_key) conditionPayload.pass_missing_key = true
      return conditionPayload
    })
  if (conditions.length > 0) payload.conditions = conditions
  if (!payload.type && !payload.conditions) {
    return JSON.stringify({ logic: 'AND' })
  }
  return JSON.stringify(payload)
}

// pass_headers helpers

const parsePassHeaderNames = (rawValue: unknown): string[] => {
  if (Array.isArray(rawValue)) {
    return rawValue.map((i) => String(i ?? '').trim()).filter(Boolean)
  }
  if (rawValue && typeof rawValue === 'object') {
    const obj = rawValue as Record<string, unknown>
    if (Array.isArray(obj.headers)) {
      return obj.headers.map((i) => String(i ?? '').trim()).filter(Boolean)
    }
    if (obj.header !== undefined) {
      const single = String(obj.header ?? '').trim()
      return single ? [single] : []
    }
    return []
  }
  if (typeof rawValue === 'string') {
    return rawValue
      .split(',')
      .map((i) => i.trim())
      .filter(Boolean)
  }
  return []
}

// Condition payload builder
const buildConditionPayload = (
  condition: ParamOverrideCondition
): Record<string, unknown> | null => {
  const path = condition.path.trim()
  if (!path) return null
  const payload: Record<string, unknown> = {
    path,
    mode: condition.mode || 'full',
    value: parseLooseValue(condition.value_text),
  }
  if (condition.invert) payload.invert = true
  if (condition.pass_missing_key) payload.pass_missing_key = true
  return payload
}

// Validation

const validateOperations = (
  operations: ParamOverrideOperation[],
  t: (key: string, options?: Record<string, unknown>) => string
): string => {
  for (let i = 0; i < operations.length; i++) {
    const op = operations[i]
    const mode = op.mode || 'set'
    const meta = MODE_META[mode] || MODE_META.set
    const line = i + 1
    const pathValue = op.path.trim()
    const fromValue = op.from.trim()
    const toValue = op.to.trim()

    if (meta.path && !pathValue) {
      return t('Rule {{line}} is missing target path', { line })
    }
    if (FROM_REQUIRED_MODES.has(mode) && !fromValue) {
      if (!(meta.pathAlias && pathValue)) {
        return t('Rule {{line}} is missing source field', { line })
      }
    }
    if (TO_REQUIRED_MODES.has(mode) && !toValue) {
      if (!(meta.pathAlias && pathValue)) {
        return t('Rule {{line}} is missing target field', { line })
      }
    }
    if (VALUE_REQUIRED_MODES.has(mode) && op.value_text.trim() === '') {
      return t('Rule {{line}} is missing value', { line })
    }

    if (mode === 'return_error') {
      const raw = op.value_text.trim()
      if (!raw) return t('Rule {{line}} is missing value', { line })
      try {
        const parsed = JSON.parse(raw)
        if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
          if (
            !String((parsed as Record<string, unknown>).message || '').trim()
          ) {
            return t('Rule {{line}} return_error requires a message field', {
              line,
            })
          }
        }
      } catch {
        /* plain string is allowed */
      }
    }

    if (mode === 'prune_objects') {
      const raw = op.value_text.trim()
      if (!raw) {
        return t('Rule {{line}} prune_objects is missing conditions', { line })
      }
    }

    if (mode === 'pass_headers') {
      const raw = op.value_text.trim()
      if (!raw) {
        return t('Rule {{line}} pass_headers is missing header names', { line })
      }
      const parsed = parseLooseValue(raw)
      const headers = parsePassHeaderNames(parsed)
      if (headers.length === 0) {
        return t('Rule {{line}} pass_headers format is invalid', { line })
      }
    }
  }
  return ''
}

// Parse initial state

type EditorState = {
  editMode: 'visual' | 'json'
  visualMode: 'operations' | 'legacy'
  legacyValue: string
  operations: ParamOverrideOperation[]
  jsonText: string
  jsonError: string
}

const parseInitialState = (rawValue: string): EditorState => {
  const text = typeof rawValue === 'string' ? rawValue : ''
  const trimmed = text.trim()
  if (!trimmed) {
    return {
      editMode: 'visual',
      visualMode: 'operations',
      legacyValue: '',
      operations: [createDefaultOperation()],
      jsonText: '',
      jsonError: '',
    }
  }

  if (!verifyJSON(trimmed)) {
    return {
      editMode: 'json',
      visualMode: 'operations',
      legacyValue: '',
      operations: [createDefaultOperation()],
      jsonText: text,
      jsonError: 'Invalid JSON format',
    }
  }

  const parsed = JSON.parse(trimmed) as Record<string, unknown>
  const pretty = JSON.stringify(parsed, null, 2)

  if (
    parsed &&
    typeof parsed === 'object' &&
    !Array.isArray(parsed) &&
    Array.isArray(parsed.operations)
  ) {
    return {
      editMode: 'visual',
      visualMode: 'operations',
      legacyValue: '',
      operations:
        (parsed.operations as Record<string, unknown>[]).length > 0
          ? (parsed.operations as Record<string, unknown>[]).map(
              normalizeOperation
            )
          : [createDefaultOperation()],
      jsonText: pretty,
      jsonError: '',
    }
  }

  if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
    return {
      editMode: 'visual',
      visualMode: 'legacy',
      legacyValue: pretty,
      operations: [createDefaultOperation()],
      jsonText: pretty,
      jsonError: '',
    }
  }

  return {
    editMode: 'json',
    visualMode: 'operations',
    legacyValue: '',
    operations: [createDefaultOperation()],
    jsonText: pretty,
    jsonError: '',
  }
}

// Build operations JSON

const buildOperationsJson = (
  sourceOperations: ParamOverrideOperation[],
  options: { validate: boolean },
  t: (key: string, options?: Record<string, unknown>) => string
): string => {
  const filteredOps = sourceOperations.filter((o) => !isOperationBlank(o))
  if (filteredOps.length === 0) return ''

  if (options.validate) {
    const message = validateOperations(filteredOps, t)
    if (message) throw new Error(message)
  }

  const payloadOps = filteredOps.map((operation) => {
    const mode = operation.mode || 'set'
    const meta = MODE_META[mode] || MODE_META.set
    const descriptionValue = String(operation.description || '').trim()
    const pathValue = operation.path.trim()
    const fromValue = operation.from.trim()
    const toValue = operation.to.trim()
    const payload: Record<string, unknown> = { mode }
    if (descriptionValue) payload.description = descriptionValue
    if (meta.path) payload.path = pathValue
    if (meta.pathOptional && pathValue) payload.path = pathValue
    if (meta.value) payload.value = parseLooseValue(operation.value_text)
    if (meta.keepOrigin && operation.keep_origin) payload.keep_origin = true
    if (meta.from) payload.from = fromValue
    if (!meta.to && operation.to.trim()) payload.to = toValue
    if (meta.to) payload.to = toValue
    if (meta.pathAlias) {
      if (!payload.from && pathValue) payload.from = pathValue
      if (!payload.to && pathValue) payload.to = pathValue
    }
    const conditions = operation.conditions
      .map(buildConditionPayload)
      .filter(Boolean)
    if (conditions.length > 0) {
      payload.conditions = conditions
      payload.logic = operation.logic === 'AND' ? 'AND' : 'OR'
    }
    return payload
  })

  return JSON.stringify({ operations: payloadOps }, null, 2)
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function ParamOverrideEditorDialog(
  props: ParamOverrideEditorDialogProps
) {
  const { t } = useTranslation()

  const [editMode, setEditMode] = useState<'visual' | 'json'>('visual')
  const [visualMode, setVisualMode] = useState<'operations' | 'legacy'>(
    'operations'
  )
  const [legacyValue, setLegacyValue] = useState('')
  const [operations, setOperations] = useState<ParamOverrideOperation[]>([
    createDefaultOperation(),
  ])
  const [jsonText, setJsonText] = useState('')
  const [jsonError, setJsonError] = useState('')
  const [operationSearch, setOperationSearch] = useState('')
  const [selectedOperationId, setSelectedOperationId] = useState('')
  const [expandedConditions, setExpandedConditions] = useState<
    Record<string, boolean>
  >({})
  const [draggedOperationId, setDraggedOperationId] = useState('')
  const [dragOverOperationId, setDragOverOperationId] = useState('')
  const [dragOverPosition, setDragOverPosition] = useState<'before' | 'after'>(
    'before'
  )
  const [templatePresetKey, setTemplatePresetKey] =
    useState('operations_default')

  // Initialize state when dialog opens
  useEffect(() => {
    if (!props.open) return
    const state = parseInitialState(props.value)
    setEditMode(state.editMode)
    setVisualMode(state.visualMode)
    setLegacyValue(state.legacyValue)
    setOperations(state.operations)
    setJsonText(state.jsonText)
    setJsonError(state.jsonError)
    setOperationSearch('')
    setSelectedOperationId(state.operations[0]?.id || '')
    setExpandedConditions({})
    setDraggedOperationId('')
    setDragOverOperationId('')
    setDragOverPosition('before')
    if (state.visualMode === 'legacy') {
      setTemplatePresetKey('legacy_default')
    } else {
      setTemplatePresetKey('operations_default')
    }
  }, [props.open, props.value])

  // Keep selectedOperationId valid
  useEffect(() => {
    if (operations.length === 0) {
      setSelectedOperationId('')
      return
    }
    if (!operations.some((o) => o.id === selectedOperationId)) {
      setSelectedOperationId(operations[0].id)
    }
  }, [operations, selectedOperationId])

  // Template preset options filtered by group
  const templatePresetOptions = useMemo(
    () =>
      Object.entries(TEMPLATE_PRESET_CONFIG).map(([value, config]) => ({
        value,
        label: config.label,
      })),
    []
  )

  const operationCount = useMemo(
    () => operations.filter((o) => !isOperationBlank(o)).length,
    [operations]
  )

  const filteredOperations = useMemo(() => {
    const keyword = operationSearch.trim().toLowerCase()
    if (!keyword) return operations
    return operations.filter((op) => {
      const searchableText = [
        op.description,
        op.mode,
        op.path,
        op.from,
        op.to,
        op.value_text,
      ]
        .filter(Boolean)
        .join(' ')
        .toLowerCase()
      return searchableText.includes(keyword)
    })
  }, [operationSearch, operations])

  const selectedOperation = useMemo(
    () => operations.find((o) => o.id === selectedOperationId),
    [operations, selectedOperationId]
  )

  const selectedOperationIndex = useMemo(
    () => operations.findIndex((o) => o.id === selectedOperationId),
    [operations, selectedOperationId]
  )

  const returnErrorDraft = useMemo(() => {
    if (!selectedOperation || selectedOperation.mode !== 'return_error') {
      return null
    }
    return parseReturnErrorDraft(selectedOperation.value_text)
  }, [selectedOperation])

  const pruneObjectsDraft = useMemo(() => {
    if (!selectedOperation || selectedOperation.mode !== 'prune_objects') {
      return null
    }
    return parsePruneObjectsDraft(selectedOperation.value_text)
  }, [selectedOperation])

  const topOperationModes = useMemo(() => {
    const counts: Record<string, number> = {}
    for (const op of operations) {
      const mode = op.mode || 'set'
      counts[mode] = (counts[mode] || 0) + 1
    }
    return Object.entries(counts)
      .sort((a, b) => b[1] - a[1])
      .slice(0, 4)
  }, [operations])

  // ---------------------------------------------------------------------------
  // Operations
  // ---------------------------------------------------------------------------

  const updateOperation = useCallback(
    (operationId: string, patch: Partial<ParamOverrideOperation>) => {
      setOperations((prev) =>
        prev.map((o) => (o.id === operationId ? { ...o, ...patch } : o))
      )
    },
    []
  )

  const addOperation = useCallback(() => {
    const created = createDefaultOperation()
    setOperations((prev) => [...prev, created])
    setSelectedOperationId(created.id)
  }, [])

  const duplicateOperation = useCallback((operationId: string) => {
    let insertedId = ''
    setOperations((prev) => {
      const idx = prev.findIndex((o) => o.id === operationId)
      if (idx < 0) return prev
      const source = prev[idx]
      const cloned = normalizeOperation({
        description: source.description,
        path: source.path,
        mode: source.mode,
        value: parseLooseValue(source.value_text),
        keep_origin: source.keep_origin,
        from: source.from,
        to: source.to,
        logic: source.logic,
        conditions: source.conditions.map((c) => ({
          path: c.path,
          mode: c.mode,
          value: parseLooseValue(c.value_text),
          invert: c.invert,
          pass_missing_key: c.pass_missing_key,
        })),
      })
      insertedId = cloned.id
      const next = [...prev]
      next.splice(idx + 1, 0, cloned)
      return next
    })
    if (insertedId) setSelectedOperationId(insertedId)
  }, [])

  const removeOperation = useCallback((operationId: string) => {
    setOperations((prev) => {
      if (prev.length <= 1) return [createDefaultOperation()]
      return prev.filter((o) => o.id !== operationId)
    })
  }, [])

  // Conditions
  const addCondition = useCallback((operationId: string) => {
    const created = createDefaultCondition()
    setOperations((prev) =>
      prev.map((op) =>
        op.id === operationId
          ? { ...op, conditions: [...op.conditions, created] }
          : op
      )
    )
    setExpandedConditions((prev) => ({ ...prev, [created.id]: true }))
  }, [])

  const updateCondition = useCallback(
    (
      operationId: string,
      conditionId: string,
      patch: Partial<ParamOverrideCondition>
    ) => {
      setOperations((prev) =>
        prev.map((op) =>
          op.id === operationId
            ? {
                ...op,
                conditions: op.conditions.map((c) =>
                  c.id === conditionId ? { ...c, ...patch } : c
                ),
              }
            : op
        )
      )
    },
    []
  )

  const removeCondition = useCallback(
    (operationId: string, conditionId: string) => {
      setOperations((prev) =>
        prev.map((op) =>
          op.id === operationId
            ? {
                ...op,
                conditions: op.conditions.filter((c) => c.id !== conditionId),
              }
            : op
        )
      )
    },
    []
  )

  // return_error draft
  const updateReturnErrorDraft = useCallback(
    (operationId: string, draftPatch: Partial<ReturnErrorDraft>) => {
      setOperations((prev) =>
        prev.map((op) => {
          if (op.id !== operationId) return op
          const draft = parseReturnErrorDraft(op.value_text)
          const nextDraft = { ...draft, ...draftPatch }
          return {
            ...op,
            value_text: buildReturnErrorValueText(nextDraft),
          }
        })
      )
    },
    []
  )

  // prune_objects draft
  const updatePruneObjectsDraft = useCallback(
    (
      operationId: string,
      updater:
        | Partial<PruneObjectsDraft>
        | ((draft: PruneObjectsDraft) => PruneObjectsDraft)
    ) => {
      setOperations((prev) =>
        prev.map((op) => {
          if (op.id !== operationId) return op
          const draft = parsePruneObjectsDraft(op.value_text)
          const nextDraft =
            typeof updater === 'function'
              ? updater(draft)
              : { ...draft, ...updater }
          return {
            ...op,
            value_text: buildPruneObjectsValueText(nextDraft),
          }
        })
      )
    },
    []
  )

  const addPruneRule = useCallback(
    (operationId: string) => {
      updatePruneObjectsDraft(operationId, (draft) => ({
        ...draft,
        simpleMode: false,
        rules: [...draft.rules, normalizePruneRule({})],
      }))
    },
    [updatePruneObjectsDraft]
  )

  const updatePruneRule = useCallback(
    (operationId: string, ruleId: string, patch: Partial<PruneRule>) => {
      updatePruneObjectsDraft(operationId, (draft) => ({
        ...draft,
        rules: draft.rules.map((r) =>
          r.id === ruleId ? { ...r, ...patch } : r
        ),
      }))
    },
    [updatePruneObjectsDraft]
  )

  const removePruneRule = useCallback(
    (operationId: string, ruleId: string) => {
      updatePruneObjectsDraft(operationId, (draft) => ({
        ...draft,
        rules: draft.rules.filter((r) => r.id !== ruleId),
      }))
    },
    [updatePruneObjectsDraft]
  )

  // Drag and drop
  const resetDragState = useCallback(() => {
    setDraggedOperationId('')
    setDragOverOperationId('')
    setDragOverPosition('before')
  }, [])

  const handleDragStart = useCallback(
    (event: DragEvent, operationId: string) => {
      setDraggedOperationId(operationId)
      setSelectedOperationId(operationId)
      event.dataTransfer.effectAllowed = 'move'
      event.dataTransfer.setData('text/plain', operationId)
    },
    []
  )

  const handleDragOver = useCallback(
    (event: DragEvent, operationId: string) => {
      event.preventDefault()
      if (!draggedOperationId || draggedOperationId === operationId) return
      const rect = event.currentTarget.getBoundingClientRect()
      const position: 'before' | 'after' =
        event.clientY - rect.top > rect.height / 2 ? 'after' : 'before'
      setDragOverOperationId(operationId)
      setDragOverPosition(position)
      event.dataTransfer.dropEffect = 'move'
    },
    [draggedOperationId]
  )

  const handleDrop = useCallback(
    (event: DragEvent, operationId: string) => {
      event.preventDefault()
      const sourceId =
        draggedOperationId || event.dataTransfer.getData('text/plain')
      const position =
        dragOverOperationId === operationId ? dragOverPosition : 'before'
      if (sourceId && operationId && sourceId !== operationId) {
        setOperations((prev) =>
          reorderOperations(prev, sourceId, operationId, position)
        )
        setSelectedOperationId(sourceId)
      }
      resetDragState()
    },
    [draggedOperationId, dragOverOperationId, dragOverPosition, resetDragState]
  )

  // ---------------------------------------------------------------------------
  // Mode switching & templates
  // ---------------------------------------------------------------------------

  const buildVisualJson = useCallback((): string => {
    if (visualMode === 'legacy') {
      const trimmed = legacyValue.trim()
      if (!trimmed) return ''
      if (!verifyJSON(trimmed)) {
        throw new Error(t('Parameter override must be valid JSON format'))
      }
      const parsed = JSON.parse(trimmed) as unknown
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
        throw new Error(t('Legacy format must be a JSON object'))
      }
      return JSON.stringify(parsed, null, 2)
    }
    return buildOperationsJson(operations, { validate: true }, t)
  }, [legacyValue, operations, t, visualMode])

  const switchToJsonMode = useCallback(() => {
    if (editMode === 'json') return
    try {
      setJsonText(buildVisualJson())
      setJsonError('')
    } catch (error) {
      toast.error((error as Error).message)
      if (visualMode === 'legacy') {
        setJsonText(legacyValue)
      } else {
        setJsonText(buildOperationsJson(operations, { validate: false }, t))
      }
      setJsonError(
        (error as Error).message || t('Parameter configuration error')
      )
    }
    setEditMode('json')
  }, [buildVisualJson, editMode, legacyValue, operations, t, visualMode])

  const switchToVisualMode = useCallback(() => {
    if (editMode === 'visual') return
    const trimmed = jsonText.trim()
    if (!trimmed) {
      const fallback = createDefaultOperation()
      setVisualMode('operations')
      setOperations([fallback])
      setSelectedOperationId(fallback.id)
      setLegacyValue('')
      setJsonError('')
      setEditMode('visual')
      return
    }
    if (!verifyJSON(trimmed)) {
      toast.error(t('Parameter override must be valid JSON format'))
      return
    }
    const parsed = JSON.parse(trimmed) as Record<string, unknown>
    if (
      parsed &&
      typeof parsed === 'object' &&
      !Array.isArray(parsed) &&
      Array.isArray(parsed.operations)
    ) {
      const nextOps =
        (parsed.operations as Record<string, unknown>[]).length > 0
          ? (parsed.operations as Record<string, unknown>[]).map(
              normalizeOperation
            )
          : [createDefaultOperation()]
      setVisualMode('operations')
      setOperations(nextOps)
      setSelectedOperationId(nextOps[0]?.id || '')
      setLegacyValue('')
      setJsonError('')
      setEditMode('visual')
      setTemplatePresetKey('operations_default')
      return
    }
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
      const fallback = createDefaultOperation()
      setVisualMode('legacy')
      setLegacyValue(JSON.stringify(parsed, null, 2))
      setOperations([fallback])
      setSelectedOperationId(fallback.id)
      setJsonError('')
      setEditMode('visual')
      setTemplatePresetKey('legacy_default')
      return
    }
    toast.error(t('Parameter override must be a valid JSON object'))
  }, [editMode, jsonText, t])

  const fillTemplate = useCallback(
    (mode: 'fill' | 'append') => {
      const preset =
        TEMPLATE_PRESET_CONFIG[templatePresetKey] ||
        TEMPLATE_PRESET_CONFIG.operations_default
      const payload = preset.payload as Record<string, unknown>

      if (preset.kind === 'legacy') {
        if (mode === 'append' && visualMode === 'legacy') {
          const trimmed = legacyValue.trim()
          let parsedCurrent: Record<string, unknown> = {}
          if (trimmed) {
            if (!verifyJSON(trimmed)) {
              toast.error(t('Current legacy JSON is invalid, cannot append'))
              return
            }
            parsedCurrent = JSON.parse(trimmed) as Record<string, unknown>
          }
          const merged = { ...payload, ...parsedCurrent }
          const text = JSON.stringify(merged, null, 2)
          setVisualMode('legacy')
          setLegacyValue(text)
          setOperations([createDefaultOperation()])
          setJsonText(text)
          setJsonError('')
          setEditMode('visual')
        } else {
          const text = JSON.stringify(payload, null, 2)
          setVisualMode('legacy')
          setLegacyValue(text)
          setOperations([createDefaultOperation()])
          setJsonText(text)
          setJsonError('')
          setEditMode('visual')
        }
        return
      }

      const operationsPayload = ((payload as Record<string, unknown>)
        .operations || []) as Record<string, unknown>[]

      if (mode === 'append') {
        const appended = operationsPayload.map(normalizeOperation)
        const existing =
          visualMode === 'operations'
            ? operations.filter((o) => !isOperationBlank(o))
            : []
        const nextOps = [...existing, ...appended]
        setVisualMode('operations')
        setOperations(nextOps.length > 0 ? nextOps : appended)
        setSelectedOperationId(nextOps[0]?.id || appended[0]?.id || '')
        setLegacyValue('')
        setJsonError('')
        setEditMode('visual')
        setJsonText('')
      } else {
        const nextOps = operationsPayload.map(normalizeOperation)
        const finalOps =
          nextOps.length > 0 ? nextOps : [createDefaultOperation()]
        setVisualMode('operations')
        setOperations(finalOps)
        setSelectedOperationId(finalOps[0]?.id || '')
        setJsonText(JSON.stringify({ operations: operationsPayload }, null, 2))
        setJsonError('')
        setEditMode('visual')
      }
    },
    [legacyValue, operations, templatePresetKey, visualMode, t]
  )

  const resetEditorState = useCallback(() => {
    const fallback = createDefaultOperation()
    setVisualMode('operations')
    setLegacyValue('')
    setOperations([fallback])
    setSelectedOperationId(fallback.id)
    setJsonText('')
    setJsonError('')
    setTemplatePresetKey('operations_default')
    setEditMode('visual')
  }, [])

  // JSON mode
  const handleJsonChange = useCallback(
    (nextValue: string) => {
      setJsonText(nextValue)
      const trimmed = nextValue.trim()
      if (!trimmed) {
        setJsonError('')
        return
      }
      setJsonError(verifyJSON(trimmed) ? '' : t('JSON format error'))
    },
    [t]
  )

  const visualValidationError = useMemo(() => {
    if (editMode !== 'visual') return ''
    try {
      buildVisualJson()
      return ''
    } catch (error) {
      return (error as Error)?.message || t('Parameter configuration error')
    }
  }, [buildVisualJson, editMode, t])

  // Save
  const handleSave = useCallback(() => {
    try {
      let result = ''
      if (editMode === 'json') {
        const trimmed = jsonText.trim()
        if (trimmed) {
          if (!verifyJSON(trimmed)) {
            throw new Error(t('Parameter override must be valid JSON format'))
          }
          result = JSON.stringify(JSON.parse(trimmed), null, 2)
        }
      } else {
        result = buildVisualJson()
      }
      props.onSave(result)
      props.onOpenChange(false)
    } catch (error) {
      toast.error((error as Error).message)
    }
  }, [buildVisualJson, editMode, jsonText, props, t])

  // Expand/collapse all conditions
  const expandAllConditions = useCallback(() => {
    if (!selectedOperation) return
    const map: Record<string, boolean> = {}
    for (const c of selectedOperation.conditions) map[c.id] = true
    setExpandedConditions((prev) => ({ ...prev, ...map }))
  }, [selectedOperation])

  const collapseAllConditions = useCallback(() => {
    if (!selectedOperation) return
    const map: Record<string, boolean> = {}
    for (const c of selectedOperation.conditions) map[c.id] = false
    setExpandedConditions((prev) => ({ ...prev, ...map }))
  }, [selectedOperation])

  // ---------------------------------------------------------------------------
  // Render
  // ---------------------------------------------------------------------------

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('Parameter Override')}
      description={t(
        'Create request parameter override rules with a visual editor or raw JSON.'
      )}
      contentClassName='flex max-h-[min(90dvh,var(--dialog-available-height))] flex-col gap-0 p-0 sm:max-w-5xl'
      headerClassName='border-b px-6 py-4'
      footerClassName='border-t px-6 py-4'
      contentHeight='min(72vh, 720px)'
      bodyClassName='space-y-4'
      footer={
        <>
          <Button
            type='button'
            variant='outline'
            onClick={() => props.onOpenChange(false)}
          >
            {t('Cancel')}
          </Button>
          <Button type='button' onClick={handleSave}>
            {t('Save')}
          </Button>
        </>
      }
    >
      {/* Toolbar */}
      <div className='bg-muted/30 border-b px-4 py-3'>
        <div className='flex flex-wrap items-center gap-2'>
          <span className='text-muted-foreground text-xs font-medium'>
            {t('Mode')}
          </span>
          <Button
            type='button'
            variant={editMode === 'visual' ? 'default' : 'outline'}
            size='sm'
            onClick={switchToVisualMode}
          >
            {t('Visual')}
          </Button>
          <Button
            type='button'
            variant={editMode === 'json' ? 'default' : 'outline'}
            size='sm'
            onClick={switchToJsonMode}
          >
            {t('JSON Text')}
          </Button>

          <div className='bg-border mx-1 h-5 w-px' />

          <span className='text-muted-foreground text-xs font-medium'>
            {t('Template')}
          </span>
          <Combobox
            options={templatePresetOptions.map((o) => ({
              value: o.value,
              label: t(o.label),
            }))}
            value={templatePresetKey}
            onValueChange={(v) =>
              setTemplatePresetKey(v || 'operations_default')
            }
            className='h-8 w-[220px]'
          />
          <Button
            type='button'
            variant='outline'
            size='sm'
            onClick={() => fillTemplate('fill')}
          >
            {t('Fill Template')}
          </Button>
          <Button
            type='button'
            variant='ghost'
            size='sm'
            onClick={() => fillTemplate('append')}
          >
            {t('Append Template')}
          </Button>
          <Button
            type='button'
            variant='ghost'
            size='sm'
            onClick={resetEditorState}
          >
            {t('Reset')}
          </Button>
        </div>
      </div>
      {/* Content */}
      <div className='min-h-0 flex-1 overflow-hidden'>
        {editMode === 'visual' &&
          (visualMode === 'legacy' ? (
            <div className='p-4'>
              <p className='text-muted-foreground mb-2 text-sm'>
                {t('Legacy Format (JSON Object)')}
              </p>
              <JsonCodeEditor
                value={legacyValue}
                onChange={setLegacyValue}
                placeholder={JSON.stringify(LEGACY_TEMPLATE, null, 2)}
                heightClassName='h-72 min-h-72 max-h-72'
                ariaLabel={t('Legacy Format (JSON Object)')}
              />
              <p className='text-muted-foreground mt-2 text-xs'>
                {t(
                  'Edit JSON object directly. Suitable for simple parameter overrides.'
                )}
              </p>
            </div>
          ) : (
            <div className='flex h-full'>
              {/* Left sidebar */}
              <div className='flex w-[280px] flex-shrink-0 flex-col border-r'>
                <div className='flex items-center justify-between border-b px-3 py-2'>
                  <div className='flex items-center gap-2'>
                    <span className='text-sm font-medium'>{t('Rules')}</span>
                    <Badge variant='secondary'>
                      {operationCount}/{operations.length}
                    </Badge>
                  </div>
                  <Button
                    type='button'
                    variant='ghost'
                    size='sm'
                    onClick={addOperation}
                  >
                    <Plus className='h-4 w-4' />
                  </Button>
                </div>

                {topOperationModes.length > 0 && (
                  <div className='flex flex-wrap gap-1 border-b px-3 py-2'>
                    {topOperationModes.map(([mode, count]) => (
                      <span
                        key={`mode_stat_${mode}`}
                        className={cn(
                          'inline-flex items-center rounded-md border px-1.5 py-0.5 text-[10px] font-medium',
                          getModeTagTailwind(mode)
                        )}
                      >
                        {t(OPERATION_MODE_LABEL_MAP[mode] || mode)} · {count}
                      </span>
                    ))}
                  </div>
                )}

                <div className='px-3 py-2'>
                  <div className='relative'>
                    <Search className='text-muted-foreground absolute top-2.5 left-2.5 h-3.5 w-3.5' />
                    <Input
                      value={operationSearch}
                      onChange={(e) => setOperationSearch(e.target.value)}
                      placeholder={t('Search rules...')}
                      className='h-8 pl-8 text-xs'
                    />
                  </div>
                </div>

                <ScrollArea className='flex-1'>
                  <div className='flex flex-col gap-1 px-3 pb-3'>
                    {filteredOperations.length === 0 ? (
                      <p className='text-muted-foreground py-4 text-center text-xs'>
                        {t('No matching rules')}
                      </p>
                    ) : (
                      filteredOperations.map((operation) => {
                        const index = operations.findIndex(
                          (o) => o.id === operation.id
                        )
                        const isActive = operation.id === selectedOperationId
                        const isDragging = operation.id === draggedOperationId
                        const isDropTarget =
                          operation.id === dragOverOperationId &&
                          draggedOperationId !== '' &&
                          draggedOperationId !== operation.id
                        return (
                          <div
                            key={operation.id}
                            role='button'
                            tabIndex={0}
                            draggable={operations.length > 1}
                            onClick={() => setSelectedOperationId(operation.id)}
                            onDragStart={(e) =>
                              handleDragStart(e, operation.id)
                            }
                            onDragOver={(e) => handleDragOver(e, operation.id)}
                            onDrop={(e) => handleDrop(e, operation.id)}
                            onDragEnd={resetDragState}
                            onKeyDown={(e: KeyboardEvent) => {
                              if (e.key === 'Enter' || e.key === ' ') {
                                e.preventDefault()
                                setSelectedOperationId(operation.id)
                              }
                            }}
                            className={cn(
                              'cursor-pointer rounded-lg border p-2.5 transition-colors',
                              isActive
                                ? 'border-primary bg-primary/5'
                                : 'hover:bg-muted/50',
                              isDragging && 'opacity-50',
                              isDropTarget &&
                                dragOverPosition === 'before' &&
                                'border-t-primary border-t-2',
                              isDropTarget &&
                                dragOverPosition === 'after' &&
                                'border-b-primary border-b-2'
                            )}
                          >
                            <div className='flex items-start gap-2'>
                              <GripVertical
                                className={cn(
                                  'text-muted-foreground mt-0.5 h-3.5 w-3.5 flex-shrink-0',
                                  operations.length > 1
                                    ? 'cursor-grab'
                                    : 'cursor-default'
                                )}
                              />
                              <div className='min-w-0 flex-1'>
                                <div className='flex items-center justify-between gap-1'>
                                  <span className='text-xs font-semibold'>
                                    #{index + 1}
                                  </span>
                                  <Badge
                                    variant='outline'
                                    className='text-[10px]'
                                  >
                                    {operation.conditions.length}
                                  </Badge>
                                </div>
                                <p className='text-muted-foreground mt-0.5 line-clamp-1 text-[11px]'>
                                  {getOperationSummary(operation, index)}
                                </p>
                                {operation.description.trim() && (
                                  <p className='text-muted-foreground mt-0.5 line-clamp-2 text-[10px]'>
                                    {operation.description}
                                  </p>
                                )}
                                <span
                                  className={cn(
                                    'mt-1 inline-flex items-center rounded-md border px-1.5 py-0.5 text-[10px] font-medium',
                                    getModeTagTailwind(operation.mode || 'set')
                                  )}
                                >
                                  {t(
                                    OPERATION_MODE_LABEL_MAP[
                                      operation.mode || 'set'
                                    ] ||
                                      operation.mode ||
                                      'set'
                                  )}
                                </span>
                              </div>
                            </div>
                          </div>
                        )
                      })
                    )}
                  </div>
                </ScrollArea>
              </div>

              {/* Right panel - Rule editor */}
              <div className='flex min-w-0 flex-1 flex-col overflow-y-auto'>
                {selectedOperation ? (
                  <RuleEditor
                    operation={selectedOperation}
                    operationIndex={selectedOperationIndex}
                    operations={operations}
                    returnErrorDraft={returnErrorDraft}
                    pruneObjectsDraft={pruneObjectsDraft}
                    expandedConditions={expandedConditions}
                    setExpandedConditions={setExpandedConditions}
                    updateOperation={updateOperation}
                    duplicateOperation={duplicateOperation}
                    removeOperation={removeOperation}
                    addCondition={addCondition}
                    updateCondition={updateCondition}
                    removeCondition={removeCondition}
                    updateReturnErrorDraft={updateReturnErrorDraft}
                    updatePruneObjectsDraft={updatePruneObjectsDraft}
                    addPruneRule={addPruneRule}
                    updatePruneRule={updatePruneRule}
                    removePruneRule={removePruneRule}
                    expandAllConditions={expandAllConditions}
                    collapseAllConditions={collapseAllConditions}
                  />
                ) : (
                  <div className='flex flex-1 items-center justify-center'>
                    <p className='text-muted-foreground text-sm'>
                      {t('Select a rule to edit.')}
                    </p>
                  </div>
                )}

                {visualValidationError && (
                  <div className='border-t px-4 py-2'>
                    <p className='text-destructive text-xs'>
                      {visualValidationError}
                    </p>
                  </div>
                )}
              </div>
            </div>
          ))}
        {/* JSON mode */}
        {editMode !== 'visual' && (
          <div className='p-4'>
            <div className='mb-2 flex items-center gap-2'>
              <span className='text-muted-foreground text-xs'>
                {t('Advanced text editing')}
              </span>
            </div>
            <JsonCodeEditor
              value={jsonText}
              onChange={handleJsonChange}
              placeholder={JSON.stringify(OPERATION_TEMPLATE, null, 2)}
              heightClassName='h-[420px] min-h-[420px] max-h-[420px]'
              aria-invalid={Boolean(jsonError)}
              ariaLabel={t('Advanced text editing')}
            />
            <p className='text-muted-foreground mt-2 text-xs'>
              {t('Edit JSON text directly. Format will be validated on save.')}
            </p>
            {jsonError && (
              <p className='text-destructive mt-1 text-xs'>{jsonError}</p>
            )}
          </div>
        )}
      </div>
      {/* Footer */}
    </Dialog>
  )
}

// ---------------------------------------------------------------------------
// RuleEditor sub-component
// ---------------------------------------------------------------------------

type RuleEditorProps = {
  operation: ParamOverrideOperation
  operationIndex: number
  operations: ParamOverrideOperation[]
  returnErrorDraft: ReturnErrorDraft | null
  pruneObjectsDraft: PruneObjectsDraft | null
  expandedConditions: Record<string, boolean>
  setExpandedConditions: React.Dispatch<
    React.SetStateAction<Record<string, boolean>>
  >
  updateOperation: (
    operationId: string,
    patch: Partial<ParamOverrideOperation>
  ) => void
  duplicateOperation: (operationId: string) => void
  removeOperation: (operationId: string) => void
  addCondition: (operationId: string) => void
  updateCondition: (
    operationId: string,
    conditionId: string,
    patch: Partial<ParamOverrideCondition>
  ) => void
  removeCondition: (operationId: string, conditionId: string) => void
  updateReturnErrorDraft: (
    operationId: string,
    draftPatch: Partial<ReturnErrorDraft>
  ) => void
  updatePruneObjectsDraft: (
    operationId: string,
    updater:
      | Partial<PruneObjectsDraft>
      | ((draft: PruneObjectsDraft) => PruneObjectsDraft)
  ) => void
  addPruneRule: (operationId: string) => void
  updatePruneRule: (
    operationId: string,
    ruleId: string,
    patch: Partial<PruneRule>
  ) => void
  removePruneRule: (operationId: string, ruleId: string) => void
  expandAllConditions: () => void
  collapseAllConditions: () => void
}

function RuleEditor(ruleEditorProps: RuleEditorProps) {
  const { t } = useTranslation()
  const operation = ruleEditorProps.operation
  const mode = operation.mode || 'set'
  const returnErrorDraft =
    mode === 'return_error' ? ruleEditorProps.returnErrorDraft : null
  const pruneObjectsDraft =
    mode === 'prune_objects' ? ruleEditorProps.pruneObjectsDraft : null
  const meta = MODE_META[mode] || MODE_META.set
  const conditions = operation.conditions
  const syncFromTarget =
    mode === 'sync_fields' ? parseSyncTargetSpec(operation.from) : null
  const syncToTarget =
    mode === 'sync_fields' ? parseSyncTargetSpec(operation.to) : null

  return (
    <ScrollArea className='flex-1'>
      <div className='space-y-4 p-4'>
        {/* Header */}
        <div className='flex items-center justify-between'>
          <div className='flex items-center gap-2'>
            <Badge variant='outline'>
              #{ruleEditorProps.operationIndex + 1}
            </Badge>
            <span className='text-muted-foreground line-clamp-1 text-xs'>
              {getOperationSummary(operation, ruleEditorProps.operationIndex)}
            </span>
          </div>
          <div className='flex items-center gap-1'>
            <Button
              type='button'
              variant='ghost'
              size='sm'
              onClick={() => ruleEditorProps.duplicateOperation(operation.id)}
            >
              <Copy className='mr-1 h-3.5 w-3.5' />
              {t('Duplicate')}
            </Button>
            <Button
              type='button'
              variant='ghost'
              size='sm'
              className='text-destructive hover:text-destructive'
              onClick={() => ruleEditorProps.removeOperation(operation.id)}
            >
              <Trash2 className='mr-1 h-3.5 w-3.5' />
              {t('Delete')}
            </Button>
          </div>
        </div>

        {/* Operation type + path */}
        <div className='grid gap-3 sm:grid-cols-2'>
          <div className='space-y-1.5'>
            <label className='text-xs font-medium'>{t('Operation Type')}</label>
            <Combobox
              options={OPERATION_MODE_OPTIONS.map((o) => ({
                value: o.value,
                label: t(o.label),
              }))}
              value={mode}
              onValueChange={(nextMode) =>
                nextMode !== null &&
                ruleEditorProps.updateOperation(operation.id, {
                  mode: nextMode,
                })
              }
              className='h-9'
            />
          </div>
          {(meta.path || meta.pathOptional) && (
            <div className='space-y-1.5'>
              <label className='text-xs font-medium'>
                {t(getModePathLabel(mode))}
              </label>
              <Input
                value={operation.path}
                onChange={(e) =>
                  ruleEditorProps.updateOperation(operation.id, {
                    path: e.target.value,
                  })
                }
                placeholder={getModePathPlaceholder(mode)}
                className='h-9'
              />
            </div>
          )}
        </div>

        {/* Mode description */}
        {MODE_DESCRIPTIONS[mode] && (
          <p className='text-muted-foreground text-xs'>
            {t(MODE_DESCRIPTIONS[mode])}
          </p>
        )}

        {/* Description */}
        <div className='space-y-1.5'>
          <div className='flex items-center justify-between'>
            <label className='text-xs font-medium'>
              {t('Rule Description (optional)')}
            </label>
            <span className='text-muted-foreground text-[10px]'>
              {operation.description.length}/180
            </span>
          </div>
          <Input
            value={operation.description}
            onChange={(e) =>
              ruleEditorProps.updateOperation(operation.id, {
                description: e.target.value,
              })
            }
            placeholder={t(
              'e.g. Clean tool parameters to avoid upstream validation errors'
            )}
            maxLength={180}
            className='h-9'
          />
        </div>

        {/* Value section */}
        {meta.value && (
          <>
            {returnErrorDraft && (
              <ReturnErrorEditor
                operationId={operation.id}
                draft={returnErrorDraft}
                updateDraft={ruleEditorProps.updateReturnErrorDraft}
              />
            )}
            {pruneObjectsDraft && (
              <PruneObjectsEditor
                operationId={operation.id}
                draft={pruneObjectsDraft}
                updateDraft={ruleEditorProps.updatePruneObjectsDraft}
                addRule={ruleEditorProps.addPruneRule}
                updateRule={ruleEditorProps.updatePruneRule}
                removeRule={ruleEditorProps.removePruneRule}
              />
            )}
            {!returnErrorDraft && !pruneObjectsDraft && (
              <div className='space-y-1.5'>
                <div className='flex items-center justify-between'>
                  <label className='text-xs font-medium'>
                    {t(getModeValueLabel(mode))}
                  </label>
                  {operation.value_text.trim().startsWith('{') && (
                    <Button
                      type='button'
                      variant='ghost'
                      size='sm'
                      className='text-muted-foreground h-auto px-1.5 py-0.5 text-xs'
                      onClick={() => {
                        try {
                          const parsed = JSON.parse(operation.value_text)
                          ruleEditorProps.updateOperation(operation.id, {
                            value_text: JSON.stringify(parsed, null, 2),
                          })
                        } catch {
                          /* not valid JSON */
                        }
                      }}
                    >
                      {t('Format')}
                    </Button>
                  )}
                </div>
                <Textarea
                  value={operation.value_text}
                  onChange={(e) =>
                    ruleEditorProps.updateOperation(operation.id, {
                      value_text: e.target.value,
                    })
                  }
                  placeholder={getModeValuePlaceholder(mode)}
                  rows={3}
                  className='max-h-[200px] resize-y overflow-y-auto font-mono text-xs'
                />
              </div>
            )}
          </>
        )}

        {/* keep_origin */}
        {meta.keepOrigin && (
          <div className='flex items-center justify-between rounded-lg border px-3 py-2'>
            <p className='text-sm font-medium'>
              {t('Keep original value (skip if target exists)')}
            </p>
            <Switch
              checked={operation.keep_origin}
              onCheckedChange={(checked) =>
                ruleEditorProps.updateOperation(operation.id, {
                  keep_origin: checked,
                })
              }
            />
          </div>
        )}

        {/* sync_fields */}
        {mode === 'sync_fields' && syncFromTarget && syncToTarget && (
          <SyncFieldsEditor
            operationId={operation.id}
            syncFromTarget={syncFromTarget}
            syncToTarget={syncToTarget}
            updateOperation={ruleEditorProps.updateOperation}
          />
        )}
        {(meta.from || meta.to !== undefined) && mode !== 'sync_fields' && (
          <div className='grid gap-3 sm:grid-cols-2'>
            {(meta.from || meta.to === false) && (
              <div className='space-y-1.5'>
                <label className='text-xs font-medium'>
                  {t(getModeFromLabel(mode))}
                </label>
                <Input
                  value={operation.from}
                  onChange={(e) =>
                    ruleEditorProps.updateOperation(operation.id, {
                      from: e.target.value,
                    })
                  }
                  placeholder={getModeFromPlaceholder(mode)}
                  className='h-9'
                />
              </div>
            )}
            {(meta.to || meta.to === false) && (
              <div className='space-y-1.5'>
                <label className='text-xs font-medium'>
                  {t(getModeToLabel(mode))}
                </label>
                <Input
                  value={operation.to}
                  onChange={(e) =>
                    ruleEditorProps.updateOperation(operation.id, {
                      to: e.target.value,
                    })
                  }
                  placeholder={getModeToPlaceholder(mode)}
                  className='h-9'
                />
              </div>
            )}
          </div>
        )}

        {/* Conditions */}
        <div className='rounded-lg border p-3'>
          <div className='mb-2 flex items-center justify-between'>
            <div className='flex items-center gap-2'>
              <span className='text-sm font-medium'>{t('Conditions')}</span>
              <Select
                items={[
                  { value: 'OR', label: t('Match Any (OR)') },
                  { value: 'AND', label: t('Match All (AND)') },
                ]}
                value={operation.logic || 'OR'}
                onValueChange={(v) =>
                  v !== null &&
                  ruleEditorProps.updateOperation(operation.id, {
                    logic: v,
                  })
                }
              >
                <SelectTrigger className='h-7 w-[120px] text-xs'>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent alignItemWithTrigger={false}>
                  <SelectGroup>
                    <SelectItem value='OR'>{t('Match Any (OR)')}</SelectItem>
                    <SelectItem value='AND'>{t('Match All (AND)')}</SelectItem>
                  </SelectGroup>
                </SelectContent>
              </Select>
            </div>
            <div className='flex items-center gap-1'>
              {conditions.length > 0 && (
                <>
                  <Button
                    type='button'
                    variant='ghost'
                    size='sm'
                    className='h-7 text-xs'
                    onClick={ruleEditorProps.expandAllConditions}
                  >
                    <ChevronDown className='mr-1 h-3 w-3' />
                    {t('Expand All')}
                  </Button>
                  <Button
                    type='button'
                    variant='ghost'
                    size='sm'
                    className='h-7 text-xs'
                    onClick={ruleEditorProps.collapseAllConditions}
                  >
                    <ChevronUp className='mr-1 h-3 w-3' />
                    {t('Collapse All')}
                  </Button>
                </>
              )}
              <Button
                type='button'
                variant='outline'
                size='sm'
                className='h-7 text-xs'
                onClick={() => ruleEditorProps.addCondition(operation.id)}
              >
                <Plus className='mr-1 h-3 w-3' />
                {t('Add Condition')}
              </Button>
            </div>
          </div>

          {conditions.length === 0 ? (
            <p className='text-muted-foreground text-xs'>
              {t('When no conditions are set, the operation always executes.')}
            </p>
          ) : (
            <div className='space-y-2'>
              {conditions.map((condition, conditionIndex) => (
                <ConditionEditor
                  key={condition.id}
                  condition={condition}
                  conditionIndex={conditionIndex}
                  operationId={operation.id}
                  expanded={
                    ruleEditorProps.expandedConditions[condition.id] ?? false
                  }
                  onExpandedChange={(expanded) =>
                    ruleEditorProps.setExpandedConditions((prev) => ({
                      ...prev,
                      [condition.id]: expanded,
                    }))
                  }
                  updateCondition={ruleEditorProps.updateCondition}
                  removeCondition={ruleEditorProps.removeCondition}
                />
              ))}
            </div>
          )}
        </div>
      </div>
    </ScrollArea>
  )
}

// ---------------------------------------------------------------------------
// ConditionEditor
// ---------------------------------------------------------------------------

type ConditionEditorProps = {
  condition: ParamOverrideCondition
  conditionIndex: number
  operationId: string
  expanded: boolean
  onExpandedChange: (expanded: boolean) => void
  updateCondition: (
    operationId: string,
    conditionId: string,
    patch: Partial<ParamOverrideCondition>
  ) => void
  removeCondition: (operationId: string, conditionId: string) => void
}

function ConditionEditor(conditionEditorProps: ConditionEditorProps) {
  const { t } = useTranslation()
  const condition = conditionEditorProps.condition

  return (
    <Collapsible
      open={conditionEditorProps.expanded}
      onOpenChange={conditionEditorProps.onExpandedChange}
    >
      <div className='rounded-md border'>
        <CollapsibleTrigger className='hover:bg-muted/50 flex w-full items-center justify-between px-3 py-2'>
          <div className='flex items-center gap-2'>
            <Badge variant='outline' className='text-[10px]'>
              C{conditionEditorProps.conditionIndex + 1}
            </Badge>
            <span className='text-muted-foreground text-xs'>
              {condition.path || t('Path not set')}
            </span>
          </div>
          {conditionEditorProps.expanded ? (
            <ChevronUp className='text-muted-foreground h-3.5 w-3.5' />
          ) : (
            <ChevronDown className='text-muted-foreground h-3.5 w-3.5' />
          )}
        </CollapsibleTrigger>
        <CollapsibleContent>
          <div className='space-y-3 border-t px-3 py-3'>
            <div className='flex items-center justify-between'>
              <span className='text-muted-foreground text-xs'>
                {t('Condition Settings')}
              </span>
              <Button
                type='button'
                variant='ghost'
                size='sm'
                className='text-destructive hover:text-destructive h-7 text-xs'
                onClick={() =>
                  conditionEditorProps.removeCondition(
                    conditionEditorProps.operationId,
                    condition.id
                  )
                }
              >
                <Trash2 className='mr-1 h-3 w-3' />
                {t('Delete Condition')}
              </Button>
            </div>
            <div className='grid gap-2 sm:grid-cols-3'>
              <div className='space-y-1'>
                <label className='text-[10px] font-medium'>
                  {t('Field Path')}
                </label>
                <Input
                  value={condition.path}
                  onChange={(e) =>
                    conditionEditorProps.updateCondition(
                      conditionEditorProps.operationId,
                      condition.id,
                      { path: e.target.value }
                    )
                  }
                  placeholder='model'
                  className='h-8 text-xs'
                />
              </div>
              <div className='space-y-1'>
                <label className='text-[10px] font-medium'>
                  {t('Match Mode')}
                </label>
                <Combobox
                  options={CONDITION_MODE_OPTIONS.map((o) => ({
                    value: o.value,
                    label: t(o.label),
                  }))}
                  value={condition.mode}
                  onValueChange={(v) =>
                    v !== null &&
                    conditionEditorProps.updateCondition(
                      conditionEditorProps.operationId,
                      condition.id,
                      { mode: v }
                    )
                  }
                  className='h-8 text-xs'
                />
              </div>
              <div className='space-y-1'>
                <label className='text-[10px] font-medium'>
                  {t('Match Value')}
                </label>
                <Input
                  value={condition.value_text}
                  onChange={(e) =>
                    conditionEditorProps.updateCondition(
                      conditionEditorProps.operationId,
                      condition.id,
                      { value_text: e.target.value }
                    )
                  }
                  placeholder='gpt'
                  className='h-8 text-xs'
                />
              </div>
            </div>
            <div className='flex flex-wrap gap-4'>
              <label className='flex items-center gap-2 text-xs'>
                <Switch
                  checked={condition.invert}
                  onCheckedChange={(checked) =>
                    conditionEditorProps.updateCondition(
                      conditionEditorProps.operationId,
                      condition.id,
                      { invert: checked }
                    )
                  }
                />
                {t('Invert match')}
              </label>
              <label className='flex items-center gap-2 text-xs'>
                <Switch
                  checked={condition.pass_missing_key}
                  onCheckedChange={(checked) =>
                    conditionEditorProps.updateCondition(
                      conditionEditorProps.operationId,
                      condition.id,
                      { pass_missing_key: checked }
                    )
                  }
                />
                {t('Pass when key is missing')}
              </label>
            </div>
          </div>
        </CollapsibleContent>
      </div>
    </Collapsible>
  )
}

// ---------------------------------------------------------------------------
// ReturnErrorEditor
// ---------------------------------------------------------------------------

type ReturnErrorEditorProps = {
  operationId: string
  draft: ReturnErrorDraft
  updateDraft: (
    operationId: string,
    draftPatch: Partial<ReturnErrorDraft>
  ) => void
}

function ReturnErrorEditor(returnErrorEditorProps: ReturnErrorEditorProps) {
  const { t } = useTranslation()
  const draft = returnErrorEditorProps.draft

  return (
    <div className='rounded-lg border p-3'>
      <div className='mb-2 flex items-center justify-between'>
        <span className='text-sm font-medium'>
          {t('Custom Error Response')}
        </span>
        <div className='flex items-center gap-1'>
          <span className='text-muted-foreground text-xs'>{t('Mode')}</span>
          <Button
            type='button'
            variant={draft.simpleMode ? 'default' : 'outline'}
            size='sm'
            className='h-7 text-xs'
            onClick={() =>
              returnErrorEditorProps.updateDraft(
                returnErrorEditorProps.operationId,
                { simpleMode: true }
              )
            }
          >
            {t('Simple')}
          </Button>
          <Button
            type='button'
            variant={draft.simpleMode ? 'outline' : 'default'}
            size='sm'
            className='h-7 text-xs'
            onClick={() =>
              returnErrorEditorProps.updateDraft(
                returnErrorEditorProps.operationId,
                { simpleMode: false }
              )
            }
          >
            {t('Advanced')}
          </Button>
        </div>
      </div>

      <div className='space-y-1.5'>
        <Label required className='text-xs font-medium'>
          {t('Error Message')}
        </Label>
        <Textarea
          value={draft.message}
          onChange={(e) =>
            returnErrorEditorProps.updateDraft(
              returnErrorEditorProps.operationId,
              { message: e.target.value }
            )
          }
          placeholder={t('e.g. This request does not meet access policy')}
          rows={2}
          className='text-xs'
        />
      </div>

      {draft.simpleMode ? (
        <p className='text-muted-foreground mt-2 text-xs'>
          {t(
            'Simple mode only returns message; status code and error type use system defaults.'
          )}
        </p>
      ) : (
        <>
          <div className='mt-3 grid gap-3 sm:grid-cols-3'>
            <div className='space-y-1'>
              <label className='text-xs font-medium'>{t('Status Code')}</label>
              <Input
                value={String(draft.statusCode ?? '')}
                onChange={(e) =>
                  returnErrorEditorProps.updateDraft(
                    returnErrorEditorProps.operationId,
                    { statusCode: Number.parseInt(e.target.value, 10) || 400 }
                  )
                }
                placeholder='400'
                className='h-8 text-xs'
              />
            </div>
            <div className='space-y-1'>
              <label className='text-xs font-medium'>
                {t('Error Code (optional)')}
              </label>
              <Input
                value={draft.code}
                onChange={(e) =>
                  returnErrorEditorProps.updateDraft(
                    returnErrorEditorProps.operationId,
                    { code: e.target.value }
                  )
                }
                placeholder='forced_bad_request'
                className='h-8 text-xs'
              />
            </div>
            <div className='space-y-1'>
              <label className='text-xs font-medium'>
                {t('Error Type (optional)')}
              </label>
              <Input
                value={draft.type}
                onChange={(e) =>
                  returnErrorEditorProps.updateDraft(
                    returnErrorEditorProps.operationId,
                    { type: e.target.value }
                  )
                }
                placeholder='invalid_request_error'
                className='h-8 text-xs'
              />
            </div>
          </div>
          <div className='mt-2 flex items-center gap-2'>
            <span className='text-muted-foreground text-xs'>
              {t('Retry Suggestion')}
            </span>
            <Button
              type='button'
              variant={draft.skipRetry ? 'default' : 'outline'}
              size='sm'
              className='h-7 text-xs'
              onClick={() =>
                returnErrorEditorProps.updateDraft(
                  returnErrorEditorProps.operationId,
                  { skipRetry: true }
                )
              }
            >
              {t('Stop Retry')}
            </Button>
            <Button
              type='button'
              variant={draft.skipRetry ? 'outline' : 'default'}
              size='sm'
              className='h-7 text-xs'
              onClick={() =>
                returnErrorEditorProps.updateDraft(
                  returnErrorEditorProps.operationId,
                  { skipRetry: false }
                )
              }
            >
              {t('Allow Retry')}
            </Button>
          </div>
          <div className='mt-2 flex flex-wrap gap-1'>
            {[
              {
                label: 'Bad Request',
                statusCode: 400,
                code: 'invalid_request',
                type: 'invalid_request_error',
              },
              {
                label: 'Unauthorized',
                statusCode: 401,
                code: 'unauthorized',
                type: 'authentication_error',
              },
              {
                label: 'Rate Limited',
                statusCode: 429,
                code: 'rate_limited',
                type: 'rate_limit_error',
              },
            ].map((preset) => (
              <Button
                key={preset.code}
                type='button'
                variant='outline'
                size='sm'
                className='h-6 text-[10px]'
                onClick={() =>
                  returnErrorEditorProps.updateDraft(
                    returnErrorEditorProps.operationId,
                    {
                      statusCode: preset.statusCode,
                      code: preset.code,
                      type: preset.type,
                    }
                  )
                }
              >
                {t(preset.label)}
              </Button>
            ))}
          </div>
        </>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// PruneObjectsEditor
// ---------------------------------------------------------------------------

type PruneObjectsEditorProps = {
  operationId: string
  draft: PruneObjectsDraft
  updateDraft: (
    operationId: string,
    updater:
      | Partial<PruneObjectsDraft>
      | ((draft: PruneObjectsDraft) => PruneObjectsDraft)
  ) => void
  addRule: (operationId: string) => void
  updateRule: (
    operationId: string,
    ruleId: string,
    patch: Partial<PruneRule>
  ) => void
  removeRule: (operationId: string, ruleId: string) => void
}

function PruneObjectsEditor(pruneObjectsEditorProps: PruneObjectsEditorProps) {
  const { t } = useTranslation()
  const draft = pruneObjectsEditorProps.draft

  return (
    <div className='rounded-lg border p-3'>
      <div className='mb-2 flex items-center justify-between'>
        <span className='text-sm font-medium'>{t('Object Prune Rules')}</span>
        <div className='flex items-center gap-1'>
          <span className='text-muted-foreground text-xs'>{t('Mode')}</span>
          <Button
            type='button'
            variant={draft.simpleMode ? 'default' : 'outline'}
            size='sm'
            className='h-7 text-xs'
            onClick={() =>
              pruneObjectsEditorProps.updateDraft(
                pruneObjectsEditorProps.operationId,
                { simpleMode: true }
              )
            }
          >
            {t('Simple')}
          </Button>
          <Button
            type='button'
            variant={draft.simpleMode ? 'outline' : 'default'}
            size='sm'
            className='h-7 text-xs'
            onClick={() =>
              pruneObjectsEditorProps.updateDraft(
                pruneObjectsEditorProps.operationId,
                { simpleMode: false }
              )
            }
          >
            {t('Advanced')}
          </Button>
        </div>
      </div>

      <div className='space-y-1.5'>
        <label className='text-xs font-medium'>{t('Type (common)')}</label>
        <Input
          value={draft.typeText}
          onChange={(e) =>
            pruneObjectsEditorProps.updateDraft(
              pruneObjectsEditorProps.operationId,
              { typeText: e.target.value }
            )
          }
          placeholder='redacted_thinking'
          className='h-8 text-xs'
        />
      </div>

      {draft.simpleMode ? (
        <p className='text-muted-foreground mt-2 text-xs'>
          {t('Simple mode: prune objects by type, e.g. redacted_thinking.')}
        </p>
      ) : (
        <>
          <div className='mt-3 grid gap-3 sm:grid-cols-2'>
            <div className='space-y-1'>
              <label className='text-xs font-medium'>{t('Logic')}</label>
              <Select
                items={[
                  { value: 'AND', label: t('All Must Match (AND)') },
                  { value: 'OR', label: t('Any Match (OR)') },
                ]}
                value={draft.logic}
                onValueChange={(v) =>
                  pruneObjectsEditorProps.updateDraft(
                    pruneObjectsEditorProps.operationId,
                    { logic: v || 'AND' }
                  )
                }
              >
                <SelectTrigger className='h-8 text-xs'>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent alignItemWithTrigger={false}>
                  <SelectGroup>
                    <SelectItem value='AND'>
                      {t('All Must Match (AND)')}
                    </SelectItem>
                    <SelectItem value='OR'>{t('Any Match (OR)')}</SelectItem>
                  </SelectGroup>
                </SelectContent>
              </Select>
            </div>
            <div className='space-y-1'>
              <label className='text-xs font-medium'>
                {t('Recursion Strategy')}
              </label>
              <div className='flex gap-1'>
                <Button
                  type='button'
                  variant={draft.recursive ? 'default' : 'outline'}
                  size='sm'
                  className='h-8 text-xs'
                  onClick={() =>
                    pruneObjectsEditorProps.updateDraft(
                      pruneObjectsEditorProps.operationId,
                      { recursive: true }
                    )
                  }
                >
                  {t('Recursive')}
                </Button>
                <Button
                  type='button'
                  variant={draft.recursive ? 'outline' : 'default'}
                  size='sm'
                  className='h-8 text-xs'
                  onClick={() =>
                    pruneObjectsEditorProps.updateDraft(
                      pruneObjectsEditorProps.operationId,
                      { recursive: false }
                    )
                  }
                >
                  {t('Current Level Only')}
                </Button>
              </div>
            </div>
          </div>

          <div className='bg-muted/30 mt-3 rounded-md border p-2'>
            <div className='mb-2 flex items-center justify-between'>
              <span className='text-xs font-medium'>
                {t('Additional Conditions')}
              </span>
              <Button
                type='button'
                variant='outline'
                size='sm'
                className='h-7 text-xs'
                onClick={() =>
                  pruneObjectsEditorProps.addRule(
                    pruneObjectsEditorProps.operationId
                  )
                }
              >
                <Plus className='mr-1 h-3 w-3' />
                {t('Add Condition')}
              </Button>
            </div>
            {draft.rules.length === 0 ? (
              <p className='text-muted-foreground text-xs'>
                {t(
                  'Without additional conditions, only the type above is used for pruning.'
                )}
              </p>
            ) : (
              <div className='space-y-2'>
                {draft.rules.map((rule, ruleIndex) => (
                  <div
                    key={rule.id}
                    className='bg-background rounded-md border p-2'
                  >
                    <div className='mb-1 flex items-center justify-between'>
                      <Badge variant='outline' className='text-[10px]'>
                        R{ruleIndex + 1}
                      </Badge>
                      <Button
                        type='button'
                        variant='ghost'
                        size='sm'
                        className='text-destructive hover:text-destructive h-6 text-[10px]'
                        onClick={() =>
                          pruneObjectsEditorProps.removeRule(
                            pruneObjectsEditorProps.operationId,
                            rule.id
                          )
                        }
                      >
                        <Trash2 className='mr-1 h-3 w-3' />
                        {t('Delete')}
                      </Button>
                    </div>
                    <div className='grid gap-2 sm:grid-cols-3'>
                      <div className='space-y-0.5'>
                        <label className='text-[10px] font-medium'>
                          {t('Field Path')}
                        </label>
                        <Input
                          value={rule.path}
                          onChange={(e) =>
                            pruneObjectsEditorProps.updateRule(
                              pruneObjectsEditorProps.operationId,
                              rule.id,
                              { path: e.target.value }
                            )
                          }
                          placeholder='type'
                          className='h-7 text-xs'
                        />
                      </div>
                      <div className='space-y-0.5'>
                        <label className='text-[10px] font-medium'>
                          {t('Match Mode')}
                        </label>
                        <Combobox
                          options={CONDITION_MODE_OPTIONS.map((o) => ({
                            value: o.value,
                            label: t(o.label),
                          }))}
                          value={rule.mode}
                          onValueChange={(v) =>
                            v !== null &&
                            pruneObjectsEditorProps.updateRule(
                              pruneObjectsEditorProps.operationId,
                              rule.id,
                              { mode: v }
                            )
                          }
                          className='h-7 text-xs'
                        />
                      </div>
                      <div className='space-y-0.5'>
                        <label className='text-[10px] font-medium'>
                          {t('Match Value (optional)')}
                        </label>
                        <Input
                          value={rule.value_text}
                          onChange={(e) =>
                            pruneObjectsEditorProps.updateRule(
                              pruneObjectsEditorProps.operationId,
                              rule.id,
                              { value_text: e.target.value }
                            )
                          }
                          placeholder='redacted_thinking'
                          className='h-7 text-xs'
                        />
                      </div>
                    </div>
                    <div className='mt-1.5 flex flex-wrap gap-3'>
                      <label className='flex items-center gap-1.5 text-[10px]'>
                        <Switch
                          checked={rule.invert}
                          onCheckedChange={(checked) =>
                            pruneObjectsEditorProps.updateRule(
                              pruneObjectsEditorProps.operationId,
                              rule.id,
                              { invert: checked }
                            )
                          }
                        />
                        {t('Invert match')}
                      </label>
                      <label className='flex items-center gap-1.5 text-[10px]'>
                        <Switch
                          checked={rule.pass_missing_key}
                          onCheckedChange={(checked) =>
                            pruneObjectsEditorProps.updateRule(
                              pruneObjectsEditorProps.operationId,
                              rule.id,
                              { pass_missing_key: checked }
                            )
                          }
                        />
                        {t('Pass when key is missing')}
                      </label>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        </>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// SyncFieldsEditor
// ---------------------------------------------------------------------------

type SyncFieldsEditorProps = {
  operationId: string
  syncFromTarget: { type: string; key: string }
  syncToTarget: { type: string; key: string }
  updateOperation: (
    operationId: string,
    patch: Partial<ParamOverrideOperation>
  ) => void
}

function SyncFieldsEditor(syncFieldsEditorProps: SyncFieldsEditorProps) {
  const { t } = useTranslation()
  return (
    <div className='space-y-3'>
      <label className='text-xs font-medium'>{t('Sync Endpoints')}</label>
      <div className='grid gap-3 sm:grid-cols-2'>
        <div className='space-y-1.5'>
          <label className='text-[10px] font-medium'>
            {t('Source Endpoint')}
          </label>
          <div className='flex gap-2'>
            <Select
              items={SYNC_TARGET_TYPE_OPTIONS.map((o) => ({
                value: o.value,
                label: t(o.label),
              }))}
              value={syncFieldsEditorProps.syncFromTarget.type || 'json'}
              onValueChange={(v) =>
                v !== null &&
                syncFieldsEditorProps.updateOperation(
                  syncFieldsEditorProps.operationId,
                  {
                    from: buildSyncTargetSpec(
                      v,
                      syncFieldsEditorProps.syncFromTarget.key
                    ),
                  }
                )
              }
            >
              <SelectTrigger className='h-8 w-[110px] text-xs'>
                <SelectValue />
              </SelectTrigger>
              <SelectContent alignItemWithTrigger={false}>
                <SelectGroup>
                  {SYNC_TARGET_TYPE_OPTIONS.map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {t(o.label)}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            <Input
              value={syncFieldsEditorProps.syncFromTarget.key}
              onChange={(e) =>
                syncFieldsEditorProps.updateOperation(
                  syncFieldsEditorProps.operationId,
                  {
                    from: buildSyncTargetSpec(
                      syncFieldsEditorProps.syncFromTarget.type,
                      e.target.value
                    ),
                  }
                )
              }
              placeholder='session_id'
              className='h-8 text-xs'
            />
          </div>
        </div>
        <div className='space-y-1.5'>
          <label className='text-[10px] font-medium'>
            {t('Target Endpoint')}
          </label>
          <div className='flex gap-2'>
            <Select
              items={SYNC_TARGET_TYPE_OPTIONS.map((o) => ({
                value: o.value,
                label: t(o.label),
              }))}
              value={syncFieldsEditorProps.syncToTarget.type || 'json'}
              onValueChange={(v) =>
                v !== null &&
                syncFieldsEditorProps.updateOperation(
                  syncFieldsEditorProps.operationId,
                  {
                    to: buildSyncTargetSpec(
                      v,
                      syncFieldsEditorProps.syncToTarget.key
                    ),
                  }
                )
              }
            >
              <SelectTrigger className='h-8 w-[110px] text-xs'>
                <SelectValue />
              </SelectTrigger>
              <SelectContent alignItemWithTrigger={false}>
                <SelectGroup>
                  {SYNC_TARGET_TYPE_OPTIONS.map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {t(o.label)}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            <Input
              value={syncFieldsEditorProps.syncToTarget.key}
              onChange={(e) =>
                syncFieldsEditorProps.updateOperation(
                  syncFieldsEditorProps.operationId,
                  {
                    to: buildSyncTargetSpec(
                      syncFieldsEditorProps.syncToTarget.type,
                      e.target.value
                    ),
                  }
                )
              }
              placeholder='prompt_cache_key'
              className='h-8 text-xs'
            />
          </div>
        </div>
      </div>
      <div className='flex flex-wrap gap-1'>
        {[
          {
            label: 'header:session_id -> json:prompt_cache_key',
            from: 'header:session_id',
            to: 'json:prompt_cache_key',
          },
          {
            label: 'json:prompt_cache_key -> header:session_id',
            from: 'json:prompt_cache_key',
            to: 'header:session_id',
          },
        ].map((preset) => (
          <Button
            key={preset.label}
            type='button'
            variant='outline'
            size='sm'
            className='h-6 text-[10px]'
            onClick={() =>
              syncFieldsEditorProps.updateOperation(
                syncFieldsEditorProps.operationId,
                { from: preset.from, to: preset.to }
              )
            }
          >
            {preset.label}
          </Button>
        ))}
      </div>
    </div>
  )
}
