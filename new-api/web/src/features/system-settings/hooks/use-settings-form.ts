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
import i18next from 'i18next'
import { useEffect, useMemo, useRef } from 'react'
import {
  useForm,
  type UseFormProps,
  type FieldValues,
  type FieldNamesMarkedBoolean,
} from 'react-hook-form'
import { toast } from 'sonner'

type SettingsFormOptions<T extends FieldValues> = UseFormProps<T> & {
  onSubmit: (data: T, changedFields: Record<string, unknown>) => Promise<void>
  compareValues?: (a: unknown, b: unknown) => boolean
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function flattenValues<T extends FieldValues>(
  values: T,
  parentKey?: string,
  result: Record<string, unknown> = {}
): Record<string, unknown> {
  if (!isPlainObject(values)) {
    if (typeof parentKey === 'string') {
      result[parentKey] = values
    }
    return result
  }

  Object.entries(values).forEach(([key, value]) => {
    const nextKey = parentKey ? `${parentKey}.${key}` : key

    if (isPlainObject(value)) {
      flattenValues(value as FieldValues, nextKey, result)
      return
    }

    if (Array.isArray(value)) {
      result[nextKey] = value
      return
    }

    result[nextKey] = value
  })

  return result
}

function collectDirtyValues<TFieldValues extends FieldValues>(
  dirtyFields: Partial<FieldNamesMarkedBoolean<TFieldValues>> | boolean,
  values: unknown,
  parentKey?: string,
  result: Record<string, unknown> = {}
) {
  if (dirtyFields === true) {
    if (typeof parentKey === 'string') {
      result[parentKey] = values
    }
    return result
  }

  if (!dirtyFields || typeof dirtyFields !== 'object') {
    return result
  }

  Object.entries(dirtyFields).forEach(([key, value]) => {
    const nextKey = parentKey ? `${parentKey}.${key}` : key
    const source =
      values && (isPlainObject(values) || Array.isArray(values))
        ? (values as Record<string, unknown>)[key]
        : undefined

    if (value === true) {
      result[nextKey] = source
      return
    }

    if (value && typeof value === 'object') {
      collectDirtyValues(
        value as Partial<FieldNamesMarkedBoolean<FieldValues>>,
        source as FieldValues,
        nextKey,
        result
      )
    }
  })

  return result
}

function setDeepValue(
  target: Record<string, unknown>,
  path: string,
  value: unknown
) {
  const segments = path.split('.')
  let current: Record<string, unknown> = target

  segments.forEach((segment, index) => {
    const isLast = index === segments.length - 1
    if (isLast) {
      current[segment] = value
      return
    }

    const next = current[segment]
    if (isPlainObject(next)) {
      current = next as Record<string, unknown>
      return
    }

    current[segment] = {}
    current = current[segment] as Record<string, unknown>
  })
}

function expandDotPaths<T extends FieldValues>(
  values: T | undefined
): T | undefined {
  if (!values || !isPlainObject(values)) {
    return values
  }

  const result: Record<string, unknown> = {}

  Object.entries(values).forEach(([key, value]) => {
    if (key.includes('.')) {
      setDeepValue(result, key, value)
      return
    }

    if (isPlainObject(value)) {
      result[key] = expandDotPaths(value as FieldValues)
      return
    }

    result[key] = value
  })

  return result as T
}

/**
 * Unified hook for system settings forms
 *
 * Key features:
 * - Initializes form with defaultValues only on mount
 * - No automatic resets that could overwrite user input
 * - Tracks changed fields to minimize API calls
 * - Provides manual reset functionality
 *
 * @example
 * ```tsx
 * const { form, handleSubmit, handleReset } = useSettingsForm({
 *   resolver: zodResolver(schema),
 *   defaultValues,
 *   onSubmit: async (data, changed) => {
 *     for (const [key, value] of Object.entries(changed)) {
 *       await updateOption.mutateAsync({ key, value })
 *     }
 *   }
 * })
 * ```
 */
export function useSettingsForm<T extends FieldValues>({
  onSubmit,
  compareValues,
  defaultValues,
  ...formOptions
}: SettingsFormOptions<T>) {
  const expandedDefaults = useMemo(
    () => expandDotPaths(defaultValues),
    [defaultValues]
  )

  const form = useForm<T>({ ...formOptions, defaultValues: expandedDefaults })

  const defaultValuesRef = useRef<T>((expandedDefaults ?? ({} as T)) as T)
  const baselineRef = useRef<Record<string, unknown>>(
    flattenValues((expandedDefaults ?? ({} as T)) as T)
  )

  /* eslint-disable react-hooks/refs */
  const serializedDefaultsRef = useRef<string>(
    JSON.stringify(baselineRef.current)
  )
  /* eslint-enable react-hooks/refs */

  useEffect(() => {
    if (!expandedDefaults) return

    const flattened = flattenValues(expandedDefaults as T)
    const serialized = JSON.stringify(flattened)

    if (serialized === serializedDefaultsRef.current) {
      return
    }

    baselineRef.current = flattened
    defaultValuesRef.current = expandedDefaults as T
    serializedDefaultsRef.current = serialized
    form.reset(expandedDefaults as T)
  }, [expandedDefaults, form])

  const defaultCompare = (a: unknown, b: unknown): boolean => {
    if (a === b) return true

    if (Array.isArray(a) && Array.isArray(b)) {
      return JSON.stringify(a) === JSON.stringify(b)
    }

    if (typeof a !== typeof b) return false

    // Handle arrays
    // Handle objects (but not null)
    if (a && b && typeof a === 'object' && typeof b === 'object') {
      return JSON.stringify(a) === JSON.stringify(b)
    }

    return false
  }

  const compare = compareValues || defaultCompare

  const handleSubmit = async (data: T) => {
    const dirtyValues = collectDirtyValues(form.formState.dirtyFields, data)
    const changedEntries = Object.entries(dirtyValues).filter(
      ([key, value]) => !compare(value, baselineRef.current[key])
    )

    if (changedEntries.length === 0) {
      toast.info(i18next.t('No changes to save'))
      return
    }

    const changedFields = changedEntries.reduce<Record<string, unknown>>(
      (acc, [key, value]) => {
        acc[key] = value
        return acc
      },
      {}
    )

    await onSubmit(data, changedFields)

    const flattenedValues = flattenValues(data)
    baselineRef.current = flattenedValues
    defaultValuesRef.current = data
    serializedDefaultsRef.current = JSON.stringify(flattenedValues)
    form.reset(data)
  }

  const handleReset = () => {
    form.reset(defaultValuesRef.current)
    toast.success(i18next.t('Form reset to saved values'))
  }

  return {
    form,
    // eslint-disable-next-line react-hooks/refs
    handleSubmit: form.handleSubmit(handleSubmit),
    handleReset,
    isDirty: form.formState.isDirty,
    isSubmitting: form.formState.isSubmitting,
  }
}
