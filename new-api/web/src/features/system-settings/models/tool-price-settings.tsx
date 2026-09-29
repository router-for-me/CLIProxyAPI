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
import { Code2, Copy, Eye, Plus, Trash2 } from 'lucide-react'
import { memo, useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table'
import { JsonCodeEditor } from '@/components/json-code-editor'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Field, FieldError } from '@/components/ui/field'
import { Input } from '@/components/ui/input'

import { useUpdateOption } from '../hooks/use-update-option'

const OPTION_KEY = 'tool_price_setting.prices'

const DEFAULT_PRICES: Record<string, number> = {
  web_search: 10.0,
  web_search_preview: 10.0,
  'web_search_preview:gpt-4o*': 25.0,
  'web_search_preview:gpt-4.1*': 25.0,
  'web_search_preview:gpt-4o-mini*': 25.0,
  'web_search_preview:gpt-4.1-mini*': 25.0,
  file_search: 2.5,
  google_search: 14.0,
  image_generation: 150.0,
}

type ToolPriceRow = {
  id: number
  key: string
  price: string
}

function parseToolPrice(value: string): number | null {
  if (value.trim() === '') return null
  const price = Number(value)
  if (!Number.isFinite(price) || price < 0) return null
  return price
}

function rowsToObject(rows: ToolPriceRow[]): Record<string, number> {
  const prices: Record<string, number> = {}
  for (const row of rows) {
    const k = row.key.trim()
    if (!k) continue
    const price = parseToolPrice(row.price)
    if (price === null) continue
    prices[k] = price
  }
  return prices
}

function objectToRows(prices: Record<string, number>): ToolPriceRow[] {
  return Object.entries(prices).map(([key, price], index) => ({
    id: index + 1,
    key,
    price: String(price),
  }))
}

function parseInitialPrices(
  rawValue: string | undefined
): Record<string, number> {
  if (!rawValue) return { ...DEFAULT_PRICES }
  try {
    const parsed = JSON.parse(rawValue) as unknown
    if (
      parsed &&
      typeof parsed === 'object' &&
      !Array.isArray(parsed) &&
      Object.keys(parsed as object).length > 0
    ) {
      const validPrices: Record<string, number> = {}
      for (const [key, value] of Object.entries(parsed)) {
        if (typeof value === 'number' && Number.isFinite(value) && value >= 0) {
          validPrices[key] = value
        }
      }
      // Merge defaults first so newly introduced tools appear for old stored
      // configs, while explicit stored values (including 0) still win.
      return {
        ...DEFAULT_PRICES,
        ...validPrices,
      }
    }
  } catch {
    // fall through to defaults
  }
  return { ...DEFAULT_PRICES }
}

type ToolPriceSettingsProps = {
  defaultValue: string
}

export const ToolPriceSettings = memo(function ToolPriceSettings({
  defaultValue,
}: ToolPriceSettingsProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const [editMode, setEditMode] = useState<'visual' | 'json'>('visual')
  const [rows, setRows] = useState<ToolPriceRow[]>([])
  const [jsonText, setJsonText] = useState('')
  const [jsonError, setJsonError] = useState('')
  const [nextRowId, setNextRowId] = useState(1)

  useEffect(() => {
    const prices = parseInitialPrices(defaultValue)
    const initialRows = objectToRows(prices)
    setRows(initialRows)
    setJsonText(JSON.stringify(prices, null, 2))
    setJsonError('')
    setNextRowId(initialRows.length + 1)
  }, [defaultValue])

  const currentPrices = useMemo(() => rowsToObject(rows), [rows])
  const invalidRowIds = useMemo(
    () =>
      new Set(
        rows
          .filter((row) => parseToolPrice(row.price) === null)
          .map((row) => row.id)
      ),
    [rows]
  )

  const syncFromRows = useCallback((nextRows: ToolPriceRow[]) => {
    setRows(nextRows)
    setJsonText(JSON.stringify(rowsToObject(nextRows), null, 2))
    setJsonError('')
  }, [])

  const handleJsonChange = useCallback(
    (text: string) => {
      setJsonText(text)
      try {
        const parsed = JSON.parse(text) as unknown
        if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
          setJsonError(t('JSON must be an object'))
          return
        }
        const prices: Record<string, number> = {}
        for (const [key, value] of Object.entries(parsed)) {
          if (
            typeof value !== 'number' ||
            !Number.isFinite(value) ||
            value < 0
          ) {
            setJsonError(t('Please enter a valid number'))
            return
          }
          prices[key] = value
        }
        const nextRows = objectToRows(prices)
        setRows(nextRows)
        setNextRowId(nextRows.length + 1)
        setJsonError('')
      } catch (error) {
        setJsonError(error instanceof Error ? error.message : t('Invalid JSON'))
      }
    },
    [t]
  )

  const updateRow = useCallback(
    (id: number, field: 'key' | 'price', value: string) => {
      syncFromRows(
        rows.map((r) => (r.id === id ? { ...r, [field]: value } : r))
      )
    },
    [rows, syncFromRows]
  )

  const addRow = useCallback(() => {
    const newRow: ToolPriceRow = { id: nextRowId, key: '', price: '0' }
    setNextRowId((prev) => prev + 1)
    syncFromRows([...rows, newRow])
  }, [nextRowId, rows, syncFromRows])

  const removeRow = useCallback(
    (id: number) => {
      syncFromRows(rows.filter((r) => r.id !== id))
    },
    [rows, syncFromRows]
  )

  const resetToDefault = useCallback(() => {
    const initialRows = objectToRows(DEFAULT_PRICES)
    setRows(initialRows)
    setJsonText(JSON.stringify(DEFAULT_PRICES, null, 2))
    setJsonError('')
    setNextRowId(initialRows.length + 1)
  }, [])

  const handleCopyJson = useCallback(async () => {
    try {
      await navigator.clipboard.writeText(jsonText)
      toast.success(t('Copied to clipboard'))
    } catch {
      toast.error(t('Failed to copy'))
    }
  }, [jsonText, t])

  const handleSave = useCallback(async () => {
    if (invalidRowIds.size > 0) {
      toast.error(t('Please enter a valid number'))
      return
    }
    if (editMode === 'json' && jsonError) {
      toast.error(t('Please fix JSON errors before saving'))
      return
    }
    await updateOption.mutateAsync({
      key: OPTION_KEY,
      value: JSON.stringify(currentPrices),
    })
  }, [currentPrices, editMode, invalidRowIds.size, jsonError, t, updateOption])

  const toggleEditMode = useCallback(() => {
    setEditMode((prev) => (prev === 'visual' ? 'json' : 'visual'))
  }, [])

  return (
    <div className='space-y-4'>
      <Alert>
        <AlertDescription className='space-y-1 text-sm'>
          <div>
            {t(
              'Configure per-tool unit prices ($/1K calls). Per-request models do not incur additional tool fees.'
            )}
          </div>
          <div>
            <span className='font-medium'>{t('Format')}:</span>{' '}
            <code className='bg-muted rounded px-1 py-0.5 text-xs'>
              web_search_preview
            </code>{' '}
            {t('is the default price; ')}
            <code className='bg-muted rounded px-1 py-0.5 text-xs'>
              web_search_preview:gpt-4o*
            </code>{' '}
            {t('overrides for matching model prefix.')}
          </div>
        </AlertDescription>
      </Alert>

      <div className='flex flex-wrap items-center justify-between gap-2'>
        <div className='flex flex-wrap items-center gap-2'>
          {editMode === 'visual' ? (
            <>
              <Button variant='outline' size='sm' onClick={addRow}>
                <Plus className='mr-2 h-4 w-4' />
                {t('Add')}
              </Button>
              <Button variant='ghost' size='sm' onClick={resetToDefault}>
                {t('Restore defaults')}
              </Button>
            </>
          ) : (
            <>
              <Button variant='ghost' size='sm' onClick={handleCopyJson}>
                <Copy className='mr-2 h-4 w-4' />
                {t('Copy')}
              </Button>
              <Button variant='ghost' size='sm' onClick={resetToDefault}>
                {t('Restore defaults')}
              </Button>
            </>
          )}
        </div>
        <Button variant='outline' size='sm' onClick={toggleEditMode}>
          {editMode === 'visual' ? (
            <>
              <Code2 className='mr-2 h-4 w-4' />
              {t('Switch to JSON')}
            </>
          ) : (
            <>
              <Eye className='mr-2 h-4 w-4' />
              {t('Switch to Visual')}
            </>
          )}
        </Button>
      </div>

      {editMode === 'visual' ? (
        <StaticDataTable
          data={rows}
          getRowKey={(row) => row.id}
          emptyClassName='text-muted-foreground py-8'
          emptyContent={t('No tools configured')}
          columns={[
            {
              id: 'tool',
              header: t('Tool identifier'),
              cell: (row) => (
                <Input
                  value={row.key}
                  placeholder='web_search_preview:gpt-4o*'
                  onChange={(e) => updateRow(row.id, 'key', e.target.value)}
                />
              ),
            },
            {
              id: 'price',
              header: t('Price ($/1K calls)'),
              className: 'w-[200px]',
              cell: (row) => {
                const isInvalid = invalidRowIds.has(row.id)
                return (
                  <Field data-invalid={isInvalid}>
                    <Input
                      type='number'
                      min={0}
                      step={0.0001}
                      value={row.price}
                      aria-invalid={isInvalid}
                      aria-label={`${t('Price ($/1K calls)')}: ${row.key || t('Tool identifier')}`}
                      onChange={(e) =>
                        updateRow(row.id, 'price', e.target.value)
                      }
                    />
                    {isInvalid ? (
                      <FieldError>
                        {t('Please enter a valid number')}
                      </FieldError>
                    ) : null}
                  </Field>
                )
              },
            },
            {
              id: 'actions',
              header: t('Actions'),
              className: 'text-right',
              cellClassName: 'text-right',
              cell: (row) => (
                <Button
                  variant='ghost'
                  size='icon'
                  onClick={() => removeRow(row.id)}
                  aria-label={t('Delete')}
                >
                  <Trash2 className='text-destructive h-4 w-4' />
                </Button>
              ),
            },
          ]}
        />
      ) : (
        <div className='space-y-2'>
          <JsonCodeEditor
            value={jsonText}
            onChange={handleJsonChange}
            heightClassName='h-72 min-h-72 max-h-72'
            aria-invalid={Boolean(jsonError)}
          />
          {jsonError && <p className='text-destructive text-sm'>{jsonError}</p>}
        </div>
      )}

      <div className='flex justify-end'>
        <Button
          onClick={handleSave}
          disabled={
            updateOption.isPending ||
            invalidRowIds.size > 0 ||
            (editMode === 'json' && !!jsonError)
          }
        >
          {t('Save tool prices')}
        </Button>
      </div>
    </div>
  )
})
