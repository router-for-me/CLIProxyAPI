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
import { Add01Icon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { ArrowRightLeft } from 'lucide-react'
import * as React from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  Combobox,
  ComboboxChip,
  ComboboxChips,
  ComboboxChipsInput,
  ComboboxCollection,
  ComboboxContent,
  ComboboxEmpty,
  ComboboxItem,
  ComboboxList,
  ComboboxValue,
  useComboboxAnchor,
} from '@/components/ui/combobox'
import { copyToClipboard } from '@/lib/copy-to-clipboard'
import { cn } from '@/lib/utils'

export type Option = {
  label: string
  value: string
  /**
   * Secondary text shown beside the option in the dropdown. Chips for hinted
   * values also carry a marker icon whose tooltip repeats the hint.
   */
  hint?: string
  /**
   * Leading icon rendered before the label in the dropdown and on the chip.
   * Decorative only: it never changes the accessible name.
   */
  icon?: React.ReactNode
}

interface MultiSelectProps {
  options: Option[]
  selected: string[]
  onChange: (values: string[]) => void
  placeholder?: string
  className?: string
  allowCreate?: boolean
  /**
   * Label shown for the "create" item in the dropdown.
   * Supports the `{{value}}` placeholder which is replaced with the typed input.
   * Falls back to `Add "{{value}}"` when omitted.
   */
  createLabel?: string
  /** Empty state text. Defaults to "No matching items". */
  emptyText?: string
  /** Optional `id` to wire labels/aria-describedby to the input. */
  id?: string
  /** Disable the entire control. */
  disabled?: boolean
  /**
   * Limits rendered chips while keeping all values selected.
   * Hidden values remain searchable/removable from the dropdown.
   */
  maxVisibleChips?: number
  /**
   * Replaces individual chips with a compact summary while preserving the
   * normal dropdown/search behaviour.
   */
  renderSelectedSummary?: (values: string[]) => React.ReactNode
  /**
   * When true, clicking a chip's label copies its value to the clipboard
   * instead of being inert. The remove (×) button keeps its own behaviour.
   */
  copyChipOnClick?: boolean
}

const COMMA_REGEX = /[,，\n]/

function splitDraft(value: string): { completed: string[]; draft: string } {
  if (!COMMA_REGEX.test(value)) {
    return { completed: [], draft: value }
  }
  const normalized = value.replaceAll('，', ',').replaceAll('\n', ',')
  const parts = normalized.split(',')
  const draft = parts.at(-1) ?? ''
  const completed = parts
    .slice(0, -1)
    .map((part) => part.trim())
    .filter(Boolean)
  return { completed, draft }
}

/**
 * MultiSelect — tags/chips style multi-select built on Base UI Combobox.
 *
 * Behaviour:
 * - Search filters built-in options (Base UI handles fuzzy filtering).
 * - When `allowCreate` is true, custom values can be added inline:
 *   - Type and press Enter / "," to add a single value.
 *   - Paste a comma- (or newline-) separated list to add many at once.
 *   - A "Add \"<value>\"" item appears at the top of the dropdown when the
 *     typed text doesn't match any option.
 * - Backspace on an empty input removes the last selected chip (Base UI default).
 * - `maxVisibleChips` can cap large selections and show a compact "+N more"
 *   summary so forms do not grow vertically without bound.
 *
 * Focus/border styling is inherited from `ComboboxChips`, which uses the same
 * tokens as `Input` so it stays visually consistent with other form fields.
 */
export function MultiSelect(props: MultiSelectProps) {
  const { t } = useTranslation()
  const placeholder = props.placeholder ?? t('Select items...')

  // Anchor the popup to the chips container so its width tracks the entire
  // input row, not just the leftover space at the end of wrapped chips.
  const chipsAnchorRef = useComboboxAnchor()

  const [inputValue, setInputValue] = React.useState('')
  const [open, setOpen] = React.useState(false)
  const [expanded, setExpanded] = React.useState(false)

  const selectedSet = React.useMemo(
    () => new Set(props.selected),
    [props.selected]
  )

  // Lookup of value -> display label so chips and items can show friendly names
  // even when the underlying option list changes (e.g. custom-added values).
  const labelMap = React.useMemo(() => {
    const map = new Map<string, string>()
    for (const option of props.options) {
      map.set(option.value, option.label)
    }
    return map
  }, [props.options])

  const hintMap = React.useMemo(() => {
    const map = new Map<string, string>()
    for (const option of props.options) {
      if (option.hint) map.set(option.value, option.hint)
    }
    return map
  }, [props.options])

  const iconMap = React.useMemo(() => {
    const map = new Map<string, React.ReactNode>()
    for (const option of props.options) {
      if (option.icon) map.set(option.value, option.icon)
    }
    return map
  }, [props.options])

  const trimmedInput = inputValue.trim()
  const inputMatchesExisting =
    trimmedInput.length > 0 &&
    (selectedSet.has(trimmedInput) ||
      props.options.some(
        (option) =>
          option.value === trimmedInput || option.label === trimmedInput
      ))

  const canCreate =
    props.allowCreate === true &&
    trimmedInput.length > 0 &&
    !inputMatchesExisting

  // We expose all known option values + every currently selected value to Base
  // UI's items list. This way Base UI filters them by the search query and the
  // user can still see the chip labels mapped correctly.
  const items = React.useMemo(() => {
    const set = new Set<string>(props.options.map((option) => option.value))
    for (const value of props.selected) {
      set.add(value)
    }
    if (canCreate) {
      set.add(trimmedInput)
    }
    return [...set]
  }, [props.options, props.selected, canCreate, trimmedInput])

  const addValues = React.useCallback(
    (values: string[]) => {
      const next: string[] = []
      const seen = new Set<string>(props.selected)
      for (const raw of values) {
        const value = raw.trim()
        if (!value) continue
        if (seen.has(value)) continue
        seen.add(value)
        next.push(value)
      }
      if (next.length === 0) return
      props.onChange([...props.selected, ...next])
    },
    [props]
  )

  const handleInputValueChange = (value: string) => {
    if (!props.allowCreate) {
      setInputValue(value)
      return
    }
    const parsed = splitDraft(value)
    if (parsed.completed.length > 0) {
      addValues(parsed.completed)
      setInputValue(parsed.draft)
      return
    }
    setInputValue(value)
  }

  const handlePaste = (event: React.ClipboardEvent<HTMLInputElement>): void => {
    if (!props.allowCreate || props.disabled) return

    const pasted = event.clipboardData.getData('text/plain')
    if (!COMMA_REGEX.test(pasted)) return

    event.preventDefault()
    const input = event.currentTarget
    const value =
      input.value.slice(0, input.selectionStart ?? input.value.length) +
      pasted +
      input.value.slice(input.selectionEnd ?? input.value.length)

    // A pasted batch is complete, including its final value. Read the clipboard
    // before the single-line input can strip newline separators.
    addValues(value.split(COMMA_REGEX))
    setInputValue('')
  }

  const handleValueChange = (next: string[]) => {
    props.onChange(next)
    // When an item is picked (multiple mode), Base UI keeps the input but most
    // UX patterns clear it. Clearing once a value is added makes batch picking
    // feel snappier and matches popular chip-style multiselects.
    if (next.length > props.selected.length) {
      setInputValue('')
    }
  }

  const handleCopyChip = React.useCallback(
    async (
      event: React.MouseEvent<HTMLButtonElement>,
      value: string,
      label: string
    ) => {
      // Prevent the click from toggling the combobox popup or focusing input.
      event.preventDefault()
      event.stopPropagation()
      const ok = await copyToClipboard(value)
      if (ok) {
        toast.success(t('Copied: {{model}}', { model: label }))
      } else {
        toast.error(t('Failed to copy'))
      }
    },
    [t]
  )

  const handleKeyDown = (event: React.KeyboardEvent<HTMLInputElement>) => {
    // Enter without a highlighted option commits the typed value.
    if (event.key === 'Enter' && props.allowCreate && canCreate) {
      // Only fire when Base UI has no highlighted item to select. We rely on
      // the highlighted item's data attribute on the popup. If the popup is
      // closed or empty, manually commit the typed value.
      const popup = document.querySelector<HTMLElement>(
        '[data-slot="combobox-content"][data-open]'
      )
      const hasHighlight = popup?.querySelector('[data-highlighted]') != null
      if (!hasHighlight) {
        event.preventDefault()
        addValues([trimmedInput])
        setInputValue('')
      }
    }
  }

  return (
    <Combobox
      multiple
      items={items}
      value={props.selected}
      onValueChange={handleValueChange}
      inputValue={inputValue}
      onInputValueChange={handleInputValueChange}
      open={open}
      onOpenChange={setOpen}
      disabled={props.disabled}
    >
      <ComboboxChips
        ref={chipsAnchorRef}
        className={cn('w-full', props.className)}
      >
        <ComboboxValue>
          {(values: string[]) => {
            if (props.renderSelectedSummary) {
              return (
                <span className='bg-muted text-muted-foreground flex h-[calc(--spacing(5.25))] w-fit items-center justify-center rounded-sm px-1.5 font-mono text-xs font-medium whitespace-nowrap'>
                  {props.renderSelectedSummary(values)}
                </span>
              )
            }

            const shouldLimit =
              typeof props.maxVisibleChips === 'number' && !expanded
            const visibleValues = shouldLimit
              ? values.slice(0, props.maxVisibleChips)
              : values
            const hiddenCount = values.length - visibleValues.length

            return (
              <>
                {visibleValues.map((value) => {
                  const label = labelMap.get(value) ?? value
                  const hint = hintMap.get(value)
                  const icon = iconMap.get(value)
                  return (
                    <ComboboxChip key={value}>
                      {icon && (
                        <span
                          aria-hidden='true'
                          className='inline-flex shrink-0'
                        >
                          {icon}
                        </span>
                      )}
                      {props.copyChipOnClick ? (
                        <button
                          type='button'
                          onClick={(event) =>
                            handleCopyChip(event, value, label)
                          }
                          onPointerDown={(event) => event.stopPropagation()}
                          title={t('Click to copy')}
                          className='max-w-[16rem] cursor-pointer truncate rounded-sm hover:underline'
                        >
                          {label}
                        </button>
                      ) : (
                        <span className='max-w-[16rem] truncate'>{label}</span>
                      )}
                      {hint && (
                        <span
                          title={hint}
                          aria-hidden='true'
                          className='text-muted-foreground inline-flex shrink-0'
                        >
                          <ArrowRightLeft className='size-3' />
                        </span>
                      )}
                    </ComboboxChip>
                  )
                })}
                {hiddenCount > 0 && (
                  <button
                    type='button'
                    onClick={(event) => {
                      event.preventDefault()
                      event.stopPropagation()
                      setExpanded(true)
                    }}
                    onPointerDown={(event) => event.stopPropagation()}
                    title={t('Show All')}
                    className='bg-muted text-muted-foreground hover:bg-muted/80 hover:text-foreground flex h-[calc(--spacing(5.25))] w-fit cursor-pointer items-center justify-center rounded-sm px-1.5 text-xs font-medium whitespace-nowrap transition-colors'
                  >
                    {t('+{{count}} more', { count: hiddenCount })}
                  </button>
                )}
                {expanded &&
                  typeof props.maxVisibleChips === 'number' &&
                  values.length > props.maxVisibleChips && (
                    <button
                      type='button'
                      onClick={(event) => {
                        event.preventDefault()
                        event.stopPropagation()
                        setExpanded(false)
                      }}
                      onPointerDown={(event) => event.stopPropagation()}
                      title={t('Collapse')}
                      className='bg-muted text-muted-foreground hover:bg-muted/80 hover:text-foreground flex h-[calc(--spacing(5.25))] w-fit cursor-pointer items-center justify-center rounded-sm px-1.5 text-xs font-medium whitespace-nowrap transition-colors'
                    >
                      {t('Collapse')}
                    </button>
                  )}
              </>
            )
          }}
        </ComboboxValue>
        <ComboboxChipsInput
          id={props.id}
          placeholder={
            props.selected.length === 0 && !props.renderSelectedSummary
              ? placeholder
              : undefined
          }
          onKeyDown={handleKeyDown}
          onPaste={handlePaste}
          aria-label={placeholder}
        />
      </ComboboxChips>

      <ComboboxContent anchor={chipsAnchorRef}>
        <ComboboxList>
          <ComboboxCollection>
            {(item: string) => {
              const isCreate = canCreate && item === trimmedInput
              const label = labelMap.get(item) ?? item
              const hint = hintMap.get(item)
              const icon = iconMap.get(item)
              return (
                <ComboboxItem
                  key={item}
                  value={item}
                  className={isCreate ? 'text-foreground' : undefined}
                  aria-description={hint}
                >
                  {isCreate ? (
                    <>
                      <HugeiconsIcon
                        icon={Add01Icon}
                        strokeWidth={2}
                        className='text-muted-foreground'
                        aria-hidden='true'
                      />
                      <span className='truncate'>
                        {props.createLabel
                          ? t(props.createLabel, { value: item })
                          : t('Add "{{value}}"', { value: item })}
                      </span>
                    </>
                  ) : (
                    <>
                      {icon && (
                        <span
                          aria-hidden='true'
                          className='inline-flex shrink-0'
                        >
                          {icon}
                        </span>
                      )}
                      <span className='truncate'>{label}</span>
                      {hint && (
                        <span
                          aria-hidden='true'
                          className='text-muted-foreground ml-auto max-w-40 shrink-0 truncate text-xs'
                        >
                          {hint}
                        </span>
                      )}
                    </>
                  )}
                </ComboboxItem>
              )
            }}
          </ComboboxCollection>
        </ComboboxList>
        <ComboboxEmpty>
          {props.emptyText ?? t('No matching items')}
        </ComboboxEmpty>
      </ComboboxContent>
    </Combobox>
  )
}
