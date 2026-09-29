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
  useEffect,
  useState,
  type ChangeEvent,
  type FocusEvent,
  type InputHTMLAttributes,
  type MouseEvent as ReactMouseEvent,
} from 'react'

import { Input } from '@/components/ui/input'

function formatNumberDraft(value: number | string): string {
  if (value === '') return ''
  if (typeof value === 'number') {
    return Number.isFinite(value) ? String(value) : '0'
  }
  return value
}

function parseNumberDraft(value: string): number {
  if (value.trim() === '') return 0
  const next = Number(value)
  return Number.isFinite(next) ? next : 0
}

function isZeroDraft(value: string): boolean {
  return value.trim() !== '' && parseNumberDraft(value) === 0
}

type DraftNumberInputProps = Omit<
  InputHTMLAttributes<HTMLInputElement>,
  'type' | 'value' | 'onChange'
> & {
  value: number | string
  onValueChange: (next: number) => void
  selectZeroOnFocus?: boolean
}

export function DraftNumberInput({
  value,
  onValueChange,
  selectZeroOnFocus = true,
  onBlur,
  onFocus,
  onMouseUp,
  ...props
}: DraftNumberInputProps) {
  const [draft, setDraft] = useState(() => formatNumberDraft(value))
  const [focused, setFocused] = useState(false)

  useEffect(() => {
    if (!focused) {
      setDraft(formatNumberDraft(value))
    }
  }, [focused, value])

  const handleChange = (event: ChangeEvent<HTMLInputElement>) => {
    const nextDraft = event.target.value
    setDraft(nextDraft)
    onValueChange(parseNumberDraft(nextDraft))
  }

  const handleFocus = (event: FocusEvent<HTMLInputElement>) => {
    setFocused(true)
    onFocus?.(event)
    if (selectZeroOnFocus && isZeroDraft(event.currentTarget.value)) {
      event.currentTarget.select()
    }
  }

  const handleMouseUp = (event: ReactMouseEvent<HTMLInputElement>) => {
    onMouseUp?.(event)
    if (selectZeroOnFocus && isZeroDraft(event.currentTarget.value)) {
      event.preventDefault()
      event.currentTarget.select()
    }
  }

  const handleBlur = (event: FocusEvent<HTMLInputElement>) => {
    const normalized = parseNumberDraft(event.currentTarget.value)
    setFocused(false)
    setDraft(String(normalized))
    onValueChange(normalized)
    onBlur?.(event)
  }

  return (
    <Input
      {...props}
      type='number'
      value={draft}
      onChange={handleChange}
      onFocus={handleFocus}
      onMouseUp={handleMouseUp}
      onBlur={handleBlur}
    />
  )
}
