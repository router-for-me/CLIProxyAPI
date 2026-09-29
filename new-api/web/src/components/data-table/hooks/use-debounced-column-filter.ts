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
import type { ColumnFiltersState, OnChangeFn } from '@tanstack/react-table'
import * as React from 'react'

import { useDebounce } from '@/hooks/use-debounce'

type UseDebouncedColumnFilterOptions = {
  columnFilters: ColumnFiltersState
  columnId: string
  onColumnFiltersChange: OnChangeFn<ColumnFiltersState>
  delay?: number
}

export function useDebouncedColumnFilter({
  columnFilters,
  columnId,
  onColumnFiltersChange,
  delay = 500,
}: UseDebouncedColumnFilterOptions) {
  const value =
    (columnFilters.find((filter) => filter.id === columnId)?.value as
      | string
      | undefined) ?? ''
  const [inputValue, setInputValue] = React.useState(value)
  const [pendingValue, setPendingValue] = React.useState(value)
  const isComposingRef = React.useRef(false)
  const debouncedValue = useDebounce(pendingValue, delay)
  const onColumnFiltersChangeRef = React.useRef(onColumnFiltersChange)
  onColumnFiltersChangeRef.current = onColumnFiltersChange

  React.useEffect(() => {
    // Keep the input aligned when URL state changes outside the local field.
    if (!isComposingRef.current) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setInputValue(value)
    }
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setPendingValue(value)
  }, [value])

  React.useEffect(() => {
    if (debouncedValue === value) return

    onColumnFiltersChangeRef.current((previous) => {
      const filters = previous.filter((filter) => filter.id !== columnId)
      return debouncedValue
        ? [...filters, { id: columnId, value: debouncedValue }]
        : filters
    })
  }, [columnId, debouncedValue, value])

  const updateInputValue = React.useCallback((nextValue: string) => {
    setInputValue(nextValue)

    if (!isComposingRef.current) {
      setPendingValue(nextValue)
    }
  }, [])

  const handleChange = React.useCallback(
    (event: React.ChangeEvent<HTMLInputElement>) => {
      updateInputValue(event.target.value)
    },
    [updateInputValue]
  )

  const handleCompositionStart = React.useCallback(() => {
    isComposingRef.current = true
  }, [])

  const handleCompositionEnd = React.useCallback(
    (event: React.CompositionEvent<HTMLInputElement>) => {
      isComposingRef.current = false
      const nextValue = event.currentTarget.value
      setInputValue(nextValue)
      setPendingValue(nextValue)
    },
    []
  )

  const resetInput = React.useCallback(() => {
    isComposingRef.current = false
    setInputValue('')
    setPendingValue('')
  }, [])

  return {
    value,
    inputValue,
    setInputValue: updateInputValue,
    onChange: handleChange,
    onCompositionStart: handleCompositionStart,
    onCompositionEnd: handleCompositionEnd,
    resetInput,
  }
}
