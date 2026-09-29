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
import { Filter, X } from 'lucide-react'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from '@/components/ui/command'
import {
  Popover,
  PopoverContent,
  PopoverDescription,
  PopoverHeader,
  PopoverTitle,
  PopoverTrigger,
} from '@/components/ui/popover'
import type {
  FlowNodeFilter,
  FlowNodeFilterOption,
  FlowNodeKind,
} from '@/features/dashboard/types'

interface FlowNodeFilterControlProps {
  stages: FlowNodeKind[]
  stageLabels: Record<FlowNodeKind, string>
  metricLabel: string
  formatMetricValue: (value: number) => string
  options: FlowNodeFilterOption[]
  selectedNodes: FlowNodeFilter[]
  onToggleNode: (filter: FlowNodeFilter) => void
  onRemoveNode: (filter: FlowNodeFilter) => void
  onClearNodes: () => void
}

function flowNodeFilterKey(filter: FlowNodeFilter): string {
  return `${filter.kind}\u0000${filter.id}`
}

export function FlowNodeFilterControl(props: FlowNodeFilterControlProps) {
  const { t } = useTranslation()
  const selectedKeys = useMemo(
    () => new Set(props.selectedNodes.map(flowNodeFilterKey)),
    [props.selectedNodes]
  )
  const optionLabels = useMemo(() => {
    const labels = new Map<string, FlowNodeFilterOption>()
    for (const option of props.options) {
      labels.set(
        flowNodeFilterKey({ kind: option.kind, id: option.value }),
        option
      )
    }
    return labels
  }, [props.options])
  const optionsByStage = useMemo(
    () =>
      props.stages
        .map((stage) => ({
          stage,
          options: props.options.filter((option) => option.kind === stage),
        }))
        .filter((group) => group.options.length > 0),
    [props.options, props.stages]
  )
  const selectedOptions = props.selectedNodes.map((filter) => {
    const option = optionLabels.get(flowNodeFilterKey(filter))
    return {
      ...filter,
      label: option?.label ?? filter.id,
    }
  })
  const selectedCount = selectedOptions.length

  return (
    <div className='flex min-w-0 flex-col gap-1.5'>
      <span className='text-muted-foreground text-xs font-medium'>
        {t('Node filters')}
      </span>
      <div className='flex min-w-0 flex-wrap items-center gap-1.5'>
        <Popover>
          <PopoverTrigger
            render={
              <Button
                type='button'
                variant='outline'
                size='sm'
                aria-label={t('Filter by node')}
              />
            }
          >
            <Filter data-icon='inline-start' aria-hidden='true' />
            {selectedCount > 0 ? t('Selected nodes') : t('All nodes')}
            {selectedCount > 0 && (
              <Badge variant='secondary' className='rounded-sm px-1'>
                {selectedCount}
              </Badge>
            )}
          </PopoverTrigger>
          <PopoverContent
            className='w-[min(28rem,calc(100vw-2rem))] p-0'
            align='start'
          >
            <PopoverHeader className='px-3 pt-3'>
              <PopoverTitle>{t('Node filters')}</PopoverTitle>
              <PopoverDescription>
                {t('Value metric')}: {props.metricLabel}
              </PopoverDescription>
            </PopoverHeader>
            <Command>
              <CommandInput placeholder={t('Filter by node')} />
              <CommandList>
                <CommandEmpty>{t('No nodes')}</CommandEmpty>
                {optionsByStage.map((group) => {
                  const stageLabel = t(props.stageLabels[group.stage])
                  return (
                    <CommandGroup key={group.stage} heading={stageLabel}>
                      {group.options.map((option) => {
                        const key = flowNodeFilterKey({
                          kind: option.kind,
                          id: option.value,
                        })
                        const metricValueLabel = props.formatMetricValue(
                          option.valueRaw
                        )
                        return (
                          <CommandItem
                            key={key}
                            value={`${stageLabel} ${option.label} ${props.metricLabel} ${metricValueLabel}`}
                            data-checked={selectedKeys.has(key)}
                            onSelect={() =>
                              props.onToggleNode({
                                kind: option.kind,
                                id: option.value,
                              })
                            }
                          >
                            <span
                              className='size-2.5 shrink-0 rounded-full'
                              style={{ backgroundColor: option.color }}
                              aria-hidden='true'
                            />
                            <span className='min-w-0 flex-1 truncate'>
                              {option.label}
                            </span>
                            <span className='text-muted-foreground flex shrink-0 items-center gap-1 text-xs'>
                              <span>{props.metricLabel}</span>
                              <span className='font-mono'>
                                {metricValueLabel}
                              </span>
                            </span>
                          </CommandItem>
                        )
                      })}
                    </CommandGroup>
                  )
                })}
                {selectedCount > 0 && (
                  <>
                    <CommandSeparator />
                    <CommandGroup>
                      <CommandItem
                        onSelect={props.onClearNodes}
                        className='justify-center text-center'
                      >
                        {t('Clear node filters')}
                      </CommandItem>
                    </CommandGroup>
                  </>
                )}
              </CommandList>
            </Command>
          </PopoverContent>
        </Popover>

        {selectedOptions.map((option) => (
          <Badge
            key={flowNodeFilterKey(option)}
            variant='secondary'
            className='max-w-[14rem] rounded-sm pr-1'
          >
            <span className='truncate'>
              {t(props.stageLabels[option.kind])}: {option.label}
            </span>
            <button
              type='button'
              className='hover:bg-muted-foreground/15 flex size-4 shrink-0 items-center justify-center rounded-sm'
              aria-label={t('Remove node filter')}
              onClick={() =>
                props.onRemoveNode({ kind: option.kind, id: option.id })
              }
            >
              <X aria-hidden='true' />
            </button>
          </Badge>
        ))}

        {selectedCount > 1 && (
          <Button
            type='button'
            variant='ghost'
            size='xs'
            onClick={props.onClearNodes}
          >
            {t('Clear')}
          </Button>
        )}
      </div>
    </div>
  )
}
