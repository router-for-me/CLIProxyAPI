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
import { Minus, Plus } from 'lucide-react'
import { useState, useEffect, useRef } from 'react'

import { Label } from '@/components/ui/label'
import { cn } from '@/lib/utils'

interface NumericSpinnerInputProps {
  value: number | null | undefined
  onChange: (value: number) => void
  onCommit?: () => void
  min?: number
  max?: number
  step?: number
  disabled?: boolean
  className?: string
  label?: string
}

export function NumericSpinnerInput({
  value,
  onChange,
  onCommit,
  min = 0,
  max,
  step = 1,
  disabled = false,
  className,
  label,
}: NumericSpinnerInputProps) {
  const [localValue, setLocalValue] = useState(String(value ?? 0))
  const [editing, setEditing] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (!editing) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setLocalValue(String(value ?? 0))
    }
  }, [value, editing])

  const clamp = (v: number) => {
    let result = v
    if (min !== undefined) result = Math.max(min, result)
    if (max !== undefined) result = Math.min(max, result)
    return result
  }

  const handleIncrement = (e: React.MouseEvent) => {
    e.stopPropagation()
    if (disabled) return
    const next = clamp((Number(localValue) || 0) + step)
    setLocalValue(String(next))
    onChange(next)
  }

  const handleDecrement = (e: React.MouseEvent) => {
    e.stopPropagation()
    if (disabled) return
    const next = clamp((Number(localValue) || 0) - step)
    setLocalValue(String(next))
    onChange(next)
  }

  const handleStartEdit = () => {
    if (disabled) return
    setEditing(true)
    requestAnimationFrame(() => inputRef.current?.select())
  }

  const handleInputChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const raw = e.target.value
    if (raw === '' || raw === '-') {
      setLocalValue(raw)
      return
    }
    if (!/^-?\d+$/.test(raw)) return
    setLocalValue(raw)
  }

  const commitValue = () => {
    setEditing(false)
    const num = Number(localValue)
    if (Number.isNaN(num) || localValue === '' || localValue === '-') {
      setLocalValue(String(value ?? 0))
      return
    }
    const clamped = clamp(num)
    setLocalValue(String(clamped))
    if (clamped !== (value ?? 0)) {
      onChange(clamped)
    }
  }

  const handleControlBlur = (e: React.FocusEvent<HTMLDivElement>) => {
    if (
      e.relatedTarget instanceof Node &&
      e.currentTarget.contains(e.relatedTarget)
    ) {
      return
    }
    onCommit?.()
  }

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      // Blurring routes Enter through the same focusout path as clicking
      // away (input onBlur -> commitValue, container onBlur -> onCommit),
      // so commit and onCommit each fire exactly once.
      inputRef.current?.blur()
    } else if (e.key === 'Escape') {
      setEditing(false)
      setLocalValue(String(value ?? 0))
    }
  }

  const atMin = min !== undefined && Number(localValue) <= min
  const atMax = max !== undefined && Number(localValue) >= max

  return (
    <div className={cn('inline-flex items-center', className)}>
      {label && (
        <Label className='text-muted-foreground mr-1.5 text-xs'>{label}</Label>
      )}
      <div
        onBlur={handleControlBlur}
        className={cn(
          'group/spinner border-input inline-flex h-7 items-center gap-0 rounded-md border transition-colors',
          !disabled && 'hover:bg-muted/60',
          editing && 'bg-muted/60 ring-primary/30 ring-1'
        )}
      >
        <button
          type='button'
          tabIndex={-1}
          aria-label='Decrement'
          onClick={handleDecrement}
          disabled={disabled || atMin}
          className={cn(
            'text-muted-foreground/0 group-hover/spinner:text-muted-foreground flex h-7 w-6 shrink-0 items-center justify-center rounded-l-md transition-colors',
            !disabled &&
              !atMin &&
              'group-hover/spinner:hover:text-foreground group-hover/spinner:hover:bg-muted',
            (disabled || atMin) && 'group-hover/spinner:opacity-30'
          )}
        >
          <Minus className='size-3' />
        </button>

        {editing ? (
          <input
            ref={inputRef}
            type='text'
            value={localValue}
            onChange={handleInputChange}
            onBlur={commitValue}
            onKeyDown={handleKeyDown}
            className='h-7 w-10 bg-transparent text-center font-mono text-sm outline-none'
            autoFocus
          />
        ) : (
          <button
            type='button'
            onClick={handleStartEdit}
            disabled={disabled}
            title={localValue}
            className={cn(
              'h-7 min-w-8 max-w-16 cursor-text truncate px-1 text-center font-mono text-sm tabular-nums',
              disabled && 'cursor-default opacity-50'
            )}
          >
            {localValue}
          </button>
        )}

        <button
          type='button'
          tabIndex={-1}
          aria-label='Increment'
          onClick={handleIncrement}
          disabled={disabled || atMax}
          className={cn(
            'text-muted-foreground/0 group-hover/spinner:text-muted-foreground flex h-7 w-6 shrink-0 items-center justify-center rounded-r-md transition-colors',
            !disabled &&
              !atMax &&
              'group-hover/spinner:hover:text-foreground group-hover/spinner:hover:bg-muted',
            (disabled || atMax) && 'group-hover/spinner:opacity-30'
          )}
        >
          <Plus className='size-3' />
        </button>
      </div>
    </div>
  )
}
