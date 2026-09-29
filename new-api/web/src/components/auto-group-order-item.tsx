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
  ArrowDown01Icon,
  ArrowUp01Icon,
  Cancel01Icon,
  Drag01Icon,
} from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { Reorder, useDragControls } from 'motion/react'
import type { KeyboardEvent, PointerEvent, ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

type AutoGroupOrderItemProps = {
  group: string
  index: number
  count: number
  leading?: ReactNode
  children?: ReactNode
  onMove: (index: number, direction: 'up' | 'down') => void
  onRemove: (group: string) => void
}

export function AutoGroupOrderItem(props: AutoGroupOrderItemProps) {
  const { t } = useTranslation()
  const dragControls = useDragControls()

  const handleDragStart = (event: PointerEvent<HTMLButtonElement>) => {
    dragControls.start(event)
  }

  const handleDragKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
    if (event.key === 'ArrowUp') {
      event.preventDefault()
      props.onMove(props.index, 'up')
    }
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      props.onMove(props.index, 'down')
    }
  }

  return (
    <Reorder.Item
      value={props.group}
      dragListener={false}
      dragMomentum={false}
      dragControls={dragControls}
      className='bg-background relative flex min-w-0 items-center gap-2 rounded-lg border p-2'
    >
      <Button
        type='button'
        variant='ghost'
        size='icon-sm'
        className='text-muted-foreground cursor-grab touch-none font-mono active:cursor-grabbing'
        aria-label={t('Drag {{group}} to reorder', { group: props.group })}
        title={t('Drag {{group}} to reorder', { group: props.group })}
        onPointerDown={handleDragStart}
        onKeyDown={handleDragKeyDown}
      >
        <HugeiconsIcon icon={Drag01Icon} strokeWidth={2} aria-hidden='true' />
      </Button>
      {props.leading}
      <div className='flex min-w-0 flex-1 flex-wrap items-center gap-2'>
        <span
          className='min-w-0 truncate text-sm font-medium'
          title={props.group}
        >
          {props.group}
        </span>
        {props.children}
      </div>
      <div className='flex shrink-0 gap-1'>
        <Button
          type='button'
          variant='ghost'
          size='icon-sm'
          disabled={props.index === 0}
          aria-label={t('Move {{group}} up', { group: props.group })}
          onClick={() => props.onMove(props.index, 'up')}
        >
          <HugeiconsIcon
            icon={ArrowUp01Icon}
            strokeWidth={2}
            aria-hidden='true'
          />
        </Button>
        <Button
          type='button'
          variant='ghost'
          size='icon-sm'
          disabled={props.index === props.count - 1}
          aria-label={t('Move {{group}} down', { group: props.group })}
          onClick={() => props.onMove(props.index, 'down')}
        >
          <HugeiconsIcon
            icon={ArrowDown01Icon}
            strokeWidth={2}
            aria-hidden='true'
          />
        </Button>
        <Button
          type='button'
          variant='ghost'
          size='icon-sm'
          aria-label={t('Remove {{group}}', { group: props.group })}
          onClick={() => props.onRemove(props.group)}
        >
          <HugeiconsIcon
            icon={Cancel01Icon}
            strokeWidth={2}
            aria-hidden='true'
          />
        </Button>
      </div>
    </Reorder.Item>
  )
}
