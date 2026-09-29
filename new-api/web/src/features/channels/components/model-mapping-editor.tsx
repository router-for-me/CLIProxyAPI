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
import { Code, ListPlus, Plus, Table, Trash2 } from 'lucide-react'
import {
  useEffect,
  useEffectEvent,
  useId,
  useMemo,
  useRef,
  useState,
} from 'react'
import { useTranslation } from 'react-i18next'

import { JsonCodeEditor } from '@/components/json-code-editor'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { ComboboxInput } from '@/components/ui/combobox-input'
import { Input } from '@/components/ui/input'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

type MappingField = 'from' | 'to'

/**
 * Asks the editor to open a mapping row for a model the caller already knows
 * one side of. When a row for `from` exists its upstream field is focused;
 * otherwise a draft row is appended and the empty side receives focus. Drafts
 * are only written to the mapping once both names are filled in.
 */
export type ModelMappingDraftRequest = {
  /** Request model name; empty when only the upstream name is known. */
  from: string
  /** Upstream model name; empty when only the request name is known. */
  to: string
  /** Field to focus; defaults to the empty side, or the upstream side of an existing row. */
  focus?: MappingField
  /** Changes re-trigger the request even for identical names. */
  token: number
}

type ModelMappingEditorProps = {
  value: string
  onChange: (value: string) => void
  disabled?: boolean
  sourceModelOptions?: string[]
  targetModelOptions?: string[]
  /** Shows the batch button; the caller owns the batch dialog. */
  onBatchAdd?: () => void
  /**
   * Fires with the latest value once an edit settles: Enter, focus leaving the
   * editor, or a row being deleted. Lets callers react per completed edit
   * instead of per keystroke.
   */
  onCommit?: (value: string) => void
  draftRequest?: ModelMappingDraftRequest | null
  onDraftRequestHandled?: () => void
}

type MappingRow = {
  id: string
  from: string
  to: string
}

const DUPLICATE_MAPPING_SENTINEL = '{ "duplicate_source_models": '
/** Show the row filter once the table is long enough to need scanning. */
const MAPPING_FILTER_THRESHOLD = 6

function getDuplicateSources(rows: MappingRow[]): string[] {
  const seen = new Set<string>()
  const duplicates = new Set<string>()

  for (const row of rows) {
    const source = row.from.trim()
    if (!source) continue
    if (seen.has(source)) {
      duplicates.add(source)
    } else {
      seen.add(source)
    }
  }

  return [...duplicates]
}

export function ModelMappingEditor(props: ModelMappingEditorProps) {
  const { t } = useTranslation()
  const inputsId = useId()
  const [mode, setMode] = useState<'visual' | 'json'>('visual')
  const [rows, setRows] = useState<MappingRow[]>([])
  const [jsonValue, setJsonValue] = useState(props.value)
  const [jsonError, setJsonError] = useState<string | null>(null)
  const [filter, setFilter] = useState('')
  const nextRowIdRef = useRef(0)
  const lastEmittedRef = useRef(props.value)
  const pendingRowFocusRef = useRef<{
    rowId: string
    field: MappingField
  } | null>(null)
  const duplicateSources = useMemo(() => getDuplicateSources(rows), [rows])
  const sourceOptions = useMemo(
    () =>
      (props.sourceModelOptions ?? []).map((model) => ({
        value: model,
        label: model,
      })),
    [props.sourceModelOptions]
  )
  const targetOptions = useMemo(
    () =>
      (props.targetModelOptions ?? []).map((model) => ({
        value: model,
        label: model,
      })),
    [props.targetModelOptions]
  )

  const createRowId = () => {
    nextRowIdRef.current += 1
    return `mapping-${nextRowIdRef.current}`
  }

  const rowInputId = (rowId: string, field: MappingField) =>
    `${inputsId}-${rowId}-${field}`

  const parseJsonToRows = (json: string): boolean => {
    try {
      if (!json.trim()) {
        setRows([])
        setJsonError(null)
        return true
      }
      const parsed = JSON.parse(json)
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
        setJsonError(t('Model mapping must be a valid JSON object'))
        return false
      }
      const entries = Object.entries(parsed)
      const invalidValue = entries.find(([, to]) => typeof to !== 'string')
      if (invalidValue) {
        setJsonError(t('Model mapping values must be strings'))
        return false
      }
      setRows((previousRows) => {
        const remainingRows = [...previousRows]
        const mapped = entries.map(([from, to], index) => {
          const toString = String(to)
          const existingIndex = remainingRows.findIndex(
            (row) =>
              row.from === from ||
              (row.from === from && row.to === toString) ||
              previousRows[index]?.id === row.id
          )
          if (existingIndex >= 0) {
            const [existing] = remainingRows.splice(existingIndex, 1)
            return {
              id: existing.id,
              from,
              to: toString,
            }
          }
          return {
            id: createRowId(),
            from,
            to: toString,
          }
        })
        // Rows without a request name are not serialized yet; keep them so a
        // draft survives edits to other rows.
        const mappedIds = new Set(mapped.map((row) => row.id))
        const drafts = previousRows.filter(
          (row) => !row.from.trim() && !mappedIds.has(row.id)
        )
        return [...mapped, ...drafts]
      })
      setJsonError(null)
      return true
    } catch {
      setJsonError(t('Model mapping must be valid JSON format'))
      return false
    }
  }

  const syncExternalValue = useEffectEvent(() => {
    lastEmittedRef.current = props.value
    setJsonValue(props.value)
    parseJsonToRows(props.value)
  })

  const commit = () => {
    props.onCommit?.(lastEmittedRef.current)
  }

  // Only replace the draft when the external value changes, not on language changes.
  useEffect(() => {
    syncExternalValue()
  }, [props.value])

  // A freshly added row exists one commit after the click, so focus it once
  // its inputs are rendered.
  const focusPendingRow = useEffectEvent(() => {
    const pending = pendingRowFocusRef.current
    if (!pending) return
    const input = document.querySelector(
      `[id="${rowInputId(pending.rowId, pending.field)}"]`
    )
    if (!(input instanceof HTMLInputElement)) return
    pendingRowFocusRef.current = null
    input.focus()
  })

  useEffect(() => {
    focusPendingRow()
  }, [rows])

  const applyDraftRequest = useEffectEvent(() => {
    const request = props.draftRequest
    if (!request) return
    const existing = request.from
      ? rows.find((row) => row.from === request.from)
      : undefined
    if (existing) {
      const input = document.querySelector(
        `[id="${rowInputId(existing.id, request.focus ?? 'to')}"]`
      )
      if (input instanceof HTMLInputElement) {
        input.focus()
        input.scrollIntoView({ block: 'center' })
      }
    } else {
      const rowId = createRowId()
      pendingRowFocusRef.current = {
        rowId,
        field: request.focus ?? (request.from ? 'to' : 'from'),
      }
      setRows((previous) => [
        ...previous,
        { id: rowId, from: request.from, to: request.to },
      ])
    }
    props.onDraftRequestHandled?.()
  })

  // Callers raise draft requests while closing a dialog, whose focus return
  // runs on the next frame; wait one frame so the editor keeps the focus.
  useEffect(() => {
    if (!props.draftRequest) return
    const frame = window.requestAnimationFrame(() => applyDraftRequest())
    return () => window.cancelAnimationFrame(frame)
  }, [props.draftRequest])

  const convertRowsToJson = (updatedRows: MappingRow[]): string => {
    if (updatedRows.length === 0) {
      return ''
    }
    const obj: Record<string, string> = {}
    updatedRows.forEach((row) => {
      if (row.from.trim()) {
        obj[row.from.trim()] = row.to.trim()
      }
    })
    return JSON.stringify(obj, null, 2)
  }

  const syncRows = (updatedRows: MappingRow[]) => {
    setRows(updatedRows)
    const duplicates = getDuplicateSources(updatedRows)
    if (duplicates.length > 0) {
      setJsonError(t('Duplicate source model mappings are not allowed'))
      setJsonValue(DUPLICATE_MAPPING_SENTINEL)
      lastEmittedRef.current = DUPLICATE_MAPPING_SENTINEL
      props.onChange(DUPLICATE_MAPPING_SENTINEL)
      return
    }

    const json = convertRowsToJson(updatedRows)
    setJsonError(null)
    setJsonValue(json)
    lastEmittedRef.current = json
    props.onChange(json)
  }

  const handleAddRow = () => {
    const newRow: MappingRow = {
      id: createRowId(),
      from: '',
      to: '',
    }
    pendingRowFocusRef.current = { rowId: newRow.id, field: 'from' }
    syncRows([...rows, newRow])
  }

  const handleDeleteRow = (id: string) => {
    syncRows(rows.filter((row) => row.id !== id))
    commit()
  }

  const handleRowChange = (
    id: string,
    field: MappingField,
    newValue: string
  ) => {
    const updatedRows = rows.map((row) =>
      row.id === id ? { ...row, [field]: newValue } : row
    )
    syncRows(updatedRows)
  }

  const handleJsonChange = (newJson: string) => {
    setJsonValue(newJson)
    lastEmittedRef.current = newJson
    props.onChange(newJson)
    parseJsonToRows(newJson)
  }

  const handleFillTemplate = () => {
    const template = JSON.stringify(
      { 'gpt-3.5-turbo': 'gpt-3.5-turbo-0125' },
      null,
      2
    )
    setJsonValue(template)
    lastEmittedRef.current = template
    props.onChange(template)
    parseJsonToRows(template)
    commit()
  }

  const handleModeChange = (nextMode: string) => {
    if (nextMode !== 'visual' && nextMode !== 'json') return
    if (nextMode === 'json') {
      const duplicates = getDuplicateSources(rows)
      if (duplicates.length === 0) {
        const json = convertRowsToJson(rows)
        setJsonValue(json)
        lastEmittedRef.current = json
        props.onChange(json)
      }
      setMode('json')
      return
    }
    parseJsonToRows(jsonValue)
    setMode('visual')
  }

  const filterKeyword = filter.trim().toLowerCase()
  const showFilter = rows.length >= MAPPING_FILTER_THRESHOLD
  let visibleRows = rows
  if (showFilter && filterKeyword) {
    visibleRows = rows.filter(
      (row) =>
        row.from.toLowerCase().includes(filterKeyword) ||
        row.to.toLowerCase().includes(filterKeyword)
    )
  }

  return (
    <div
      className='space-y-2'
      onKeyDown={(event) => {
        if (event.key === 'Enter') commit()
      }}
      onBlur={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget)) commit()
      }}
    >
      <Tabs value={mode} onValueChange={handleModeChange} className='space-y-2'>
        <div className='flex flex-wrap items-center justify-between gap-3'>
          <TabsList>
            <TabsTrigger value='visual'>
              <Table className='h-4 w-4' aria-hidden='true' />
              {t('Visual')}
            </TabsTrigger>
            <TabsTrigger value='json'>
              <Code className='h-4 w-4' aria-hidden='true' />
              {t('JSON')}
            </TabsTrigger>
          </TabsList>
          <div className='flex items-center gap-3'>
            {props.onBatchAdd && (
              <Button
                type='button'
                variant='outline'
                size='sm'
                onClick={props.onBatchAdd}
                disabled={props.disabled || Boolean(jsonError)}
              >
                <ListPlus aria-hidden='true' />
                {t('Batch Add')}
              </Button>
            )}
            <Button
              type='button'
              variant='link'
              size='sm'
              className='h-auto p-0'
              onClick={handleFillTemplate}
              disabled={props.disabled}
            >
              {t('Fill Template')}
            </Button>
          </div>
        </div>

        {jsonError && (
          <Alert variant='destructive'>
            <AlertDescription>{jsonError}</AlertDescription>
          </Alert>
        )}

        {duplicateSources.length > 0 && (
          <Alert>
            <AlertDescription>
              {t('Duplicate source model(s): {{models}}', {
                models: duplicateSources.join(', '),
              })}
            </AlertDescription>
          </Alert>
        )}

        <TabsContent value='visual' className='space-y-2'>
          <p className='text-muted-foreground text-xs'>
            {t(
              'Users call the model on the left. The platform forwards the request to the upstream model on the right.'
            )}
          </p>
          {rows.length > 0 ? (
            <div className='space-y-2'>
              {showFilter && (
                <div className='space-y-1'>
                  <Input
                    aria-label={t('Filter mappings')}
                    placeholder={t('Filter by model name')}
                    value={filter}
                    onChange={(event) => setFilter(event.target.value)}
                  />
                  {filterKeyword && (
                    <p className='text-muted-foreground text-xs'>
                      {t('Showing {{shown}} of {{total}} mappings', {
                        shown: visibleRows.length,
                        total: rows.length,
                      })}
                    </p>
                  )}
                </div>
              )}
              <div className='grid grid-cols-[1fr_1fr_auto] gap-2 text-sm font-medium'>
                <div>{t('Request Model Name')}</div>
                <div>{t('Upstream Model Name')}</div>
                <div className='w-10' />
              </div>
              {visibleRows.map((row) => (
                <div
                  key={row.id}
                  className='grid grid-cols-[1fr_1fr_auto] gap-2'
                >
                  <ComboboxInput
                    id={rowInputId(row.id, 'from')}
                    options={sourceOptions}
                    value={row.from}
                    onValueChange={(value) =>
                      handleRowChange(row.id, 'from', value)
                    }
                    placeholder='gpt-3.5-turbo'
                    emptyText='No matching items'
                    allowCustomValue
                    disabled={props.disabled}
                    aria-label={t('Request Model Name')}
                  />
                  <ComboboxInput
                    id={rowInputId(row.id, 'to')}
                    options={targetOptions}
                    value={row.to}
                    onValueChange={(value) =>
                      handleRowChange(row.id, 'to', value)
                    }
                    placeholder='gpt-3.5-turbo-0125'
                    emptyText='No matching items'
                    allowCustomValue
                    disabled={props.disabled}
                    aria-label={t('Upstream Model Name')}
                  />
                  <Button
                    type='button'
                    variant='ghost'
                    size='icon'
                    onClick={() => handleDeleteRow(row.id)}
                    disabled={props.disabled}
                    className='h-10 w-10'
                    aria-label={t('Delete mapping')}
                  >
                    <Trash2 className='h-4 w-4' aria-hidden='true' />
                  </Button>
                </div>
              ))}
              {visibleRows.length === 0 && (
                <p className='text-muted-foreground py-3 text-center text-sm'>
                  {t('No matching items')}
                </p>
              )}
            </div>
          ) : (
            <div className='text-muted-foreground flex h-24 items-center justify-center rounded-md border border-dashed text-sm'>
              {t(
                'No model mappings configured. Click "Add Mapping" to get started.'
              )}
            </div>
          )}
          <Button
            type='button'
            variant='outline'
            size='sm'
            onClick={handleAddRow}
            disabled={props.disabled}
            className='w-full'
          >
            <Plus className='mr-2 h-4 w-4' />
            {t('Add Mapping')}
          </Button>
        </TabsContent>
        <TabsContent value='json' className='space-y-2'>
          <p className='text-muted-foreground text-sm'>
            {t(
              'JSON keys are request model names; values are upstream model names.'
            )}
          </p>
          <JsonCodeEditor
            value={jsonValue}
            onChange={handleJsonChange}
            placeholder='{"request-model": "upstream-model"}'
            disabled={props.disabled}
            className={jsonError ? 'border-destructive' : undefined}
            aria-invalid={Boolean(jsonError)}
            ariaLabel={t('Model Mapping')}
          />
        </TabsContent>
      </Tabs>
    </div>
  )
}
