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
import { CalendarDays } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import dayjs from '@/lib/dayjs'
import { cn } from '@/lib/utils'

interface CompactDateTimeRangePickerProps {
  start?: Date
  end?: Date
  onChange: (range: { start?: Date; end?: Date }) => void
  className?: string
}

function toInputValue(date?: Date): string {
  return date ? dayjs(date).format('YYYY-MM-DDTHH:mm') : ''
}

function fromInputValue(value: string): Date | undefined {
  if (!value) return undefined
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? undefined : date
}

export function CompactDateTimeRangePicker({
  start,
  end,
  onChange,
  className,
}: CompactDateTimeRangePickerProps) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [draftStart, setDraftStart] = useState(toInputValue(start))
  const [draftEnd, setDraftEnd] = useState(toInputValue(end))

  const label = useMemo(() => {
    if (!start && !end) return t('Date Range')
    // The popover's <input type="datetime-local"> only supports minute
    // precision, so seconds are always 00 (manual pick) or 59 (preset
    // end-of-day). Hide them in the trigger label to keep the button
    // width compact while still showing the meaningful timestamp.
    const startText = start ? dayjs(start).format('YYYY-MM-DD HH:mm') : '-'
    const endText = end ? dayjs(end).format('YYYY-MM-DD HH:mm') : '-'
    return `${startText} ~ ${endText}`
  }, [end, start, t])

  const mobileLabel = useMemo(() => {
    if (!start || !end) return label
    if (dayjs(start).isSame(end, 'day')) {
      return `${dayjs(start).format('MM/DD HH:mm')}–${dayjs(end).format('HH:mm')}`
    }
    return label
  }, [start, end, label])

  const handleOpenChange = (nextOpen: boolean) => {
    if (nextOpen) {
      setDraftStart(toInputValue(start))
      setDraftEnd(toInputValue(end))
    }
    setOpen(nextOpen)
  }

  const applyDraft = () => {
    onChange({
      start: fromInputValue(draftStart),
      end: fromInputValue(draftEnd),
    })
    setOpen(false)
  }

  const applyPreset = (kind: 'today' | '7d' | 'week' | '30d' | 'month') => {
    const now = dayjs()
    const presets = {
      today: {
        start: now.startOf('day').toDate(),
        end: now.endOf('day').toDate(),
      },
      '7d': {
        start: now.subtract(6, 'day').startOf('day').toDate(),
        end: now.endOf('day').toDate(),
      },
      week: {
        start: now.startOf('week').toDate(),
        end: now.endOf('week').toDate(),
      },
      '30d': {
        start: now.subtract(29, 'day').startOf('day').toDate(),
        end: now.endOf('day').toDate(),
      },
      month: {
        start: now.startOf('month').toDate(),
        end: now.endOf('month').toDate(),
      },
    }
    const range = presets[kind]
    setDraftStart(toInputValue(range.start))
    setDraftEnd(toInputValue(range.end))
    onChange(range)
    setOpen(false)
  }

  return (
    <Popover open={open} onOpenChange={handleOpenChange}>
      <PopoverTrigger
        render={
          <Button
            type='button'
            variant='outline'
            aria-label={label}
            className={cn(
              'w-full justify-start gap-2 px-2.5 text-sm leading-5 font-normal tabular-nums',
              !start && !end && 'text-muted-foreground',
              className
            )}
          />
        }
      >
        <CalendarDays className='text-muted-foreground size-4 shrink-0' />
        <span className='hidden truncate sm:block'>{label}</span>
        <span className='min-w-0 [overflow-wrap:anywhere] whitespace-normal sm:hidden'>
          {mobileLabel}
        </span>
      </PopoverTrigger>
      <PopoverContent
        align='start'
        className='w-[min(520px,calc(100vw-2rem))] p-3'
      >
        <div className='space-y-3'>
          <div className='grid gap-2 sm:grid-cols-[1fr_auto_1fr] sm:items-end'>
            <div className='space-y-1.5'>
              <div className='text-muted-foreground text-xs'>
                {t('Start Time')}
              </div>
              <Input
                type='datetime-local'
                value={draftStart}
                aria-label={t('Start Time')}
                onChange={(e) => setDraftStart(e.target.value)}
                className='h-8 text-sm leading-5 tabular-nums'
              />
            </div>
            <span className='text-muted-foreground hidden pb-2 text-xs sm:block'>
              ~
            </span>
            <div className='space-y-1.5'>
              <div className='text-muted-foreground text-xs'>
                {t('End Time')}
              </div>
              <Input
                type='datetime-local'
                value={draftEnd}
                aria-label={t('End Time')}
                onChange={(e) => setDraftEnd(e.target.value)}
                className='h-8 text-sm leading-5 tabular-nums'
              />
            </div>
          </div>

          <div className='flex flex-wrap gap-1.5'>
            <Button
              type='button'
              variant='secondary'
              size='sm'
              className='h-7 flex-1 px-2 text-xs'
              onClick={() => applyPreset('today')}
            >
              {t('Today')}
            </Button>
            <Button
              type='button'
              variant='secondary'
              size='sm'
              className='h-7 flex-1 px-2 text-xs'
              onClick={() => applyPreset('7d')}
            >
              {t('7 Days')}
            </Button>
            <Button
              type='button'
              variant='secondary'
              size='sm'
              className='h-7 flex-1 px-2 text-xs'
              onClick={() => applyPreset('week')}
            >
              {t('This week')}
            </Button>
            <Button
              type='button'
              variant='secondary'
              size='sm'
              className='h-7 flex-1 px-2 text-xs'
              onClick={() => applyPreset('30d')}
            >
              {t('30 Days')}
            </Button>
            <Button
              type='button'
              variant='secondary'
              size='sm'
              className='h-7 flex-1 px-2 text-xs'
              onClick={() => applyPreset('month')}
            >
              {t('Current month')}
            </Button>
          </div>

          <div className='flex justify-end'>
            <Button size='sm' className='h-8' onClick={applyDraft}>
              {t('Confirm')}
            </Button>
          </div>
        </div>
      </PopoverContent>
    </Popover>
  )
}
