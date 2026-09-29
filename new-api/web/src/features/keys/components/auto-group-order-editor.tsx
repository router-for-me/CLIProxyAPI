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
import { ArrowRight01Icon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { Reorder } from 'motion/react'
import { useMemo, type ComponentProps } from 'react'
import { useTranslation } from 'react-i18next'

import { AutoGroupOrderItem } from '@/components/auto-group-order-item'
import { Button } from '@/components/ui/button'
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
} from '@/components/ui/empty'
import { cn } from '@/lib/utils'

import {
  ApiKeyGroupCombobox,
  type ApiKeyGroupOption,
} from './api-key-group-combobox'
import { GroupRatioBadge } from './auto-group-visuals'

type AutoGroupOrderEditorProps = Omit<ComponentProps<'div'>, 'onChange'> & {
  value: string[]
  mode: 'inherit' | 'custom'
  options: ApiKeyGroupOption[]
  globalOptions: ApiKeyGroupOption[]
  maxCount: number
  onChange: (value: { groups: string[]; mode: 'inherit' | 'custom' }) => void
  'data-slot'?: string
  'data-form-root'?: string
}

export function AutoGroupOrderEditor(props: AutoGroupOrderEditorProps) {
  const { t } = useTranslation()
  const maxCount =
    Number.isInteger(props.maxCount) && props.maxCount > 0 ? props.maxCount : 5
  const isInheriting = props.mode === 'inherit'
  const atLimit = props.value.length >= maxCount
  const candidates = useMemo(
    () =>
      props.options.filter(
        (option) =>
          option.value !== 'auto' && !props.value.includes(option.value)
      ),
    [props.options, props.value]
  )

  const handleAdd = (group: string) => {
    if (atLimit || props.value.includes(group)) return
    props.onChange({
      groups: [...props.value, group],
      mode: 'custom',
    })
  }

  const handleRemove = (group: string) => {
    props.onChange({
      groups: props.value.filter((item) => item !== group),
      mode: 'custom',
    })
  }

  const handleMove = (index: number, direction: 'up' | 'down') => {
    const targetIndex = direction === 'up' ? index - 1 : index + 1
    if (targetIndex < 0 || targetIndex >= props.value.length) return
    const next = [...props.value]
    ;[next[index], next[targetIndex]] = [next[targetIndex], next[index]]
    props.onChange({ groups: next, mode: 'custom' })
  }

  return (
    <div
      id={props.id}
      data-slot={props['data-slot']}
      data-form-root={props['data-form-root']}
      role='group'
      tabIndex={-1}
      aria-label={props['aria-label'] || t('Auto group order')}
      aria-describedby={props['aria-describedby']}
      aria-invalid={props['aria-invalid']}
      className={cn('flex flex-col gap-3', props.className)}
    >
      <div className='flex items-center justify-between gap-3'>
        <p className='text-muted-foreground text-xs' aria-live='polite'>
          {isInheriting
            ? t('Using the complete global Auto order ({{count}} groups)', {
                count: props.globalOptions.length,
              })
            : t('{{count}} / {{max}} groups selected', {
                count: props.value.length,
                max: maxCount,
              })}
        </p>
        <Button
          type='button'
          variant='outline'
          size='sm'
          disabled={isInheriting}
          onClick={() => {
            props.onChange({ groups: [], mode: 'inherit' })
          }}
        >
          {t('Restore global Auto')}
        </Button>
      </div>

      <ApiKeyGroupCombobox
        options={candidates}
        value={undefined}
        onValueChange={handleAdd}
        placeholder={
          atLimit
            ? t('Maximum {{max}} groups selected', { max: maxCount })
            : t('Add Auto group')
        }
        disabled={atLimit || candidates.length === 0}
      />

      {isInheriting && props.globalOptions.length === 0 && (
        <Empty className='min-h-28 border'>
          <EmptyHeader>
            <EmptyTitle>{t('Inherit global Auto order')}</EmptyTitle>
            <EmptyDescription>
              {t('No available groups in the global Auto order.')}
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      )}

      {isInheriting && props.globalOptions.length > 0 && (
        <ol
          data-slot='global-auto-order'
          aria-label={t('Inherit global Auto order')}
          className='flex max-h-24 flex-wrap content-start gap-1.5 overflow-y-auto'
        >
          {props.globalOptions.map((option, index) => (
            <li key={option.value} className='flex min-w-0 items-center gap-1'>
              {index > 0 && (
                <HugeiconsIcon
                  icon={ArrowRight01Icon}
                  strokeWidth={2}
                  aria-hidden='true'
                  data-slot='global-auto-order-connector'
                  className='text-muted-foreground size-3.5 shrink-0'
                />
              )}
              <span
                data-slot='global-auto-order-chip'
                title={option.desc}
                className='bg-muted/30 flex min-w-0 items-center gap-1.5 rounded-md border px-2 py-1'
              >
                <span
                  data-slot='global-auto-order-index'
                  aria-hidden='true'
                  className='bg-primary/10 text-primary flex size-4 shrink-0 items-center justify-center rounded-full text-[10px] font-semibold tabular-nums'
                >
                  {index + 1}
                </span>
                <span
                  data-slot='global-auto-order-name'
                  className='max-w-40 truncate text-xs font-medium'
                >
                  {option.label}
                </span>
                {option.desc && (
                  <span
                    data-slot='global-auto-order-description'
                    className='sr-only'
                  >
                    {option.desc}
                  </span>
                )}
                <GroupRatioBadge ratio={option.ratio} />
              </span>
            </li>
          ))}
        </ol>
      )}

      {!isInheriting && props.value.length === 0 && (
        <Empty className='min-h-24 border'>
          <EmptyHeader>
            <EmptyTitle>{t('Auto group order')}</EmptyTitle>
            <EmptyDescription>
              {t(
                'No valid custom Auto groups remain. Add a group or restore global Auto.'
              )}
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      )}

      {!isInheriting && props.value.length > 0 && (
        <Reorder.Group
          axis='y'
          values={props.value}
          onReorder={(groups) => props.onChange({ groups, mode: 'custom' })}
          className='flex flex-col gap-2'
        >
          {props.value.map((group, index) => (
            <AutoGroupOrderItem
              key={group}
              group={group}
              index={index}
              count={props.value.length}
              onMove={handleMove}
              onRemove={handleRemove}
            />
          ))}
        </Reorder.Group>
      )}
    </div>
  )
}
