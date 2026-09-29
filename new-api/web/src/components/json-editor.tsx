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
import { Code, Table, Plus, Trash2 } from 'lucide-react'
import { useState, useEffect } from 'react'
import { useTranslation } from 'react-i18next'

import { JsonCodeEditor } from '@/components/json-code-editor'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

type JsonEditorProps = {
  value: string
  onChange: (value: string) => void
  disabled?: boolean
  keyPlaceholder?: string
  valuePlaceholder?: string
  keyLabel?: string
  valueLabel?: string
  emptyMessage?: string
  template?: Record<string, unknown>
  valueType?: 'string' | 'number' | 'any'
}

type EditorRow = {
  id: string
  key: string
  value: string
}

function parseJsonRows(json: string): EditorRow[] {
  try {
    if (!json.trim()) return []
    const parsed = JSON.parse(json)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      return []
    }
    return Object.entries(parsed).map(([key, val], index) => ({
      id: `${Date.now()}-${index}`,
      key,
      value: typeof val === 'object' ? JSON.stringify(val) : String(val),
    }))
  } catch {
    return []
  }
}

export function JsonEditor({
  value,
  onChange,
  disabled = false,
  keyPlaceholder,
  valuePlaceholder,
  keyLabel,
  valueLabel,
  emptyMessage,
  template,
  valueType = 'string',
}: JsonEditorProps) {
  const { t } = useTranslation()
  const resolvedEmptyMessage =
    emptyMessage ?? t('No mappings configured. Click "Add Row" to get started.')
  const resolvedKeyPlaceholder = keyPlaceholder ?? t('Key')
  const resolvedValuePlaceholder = valuePlaceholder ?? t('Value')
  const resolvedKeyLabel = keyLabel ?? t('Key')
  const resolvedValueLabel = valueLabel ?? t('Value')
  const [mode, setMode] = useState<'visual' | 'json'>('visual')
  const [rows, setRows] = useState<EditorRow[]>(() => parseJsonRows(value))
  const [jsonValue, setJsonValue] = useState(value)

  const parseJsonToRows = (json: string) => {
    setRows(parseJsonRows(json))
  }

  // Parse JSON to rows when value changes externally
  useEffect(() => {
    if (value !== jsonValue) {
      setJsonValue(value)
      parseJsonToRows(value)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value])

  const convertRowsToJson = (updatedRows: EditorRow[]): string => {
    if (updatedRows.length === 0) {
      return ''
    }
    const obj: Record<string, unknown> = {}
    updatedRows.forEach((row) => {
      if (row.key.trim()) {
        let parsedValue: unknown = row.value.trim()

        // Try to parse value based on type
        if (valueType === 'number') {
          parsedValue = Number(parsedValue) || 0
        } else if (valueType === 'any') {
          // Try to parse as JSON first
          try {
            parsedValue = JSON.parse(row.value)
          } catch {
            // If not valid JSON, keep as string
            parsedValue = row.value.trim()
          }
        }

        obj[row.key.trim()] = parsedValue
      }
    })
    return JSON.stringify(obj, null, 2)
  }

  const handleAddRow = () => {
    const newRow: EditorRow = {
      id: `${Date.now()}`,
      key: '',
      value: '',
    }
    const updatedRows = [...rows, newRow]
    setRows(updatedRows)
  }

  const handleDeleteRow = (id: string) => {
    const updatedRows = rows.filter((row) => row.id !== id)
    setRows(updatedRows)
    const json = convertRowsToJson(updatedRows)
    setJsonValue(json)
    onChange(json)
  }

  const handleRowChange = (
    id: string,
    field: 'key' | 'value',
    newValue: string
  ) => {
    const updatedRows = rows.map((row) =>
      row.id === id ? { ...row, [field]: newValue } : row
    )
    setRows(updatedRows)
    const json = convertRowsToJson(updatedRows)
    setJsonValue(json)
    onChange(json)
  }

  const handleJsonChange = (newJson: string) => {
    setJsonValue(newJson)
    onChange(newJson)
    parseJsonToRows(newJson)
  }

  const handleFillTemplate = () => {
    if (!template) return
    const templateJson = JSON.stringify(template, null, 2)
    setJsonValue(templateJson)
    onChange(templateJson)
    parseJsonToRows(templateJson)
  }

  const toggleMode = () => {
    if (mode === 'visual') {
      // Switching to JSON mode: sync rows to JSON
      const json = convertRowsToJson(rows)
      setJsonValue(json)
      onChange(json)
      setMode('json')
    } else {
      // Switching to visual mode: sync JSON to rows
      parseJsonToRows(jsonValue)
      setMode('visual')
    }
  }

  return (
    <div className='space-y-2'>
      <div className='flex items-center justify-between'>
        <div className='flex gap-2'>
          <Button
            type='button'
            variant='outline'
            size='sm'
            onClick={toggleMode}
            disabled={disabled}
          >
            {mode === 'visual' ? (
              <>
                <Code className='mr-2 h-4 w-4' />
                {t('JSON Mode')}
              </>
            ) : (
              <>
                <Table className='mr-2 h-4 w-4' />
                {t('Visual Mode')}
              </>
            )}
          </Button>
          {template && (
            <Button
              type='button'
              variant='link'
              size='sm'
              className='h-auto p-0'
              onClick={handleFillTemplate}
              disabled={disabled}
            >
              {t('Fill Template')}
            </Button>
          )}
        </div>
      </div>

      {mode === 'visual' ? (
        <div className='space-y-2'>
          {rows.length > 0 ? (
            <div className='space-y-2'>
              <div className='grid grid-cols-[1fr_1fr_auto] gap-2 text-sm font-medium'>
                <div>{resolvedKeyLabel}</div>
                <div>{resolvedValueLabel}</div>
                <div className='w-10' />
              </div>
              {rows.map((row) => (
                <div
                  key={row.id}
                  className='grid grid-cols-[1fr_1fr_auto] gap-2'
                >
                  <Input
                    value={row.key}
                    onChange={(e) =>
                      handleRowChange(row.id, 'key', e.target.value)
                    }
                    placeholder={resolvedKeyPlaceholder}
                    disabled={disabled}
                  />
                  <Input
                    value={row.value}
                    onChange={(e) =>
                      handleRowChange(row.id, 'value', e.target.value)
                    }
                    placeholder={resolvedValuePlaceholder}
                    disabled={disabled}
                  />
                  <Button
                    type='button'
                    variant='ghost'
                    size='icon'
                    aria-label='Delete row'
                    onClick={() => handleDeleteRow(row.id)}
                    disabled={disabled}
                    className='h-10 w-10'
                  >
                    <Trash2 className='h-4 w-4' aria-hidden='true' />
                  </Button>
                </div>
              ))}
            </div>
          ) : (
            <div className='text-muted-foreground flex h-24 items-center justify-center rounded-md border border-dashed text-sm'>
              {resolvedEmptyMessage}
            </div>
          )}
          <Button
            type='button'
            variant='outline'
            size='sm'
            onClick={handleAddRow}
            disabled={disabled}
            className='w-full'
          >
            <Plus className='mr-2 h-4 w-4' />
            {t('Add Row')}
          </Button>
        </div>
      ) : (
        <JsonCodeEditor
          value={jsonValue}
          onChange={handleJsonChange}
          placeholder={
            template ? JSON.stringify(template, null, 2) : '{"key": "value"}'
          }
          disabled={disabled}
          ariaLabel={t('JSON')}
        />
      )}
    </div>
  )
}
